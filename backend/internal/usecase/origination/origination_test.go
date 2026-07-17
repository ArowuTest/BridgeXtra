package origination_test

// The origination saga EDG pack (BUILD_PLAN M1, gate G1):
//   EDG-001 duplicate confirm -> one advance, original outcome
//   EDG-002 concurrent confirms -> exactly one open advance (DB backstop)
//   EDG-005 timeout-after-success -> FULFILMENT_UNKNOWN, NO journal, funding
//           still reserved, enquiry scheduled
//   EDG-011 offer expired between menu and confirm -> safe rejection
//   FAIL    -> FULFILMENT_FAILED, reservation released, NO journal
//   SUCCESS -> ACTIVE, balanced journal with correlation lineage (BC-6),
//              pool utilised
// All against the REAL stack: RLS'd repos, governed config, ledger, adapter,
// simulator over HTTP.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/ledger"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/mno"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/origination"
	"github.com/ArowuTest/telco-credit-platform/simulator/sim"
)

type fixture struct {
	db     *testutil.DB
	svc    *origination.Service
	cfg    *configsvc.Service
	simURL string
}

func newFixture(t *testing.T, suffix string, simHold time.Duration, adapterTimeoutMs int) *fixture {
	t.Helper()
	db := testutil.MustSetup(t, suffix)
	simulator := sim.New(slog.Default(), "orig-test", simHold)
	srv := httptest.NewServer(simulator.Handler())
	t.Cleanup(srv.Close)

	cfg := configsvc.New(db.Worker)
	// Point the SIM_NG adapter at this test's simulator through the governed
	// lifecycle (no redeploy — the owner directive, exercised constantly).
	ctx := context.Background()
	content := fmt.Sprintf(`{"fulfilment_url":%q,"request_timeout_ms":%d,"retry_budget":0,"circuit_error_threshold_pct":50,"circuit_min_requests":20}`, srv.URL, adapterTimeoutMs)
	c, err := cfg.CreateDraft(ctx, "telco.adapter", "telco:SIM_NG", "alice", "test sim", []byte(content))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Submit(ctx, c.ConfigVersionID, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Approve(ctx, c.ConfigVersionID, "bob"); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Activate(ctx, c.ConfigVersionID, "bob", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	appCfg := configsvc.New(db.App) // reads only, app role
	svc := origination.New(db.App, appCfg, ledger.New(appCfg), mno.NewHTTPAdapter(appCfg), slog.Default())
	return &fixture{db: db, svc: svc, cfg: cfg, simURL: srv.URL}
}

// seedSubscriber adds a subscriber + current decision for a token (the 0004
// fixture only covers tok_sim_0001).
func (f *fixture) seedSubscriber(t *testing.T, subID, token string, maxFaceMinor int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.db.Admin.Exec(ctx, `
		INSERT INTO subscriber_accounts (subscriber_account_id, telco_id, msisdn_token, status)
		VALUES ($1, 'SIM_NG', $2, 'ACTIVE')`, subID, token); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(ctx, `
		INSERT INTO decision_snapshots (decision_snapshot_id, telco_id, subscriber_account_id,
		  max_face_value_minor, currency, config_version_id)
		VALUES ('dec_'||$1, 'SIM_NG', $1, $2, 'NGN', 'cfg_seed_product_airtime_v1')`,
		subID, maxFaceMinor); err != nil {
		t.Fatal(err)
	}
}

func tenantCtx() context.Context {
	return platform.WithTenant(context.Background(), "SIM_NG")
}

func (f *fixture) offersFor(t *testing.T, token string) []entity.Offer {
	t.Helper()
	offers, err := f.svc.GetOffers(tenantCtx(), "prg_sim_airtime01", token)
	if err != nil {
		t.Fatal(err)
	}
	if len(offers) == 0 {
		t.Fatal("no offers generated")
	}
	return offers
}

func (f *fixture) poolState(t *testing.T) (reserved, utilised int64) {
	t.Helper()
	if err := f.db.Admin.QueryRow(context.Background(),
		`SELECT reserved_minor, utilised_minor FROM funding_pools WHERE pool_id='pool_sim_01'`).
		Scan(&reserved, &utilised); err != nil {
		t.Fatal(err)
	}
	return
}

func TestWalkingSkeleton_SuccessPath_ActiveBalancedLedgerCorrelated(t *testing.T) {
	f := newFixture(t, "orig_success", 0, 2_000)
	offers := f.offersFor(t, "tok_sim_0001") // seeded subscriber, ₦500 cap

	// Ladder derives from config: 5000..50000 kobo, 10% upfront fee.
	first := offers[0]
	if first.FaceValue.Amount() != 5_000 || first.Fee.Amount() != 500 || first.Disbursed.Amount() != 4_500 {
		t.Fatalf("offer economics from config wrong: %+v", first)
	}

	res, err := f.svc.Confirm(tenantCtx(), origination.ConfirmCmd{
		ProgrammeID: "prg_sim_airtime01", OfferID: first.OfferID, MSISDNToken: "tok_sim_0001",
		IdemKey: "confirm-1", CorrelationID: "cor-e2e-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Advance.State != entity.AdvActive {
		t.Fatalf("want ACTIVE, got %s", res.Advance.State)
	}
	if res.Advance.Outstanding.Amount() != 5_000 {
		t.Fatalf("outstanding = %d, want 5000 (repayment obligation)", res.Advance.Outstanding.Amount())
	}

	// Pool: reservation converted to utilisation.
	reserved, utilised := f.poolState(t)
	if reserved != 0 || utilised != 5_000 {
		t.Fatalf("pool reserved=%d utilised=%d, want 0/5000", reserved, utilised)
	}

	// Ledger: balanced journal, correlation lineage (BC-6).
	var journals int
	var cor string
	if err := f.db.Admin.QueryRow(context.Background(),
		`SELECT count(*), min(correlation_id) FROM journals`).Scan(&journals, &cor); err != nil {
		t.Fatal(err)
	}
	if journals != 1 || cor != "cor-e2e-1" {
		t.Fatalf("journals=%d correlation=%q — want 1 journal carrying the request correlation", journals, cor)
	}
	var unbalanced int
	if err := f.db.Admin.QueryRow(context.Background(), `
		SELECT count(*) FROM (
			SELECT journal_id FROM journal_entries GROUP BY journal_id, currency
			HAVING SUM(debit_minor) <> SUM(credit_minor)) x`).Scan(&unbalanced); err != nil {
		t.Fatal(err)
	}
	if unbalanced != 0 {
		t.Fatal("INV-004 violated: unbalanced journal")
	}
}

func TestEDG001_DuplicateConfirm_ReplaysOriginal_OneAdvance(t *testing.T) {
	f := newFixture(t, "orig_edg001", 0, 2_000)
	offers := f.offersFor(t, "tok_sim_0001")

	cmd := origination.ConfirmCmd{
		ProgrammeID: "prg_sim_airtime01", OfferID: offers[0].OfferID, MSISDNToken: "tok_sim_0001",
		IdemKey: "dup-key", CorrelationID: "cor-dup",
	}
	r1, err := f.svc.Confirm(tenantCtx(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := f.svc.Confirm(tenantCtx(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	if !r2.Replayed || r2.Advance.AdvanceID != r1.Advance.AdvanceID {
		t.Fatalf("duplicate must replay original advance %s, got %+v", r1.Advance.AdvanceID, r2)
	}
	var advances, journals int
	if err := f.db.Admin.QueryRow(context.Background(),
		`SELECT (SELECT count(*) FROM advances), (SELECT count(*) FROM journals)`).
		Scan(&advances, &journals); err != nil {
		t.Fatal(err)
	}
	if advances != 1 || journals != 1 {
		t.Fatalf("exactly one advance and one journal, got %d/%d", advances, journals)
	}
}

func TestEDG002_ConcurrentConfirms_ExactlyOneOpenAdvance(t *testing.T) {
	f := newFixture(t, "orig_edg002", 0, 2_000)
	offers := f.offersFor(t, "tok_sim_0001")
	if len(offers) < 2 {
		t.Fatal("need at least 2 offers")
	}

	// 8 concurrent confirms across DIFFERENT offers and DIFFERENT idem keys
	// for the SAME subscriber: the one-active backstop must admit exactly one.
	const n = 8
	var wg sync.WaitGroup
	results := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := f.svc.Confirm(tenantCtx(), origination.ConfirmCmd{
				ProgrammeID:   "prg_sim_airtime01",
				OfferID:       offers[i%len(offers)].OfferID,
				MSISDNToken:   "tok_sim_0001",
				IdemKey:       fmt.Sprintf("conc-%d", i),
				CorrelationID: fmt.Sprintf("cor-conc-%d", i),
			})
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)

	successes, blocked, other := 0, 0, 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, repo.ErrConcurrentAdvanceBlocked),
			errors.Is(err, repo.ErrOfferAlreadyUsed),
			errors.Is(err, origination.ErrOfferNotAcceptable):
			blocked++
		default:
			other++
			t.Logf("unexpected error class: %v", err)
		}
	}
	if successes != 1 || other != 0 {
		t.Fatalf("want exactly 1 success and deterministic blocks, got success=%d blocked=%d other=%d",
			successes, blocked, other)
	}
	var open int
	if err := f.db.Admin.QueryRow(context.Background(),
		`SELECT count(*) FROM advances WHERE state NOT IN ('FULFILMENT_FAILED','DECLINED','CLOSED')`).
		Scan(&open); err != nil {
		t.Fatal(err)
	}
	if open != 1 {
		t.Fatalf("exactly one open advance, got %d", open)
	}
}

func TestFAIL_ReleasesReservation_NoJournal(t *testing.T) {
	f := newFixture(t, "orig_fail", 0, 2_000)
	f.seedSubscriber(t, "sub_fail_1", "tok_FAIL_20", 50_000)
	offers := f.offersFor(t, "tok_FAIL_20")

	res, err := f.svc.Confirm(tenantCtx(), origination.ConfirmCmd{
		ProgrammeID: "prg_sim_airtime01", OfferID: offers[0].OfferID, MSISDNToken: "tok_FAIL_20",
		IdemKey: "fail-1", CorrelationID: "cor-fail",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Advance.State != entity.AdvFulfilmentFailed {
		t.Fatalf("want FULFILMENT_FAILED, got %s", res.Advance.State)
	}
	reserved, utilised := f.poolState(t)
	if reserved != 0 || utilised != 0 {
		t.Fatalf("failed fulfilment must release funding: reserved=%d utilised=%d", reserved, utilised)
	}
	var journals int
	if err := f.db.Admin.QueryRow(context.Background(), `SELECT count(*) FROM journals`).Scan(&journals); err != nil {
		t.Fatal(err)
	}
	if journals != 0 {
		t.Fatal("no journal may exist for a failed fulfilment (V2-LED-006)")
	}
}

func TestEDG005_TimeoutAfterSuccess_UnknownNoJournalReservationHeld(t *testing.T) {
	f := newFixture(t, "orig_edg005", 2*time.Second, 300)
	f.seedSubscriber(t, "sub_to_1", "tok_TIMEOUT_20", 50_000)
	offers := f.offersFor(t, "tok_TIMEOUT_20")

	res, err := f.svc.Confirm(tenantCtx(), origination.ConfirmCmd{
		ProgrammeID: "prg_sim_airtime01", OfferID: offers[0].OfferID, MSISDNToken: "tok_TIMEOUT_20",
		IdemKey: "to-1", CorrelationID: "cor-to",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Advance.State != entity.AdvFulfilmentUnknown {
		t.Fatalf("want FULFILMENT_UNKNOWN, got %s", res.Advance.State)
	}
	// NO journal, reservation HELD (the credit may have happened — it did).
	var journals int
	if err := f.db.Admin.QueryRow(context.Background(), `SELECT count(*) FROM journals`).Scan(&journals); err != nil {
		t.Fatal(err)
	}
	if journals != 0 {
		t.Fatal("V2-LED-006: no journal while fulfilment is unknown")
	}
	reserved, _ := f.poolState(t)
	if reserved != 5_000 {
		t.Fatalf("reservation must be held during UNKNOWN, reserved=%d", reserved)
	}
	// Enquiry scheduled from governed config.
	var nextEnquiry *time.Time
	if err := f.db.Admin.QueryRow(context.Background(),
		`SELECT next_enquiry_at FROM fulfilment_attempts WHERE state='UNKNOWN'`).Scan(&nextEnquiry); err != nil {
		t.Fatal(err)
	}
	if nextEnquiry == nil {
		t.Fatal("UNKNOWN attempt must have a scheduled enquiry (V2-ADV-009)")
	}
}

func TestEDG011_ExpiredOffer_SafeRejection(t *testing.T) {
	f := newFixture(t, "orig_edg011", 0, 2_000)
	offers := f.offersFor(t, "tok_sim_0001")

	// Force-expire the offer (admin surgery simulating menu-to-confirm delay).
	if _, err := f.db.Admin.Exec(context.Background(),
		`UPDATE offers SET expires_at = now() - interval '1 minute' WHERE offer_id = $1`,
		offers[0].OfferID); err != nil {
		t.Fatal(err)
	}
	_, err := f.svc.Confirm(tenantCtx(), origination.ConfirmCmd{
		ProgrammeID: "prg_sim_airtime01", OfferID: offers[0].OfferID, MSISDNToken: "tok_sim_0001",
		IdemKey: "exp-1", CorrelationID: "cor-exp",
	})
	if !errors.Is(err, origination.ErrOfferExpired) {
		t.Fatalf("want ErrOfferExpired, got %v", err)
	}
	var advances int
	if err := f.db.Admin.QueryRow(context.Background(), `SELECT count(*) FROM advances`).Scan(&advances); err != nil {
		t.Fatal(err)
	}
	if advances != 0 {
		t.Fatal("expired offer must create no advance or financial effect")
	}
	reserved, _ := f.poolState(t)
	if reserved != 0 {
		t.Fatalf("expired offer must hold no reservation, reserved=%d", reserved)
	}
}

func TestVR7a_ConcurrentFirstEnquiries_SingleLadder(t *testing.T) {
	f := newFixture(t, "orig_vr7a", 0, 2_000)
	f.seedSubscriber(t, "sub_vr7a", "tok_vr7a_1", 50_000)

	const n = 6
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = f.svc.GetOffers(tenantCtx(), "prg_sim_airtime01", "tok_vr7a_1")
		}()
	}
	wg.Wait()

	// Exactly one ladder (4 config denominations, all under the ₦500 cap).
	var offers int
	if err := f.db.Admin.QueryRow(context.Background(),
		`SELECT count(*) FROM offers WHERE subscriber_account_id='sub_vr7a'`).Scan(&offers); err != nil {
		t.Fatal(err)
	}
	if offers != 4 {
		t.Fatalf("VR-7a: concurrent first enquiries must mint ONE ladder (4 offers), got %d", offers)
	}
}

func TestOfferReuse_SecondEnquiryReturnsSameLadder(t *testing.T) {
	f := newFixture(t, "orig_reuse", 0, 2_000)
	first := f.offersFor(t, "tok_sim_0001")
	second := f.offersFor(t, "tok_sim_0001")
	if len(first) != len(second) || first[0].OfferID != second[0].OfferID {
		t.Fatalf("valid offers must be reused (V2-OFR-009): %d vs %d", len(first), len(second))
	}
}
