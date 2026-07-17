package fulfilmentresolver_test

// The resolver closes the ambiguity loop (V2-ADV-009):
//   EDG-005 continuation: UNKNOWN (timeout-after-success) -> enquiry ->
//     ACTIVE exactly once, ONE journal, pool utilised once
//   EDG-007: crash between tx1 and tx2 (stale SENT attempt) -> enquiry ->
//     recovered exactly once, never re-lent
//   EDG-008: never-landed instruction -> NOT_FOUND -> FAILED + release
//   still-unknown -> quiet reschedule, VR-7b: no new outbox events

import (
	"context"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/ledger"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/mno"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/fulfilmentresolver"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/origination"
	"github.com/ArowuTest/telco-credit-platform/simulator/sim"
)

type fixture struct {
	db       *testutil.DB
	orig     *origination.Service
	resolver *fulfilmentresolver.Service
	sim      *sim.Simulator
}

func tenantCtx() context.Context { return platform.WithTenant(context.Background(), "SIM_NG") }

func newFixture(t *testing.T, suffix string, simHold time.Duration, adapterTimeoutMs int) *fixture {
	t.Helper()
	db := testutil.MustSetup(t, suffix)
	simulator := sim.New(slog.Default(), "res-test", simHold)
	srv := httptest.NewServer(simulator.Handler())
	t.Cleanup(srv.Close)

	cfgW := configsvc.New(db.Worker)
	ctx := context.Background()
	content := fmt.Sprintf(`{"fulfilment_url":%q,"request_timeout_ms":%d,"retry_budget":0,"circuit_error_threshold_pct":50,"circuit_min_requests":20}`, srv.URL, adapterTimeoutMs)
	c, err := cfgW.CreateDraft(ctx, "telco.adapter", "telco:SIM_NG", "alice", "test sim", []byte(content))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfgW.Submit(ctx, c.ConfigVersionID, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := cfgW.Approve(ctx, c.ConfigVersionID, "bob"); err != nil {
		t.Fatal(err)
	}
	if err := cfgW.Activate(ctx, c.ConfigVersionID, "bob", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	appCfg := configsvc.New(db.App)
	adapter := mno.NewHTTPAdapter(appCfg)
	orig := origination.New(db.App, appCfg, ledger.New(appCfg), adapter, slog.Default())
	resolver := fulfilmentresolver.New(db.App, appCfg, adapter, orig, slog.Default())
	return &fixture{db: db, orig: orig, resolver: resolver, sim: simulator}
}

func (f *fixture) makeDue(t *testing.T) {
	t.Helper()
	// Pull every scheduled enquiry into the past so RunOnce claims it now.
	if _, err := f.db.Admin.Exec(context.Background(),
		`UPDATE fulfilment_attempts SET next_enquiry_at = now() - interval '1 second'
		 WHERE state = 'UNKNOWN'`); err != nil {
		t.Fatal(err)
	}
}

func TestEDG005_Continuation_ResolverActivatesExactlyOnce(t *testing.T) {
	f := newFixture(t, "res_edg005", 2*time.Second, 300)
	f.seed(t, "sub_r1", "tok_TIMEOUT_r1")

	offers, err := f.orig.GetOffers(tenantCtx(), "prg_sim_airtime01", "tok_TIMEOUT_r1")
	if err != nil {
		t.Fatal(err)
	}
	res, err := f.orig.Confirm(tenantCtx(), origination.ConfirmCmd{
		ProgrammeID: "prg_sim_airtime01", OfferID: offers[0].OfferID, MSISDNToken: "tok_TIMEOUT_r1",
		IdemKey: "res-to-1", CorrelationID: "cor-res-to",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Advance.State != entity.AdvFulfilmentUnknown {
		t.Fatalf("precondition: want UNKNOWN, got %s", res.Advance.State)
	}

	f.makeDue(t)
	n, err := f.resolver.RunOnce(context.Background(), "SIM_NG", 10)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("resolver must resolve exactly 1, got %d", n)
	}

	// ACTIVE exactly once: one journal, utilisation once, no double effects.
	var state string
	if err := f.db.Admin.QueryRow(context.Background(),
		`SELECT state FROM advances WHERE advance_id=$1`, res.Advance.AdvanceID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "ACTIVE" {
		t.Fatalf("resolver must ACTIVATE after enquiry confirms, got %s", state)
	}
	var journals int
	var reserved, utilised int64
	if err := f.db.Admin.QueryRow(context.Background(), `
		SELECT (SELECT count(*) FROM journals),
		       (SELECT reserved_minor FROM funding_pools WHERE pool_id='pool_sim_01'),
		       (SELECT utilised_minor FROM funding_pools WHERE pool_id='pool_sim_01')`).
		Scan(&journals, &reserved, &utilised); err != nil {
		t.Fatal(err)
	}
	if journals != 1 || reserved != 0 || utilised != 5_000 {
		t.Fatalf("exactly-once violated: journals=%d reserved=%d utilised=%d", journals, reserved, utilised)
	}

	// Idempotent re-run: nothing left to resolve, nothing double-posted.
	f.makeDue(t)
	if n, err := f.resolver.RunOnce(context.Background(), "SIM_NG", 10); err != nil || n != 0 {
		t.Fatalf("re-run must be a no-op: n=%d err=%v", n, err)
	}
}

func TestEDG007_CrashBetweenTx1AndTx2_StaleSentRecoveredOnce(t *testing.T) {
	f := newFixture(t, "res_edg007", 0, 2_000)
	f.seed(t, "sub_r2", "tok_crash_r2")

	// Simulate the crash: run tx1's effects manually (advance PENDING with a
	// SENT attempt), then credit the simulator as if the call landed before
	// the process died — the EDG-007 shape.
	offers, err := f.orig.GetOffers(tenantCtx(), "prg_sim_airtime01", "tok_crash_r2")
	if err != nil {
		t.Fatal(err)
	}
	advID := f.manualTx1(t, offers[0], "tok_crash_r2")
	f.sim.CreditDirect(advID, 5_000, "NGN") // telco succeeded; we never heard

	// Stale-SENT threshold: backdate the attempt past delays[0]=10s.
	if _, err := f.db.Admin.Exec(context.Background(),
		`UPDATE fulfilment_attempts SET submitted_at = now() - interval '1 hour'`); err != nil {
		t.Fatal(err)
	}

	n, err := f.resolver.RunOnce(context.Background(), "SIM_NG", 10)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("stale SENT must be resolved, got %d", n)
	}
	var state string
	var journals int
	if err := f.db.Admin.QueryRow(context.Background(), `
		SELECT (SELECT state FROM advances WHERE advance_id=$1),
		       (SELECT count(*) FROM journals)`, advID).Scan(&state, &journals); err != nil {
		t.Fatal(err)
	}
	if state != "ACTIVE" || journals != 1 {
		t.Fatalf("EDG-007: recover exactly once — state=%s journals=%d", state, journals)
	}
}

func TestEDG008_NeverLanded_NotFoundFailsAndReleases(t *testing.T) {
	f := newFixture(t, "res_edg008", 0, 2_000)
	f.seed(t, "sub_r3", "tok_crash_r3")

	offers, err := f.orig.GetOffers(tenantCtx(), "prg_sim_airtime01", "tok_crash_r3")
	if err != nil {
		t.Fatal(err)
	}
	_ = f.manualTx1(t, offers[0], "tok_crash_r3") // crash BEFORE the call: simulator never saw it
	if _, err := f.db.Admin.Exec(context.Background(),
		`UPDATE fulfilment_attempts SET submitted_at = now() - interval '1 hour'`); err != nil {
		t.Fatal(err)
	}

	if n, err := f.resolver.RunOnce(context.Background(), "SIM_NG", 10); err != nil || n != 1 {
		t.Fatalf("never-landed must resolve definitively: n=%d err=%v", n, err)
	}
	var state string
	var reserved int64
	var journals int
	if err := f.db.Admin.QueryRow(context.Background(), `
		SELECT (SELECT state FROM advances),
		       (SELECT reserved_minor FROM funding_pools WHERE pool_id='pool_sim_01'),
		       (SELECT count(*) FROM journals)`).Scan(&state, &reserved, &journals); err != nil {
		t.Fatal(err)
	}
	if state != "FULFILMENT_FAILED" || reserved != 0 || journals != 0 {
		t.Fatalf("EDG-008: state=%s reserved=%d journals=%d — want FAILED/0/0", state, reserved, journals)
	}
}

func TestVR7b_StillUnknown_QuietRescheduleNoEventFlood(t *testing.T) {
	f := newFixture(t, "res_vr7b", 10*time.Second, 300)
	f.seed(t, "sub_r4", "tok_TIMEOUT_r4")

	offers, err := f.orig.GetOffers(tenantCtx(), "prg_sim_airtime01", "tok_TIMEOUT_r4")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.orig.Confirm(tenantCtx(), origination.ConfirmCmd{
		ProgrammeID: "prg_sim_airtime01", OfferID: offers[0].OfferID, MSISDNToken: "tok_TIMEOUT_r4",
		IdemKey: "res-vr7b", CorrelationID: "cor-vr7b",
	}); err != nil {
		t.Fatal(err)
	}
	var eventsBefore int
	if err := f.db.Admin.QueryRow(context.Background(), `SELECT count(*) FROM outbox`).Scan(&eventsBefore); err != nil {
		t.Fatal(err)
	}

	// Enquiry will ALSO be unknown? No — enquiry is a GET that answers
	// immediately with SUCCESS (the transaction exists). To keep it unknown,
	// point the enquiry at a droppable state: use a token whose transaction
	// we remove, so the enquiry 404s? That would resolve as FAILED. Instead:
	// hold the enquiry too by removing the record and re-adding after? For
	// VR-7b we only need "resolver cycle that ends still-unknown": simulate
	// by making the enquiry time out — shrink adapter timeout is fixture-
	// level, so instead drop the sim transaction and restore it after
	// asserting quietness is not possible... Simplest honest path: make the
	// enquiry itself time out using the TIMEOUT hold on the enquiry route.
	f.sim.HoldEnquiries(true)
	defer f.sim.HoldEnquiries(false)

	f.makeDue(t)
	if n, err := f.resolver.RunOnce(context.Background(), "SIM_NG", 10); err != nil || n != 0 {
		t.Fatalf("still-unknown cycle must resolve nothing: n=%d err=%v", n, err)
	}
	var eventsAfter, enquiryCount int
	if err := f.db.Admin.QueryRow(context.Background(), `
		SELECT (SELECT count(*) FROM outbox),
		       (SELECT enquiry_count FROM fulfilment_attempts LIMIT 1)`).
		Scan(&eventsAfter, &enquiryCount); err != nil {
		t.Fatal(err)
	}
	if eventsAfter != eventsBefore {
		t.Fatalf("VR-7b violated: still-unknown cycle emitted %d new events", eventsAfter-eventsBefore)
	}
	if enquiryCount != 1 {
		t.Fatalf("enquiry must be counted and rescheduled: count=%d", enquiryCount)
	}
}

// --- fixture helpers -------------------------------------------------------

func (f *fixture) seed(t *testing.T, subID, token string) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.db.Admin.Exec(ctx, `
		INSERT INTO subscriber_accounts (subscriber_account_id, telco_id, msisdn_token, status)
		VALUES ($1,'SIM_NG',$2,'ACTIVE')`, subID, token); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(ctx, `
		INSERT INTO decision_snapshots (decision_snapshot_id, telco_id, subscriber_account_id,
		  max_face_value_minor, currency, config_version_id)
		VALUES ('dec_'||$1,'SIM_NG',$1,50000,'NGN','cfg_seed_product_airtime_v1')`, subID); err != nil {
		t.Fatal(err)
	}
}

// manualTx1 replicates the saga's tx1 via the repos (as if the process died
// right after commit): advance PENDING_FULFILMENT with a SENT attempt.
func (f *fixture) manualTx1(t *testing.T, offer entity.Offer, token string) string {
	t.Helper()
	ctx := tenantCtx()
	advances := repo.Advances{}
	offersR := repo.Offers{}
	poolsR := repo.FundingPools{}
	attempts := repo.Attempts{}
	subs := repo.Subscribers{}

	advID := platform.NewID("adv")
	if err := repo.WithTenantTx(ctx, f.db.App, func(tx pgx.Tx) error {
		sub, err := subs.GetLiveByToken(ctx, tx, token)
		if err != nil {
			return err
		}
		adv := entity.Advance{
			AdvanceID: advID, TelcoID: "SIM_NG", ProgrammeID: offer.ProgrammeID,
			SubscriberAccountID: sub.SubscriberAccountID, OfferID: offer.OfferID,
			IdempotencyKey: "crash-" + advID, CorrelationID: "cor-crash-" + advID,
			State: entity.AdvRequested, Version: 1,
			FaceValue: offer.FaceValue, Fee: offer.Fee, Disbursed: offer.Disbursed,
			Outstanding: offer.Repayment,
		}
		if _, err := advances.Insert(ctx, tx, adv); err != nil {
			return err
		}
		if err := advances.Transition(ctx, tx, advID, 1, entity.AdvRequested, entity.AdvValidated); err != nil {
			return err
		}
		poolID, err := poolsR.Reserve(ctx, tx, offer.ProgrammeID, offer.Repayment)
		if err != nil {
			return err
		}
		if err := advances.ReserveTransition(ctx, tx, advID, 2, poolID); err != nil {
			return err
		}
		if err := offersR.SetState(ctx, tx, offer.OfferID, entity.OfferGenerated, entity.OfferAccepted); err != nil {
			return err
		}
		if err := attempts.Insert(ctx, tx, entity.FulfilmentAttempt{
			AttemptID: platform.NewID("att"), AdvanceID: advID, AttemptNo: 1,
			TelcoIdempotencyKey: platform.NewID("tik"), State: entity.AttemptSent,
			RequestEvidence: []byte(`{}`),
		}); err != nil {
			return err
		}
		return advances.Transition(ctx, tx, advID, 3, entity.AdvExposureReserved, entity.AdvPendingFulfilment)
	}); err != nil {
		t.Fatal(err)
	}
	return advID
}
