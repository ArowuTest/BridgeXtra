package collections_test

// M3c pack: config-driven delinquency classification (set-based), the
// maker-checker write-off journey (request gate -> self-approval refused ->
// distinct approval crystallises loss + balanced journal + pool release),
// and EDG-021 through the REAL path: write-off first, then a recovery that
// books as income.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/ledger"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/mno"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/collections"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/origination"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/recovery"
	"github.com/ArowuTest/telco-credit-platform/simulator/sim"
)

type fixture struct {
	db   *testutil.DB
	orig *origination.Service
	rec  *recovery.Service
	col  *collections.Service
}

func tenantCtx() context.Context { return platform.WithTenant(context.Background(), "SIM_NG") }

func newFixture(t *testing.T, suffix string) *fixture {
	t.Helper()
	db := testutil.MustSetup(t, suffix)
	simulator := sim.New(slog.Default(), "col-test", 0)
	srv := httptest.NewServer(simulator.Handler())
	t.Cleanup(srv.Close)

	cfgW := configsvc.New(db.Worker)
	ctx := context.Background()
	content := fmt.Sprintf(`{"fulfilment_url":%q,"request_timeout_ms":2000,"retry_budget":0,"circuit_error_threshold_pct":50,"circuit_min_requests":20,"circuit_cooldown_seconds":30,"max_weekly_recharge_minor":100000000}`, srv.URL)
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
	led := ledger.New(appCfg)
	return &fixture{
		db:   db,
		orig: origination.New(db.App, appCfg, led, mno.NewHTTPAdapter(appCfg), slog.Default()),
		rec:  recovery.New(db.App, appCfg, led, slog.Default()),
		col:  collections.New(db.App, appCfg, led, slog.Default()),
	}
}

func (f *fixture) activeAdvance(t *testing.T) entity.Advance {
	t.Helper()
	offers, err := f.orig.GetOffers(tenantCtx(), "prg_sim_airtime01", "tok_sim_0001")
	if err != nil {
		t.Fatal(err)
	}
	res, err := f.orig.Confirm(tenantCtx(), origination.ConfirmCmd{
		ProgrammeID: "prg_sim_airtime01", OfferID: offers[0].Offer.OfferID, MSISDNToken: "tok_sim_0001",
		IdemKey: "col-adv-1", CorrelationID: "cor-col-adv",
		DisclosureRef: offers[0].Disclosure.DisclosureSnapshotID,
		Channel:       "USSD", SessionID: "sess-col", AcceptedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return res.Advance
}

// backdate makes the advance N days old (classification input).
func (f *fixture) backdate(t *testing.T, advanceID string, days int) {
	t.Helper()
	if _, err := f.db.Admin.Exec(context.Background(),
		fmt.Sprintf(`UPDATE advances SET activated_at = now() - interval '%d days' WHERE advance_id = $1`, days),
		advanceID); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) bucketOf(t *testing.T, advanceID string) string {
	t.Helper()
	var b string
	if err := f.db.Admin.QueryRow(context.Background(),
		`SELECT COALESCE(delinquency_bucket,'') FROM advances WHERE advance_id = $1`, advanceID).Scan(&b); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestM3C_Classification_LadderFromConfig(t *testing.T) {
	f := newFixture(t, "col_classify")
	adv := f.activeAdvance(t)

	// Fresh advance: CURRENT.
	if _, err := f.col.Classify(tenantCtx(), "SIM_NG", "prg_sim_airtime01"); err != nil {
		t.Fatal(err)
	}
	if b := f.bucketOf(t, adv.AdvanceID); b != "CURRENT" {
		t.Fatalf("fresh advance must be CURRENT, got %q", b)
	}

	// Age it through the ladder: 10 days -> DPD_8_30; 95 days -> DPD_90_PLUS.
	f.backdate(t, adv.AdvanceID, 10)
	changed, err := f.col.Classify(tenantCtx(), "SIM_NG", "prg_sim_airtime01")
	if err != nil {
		t.Fatal(err)
	}
	if changed != 1 || f.bucketOf(t, adv.AdvanceID) != "DPD_8_30" {
		t.Fatalf("10-day advance must move to DPD_8_30 (changed=%d bucket=%s)", changed, f.bucketOf(t, adv.AdvanceID))
	}

	f.backdate(t, adv.AdvanceID, 95)
	if _, err := f.col.Classify(tenantCtx(), "SIM_NG", "prg_sim_airtime01"); err != nil {
		t.Fatal(err)
	}
	if b := f.bucketOf(t, adv.AdvanceID); b != "DPD_90_PLUS" {
		t.Fatalf("95-day advance must be DPD_90_PLUS, got %q", b)
	}

	// Re-run with no age change: zero rows touched (stable, idempotent).
	changed, err = f.col.Classify(tenantCtx(), "SIM_NG", "prg_sim_airtime01")
	if err != nil {
		t.Fatal(err)
	}
	if changed != 0 {
		t.Fatalf("stable re-classification must touch nothing, changed=%d", changed)
	}
}

func TestM3C_WriteOff_FullMakerCheckerJourney(t *testing.T) {
	f := newFixture(t, "col_writeoff")
	adv := f.activeAdvance(t)
	ctx := context.Background()

	// Below the policy minimum: request refused.
	f.backdate(t, adv.AdvanceID, 10)
	if _, err := f.col.Classify(tenantCtx(), "SIM_NG", "prg_sim_airtime01"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.col.RequestWriteOff(tenantCtx(), "SIM_NG", adv.AdvanceID, "alice", "uncollectable"); !errors.Is(err, collections.ErrNotEligible) {
		t.Fatalf("bucket below minimum must refuse (G3 gate): %v", err)
	}

	// Age past the minimum, request opens.
	f.backdate(t, adv.AdvanceID, 100)
	if _, err := f.col.Classify(tenantCtx(), "SIM_NG", "prg_sim_airtime01"); err != nil {
		t.Fatal(err)
	}
	wo, err := f.col.RequestWriteOff(tenantCtx(), "SIM_NG", adv.AdvanceID, "alice", "uncollectable 100d")
	if err != nil {
		t.Fatal(err)
	}
	if wo.Fee.Amount() != 1_000 || wo.Principal.Amount() != 9_000 {
		t.Fatalf("split must itemise the obligation: fee=%d principal=%d", wo.Fee.Amount(), wo.Principal.Amount())
	}
	// Duplicate request refused (schema-arbitered).
	if _, err := f.col.RequestWriteOff(tenantCtx(), "SIM_NG", adv.AdvanceID, "alice", "again"); !errors.Is(err, collections.ErrAlreadyExists) {
		t.Fatalf("second request must refuse: %v", err)
	}

	// Maker cannot approve their own request — the SCHEMA says no.
	if err := f.col.ApproveWriteOff(tenantCtx(), "SIM_NG", wo.WriteOffID, "alice", "cor-wo-1", false); !errors.Is(err, collections.ErrSelfApproval) {
		t.Fatalf("self-approval must be refused by the schema: %v", err)
	}

	// Distinct approver: the loss crystallises atomically.
	if err := f.col.ApproveWriteOff(tenantCtx(), "SIM_NG", wo.WriteOffID, "bob", "cor-wo-1", false); err != nil {
		t.Fatal(err)
	}

	var state string
	var outstanding, utilised int64
	if err := f.db.Admin.QueryRow(ctx, `
		SELECT a.state, a.outstanding_minor, p.utilised_minor
		FROM advances a JOIN funding_pools p ON p.pool_id = a.funding_pool_id
		WHERE a.advance_id = $1`, adv.AdvanceID).Scan(&state, &outstanding, &utilised); err != nil {
		t.Fatal(err)
	}
	if state != "WRITTEN_OFF" || outstanding != 0 || utilised != 0 {
		t.Fatalf("crystallised loss: state=%s outstanding=%d utilised=%d", state, outstanding, utilised)
	}
	var expense int64
	if err := f.db.Admin.QueryRow(ctx, `
		SELECT COALESCE(SUM(debit_minor),0) FROM journal_entries WHERE account_code='WRITE_OFF_EXPENSE'`).Scan(&expense); err != nil {
		t.Fatal(err)
	}
	if expense != 10_000 {
		t.Fatalf("loss must be recognised in the books: %d", expense)
	}
	var woState string
	if err := f.db.Admin.QueryRow(ctx,
		`SELECT state FROM write_offs WHERE write_off_id=$1`, wo.WriteOffID).Scan(&woState); err != nil {
		t.Fatal(err)
	}
	if woState != "POSTED" {
		t.Fatalf("evidence must be POSTED, got %s", woState)
	}

	// Self-audit (0021): a POSTED write-off is a frozen audit record — even the
	// table owner cannot rewrite its amount or its maker-checker approver.
	if _, err := f.db.Admin.Exec(ctx,
		`UPDATE write_offs SET principal_minor=0 WHERE write_off_id=$1`, wo.WriteOffID); err == nil ||
		!strings.Contains(err.Error(), "immutable") {
		t.Fatalf("POSTED write-off amount must be immutable, got %v", err)
	}
	if _, err := f.db.Admin.Exec(ctx,
		`UPDATE write_offs SET approved_by='mallory' WHERE write_off_id=$1`, wo.WriteOffID); err == nil ||
		!strings.Contains(err.Error(), "immutable") {
		t.Fatalf("POSTED write-off approver must be immutable, got %v", err)
	}

	// EDG-021 through the REAL path: a later recovery books as income.
	res, err := f.rec.Ingest(tenantCtx(), recovery.IngestCmd{
		SourceEventID: "src-after-wo", MSISDNToken: "tok_sim_0001",
		Amount: entity.MustMoney(2_000, entity.NGN), OccurredAt: time.Now().UTC(),
		CorrelationID: "cor-after-wo",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.State != entity.RecoveryAllocated || res.Applied.Amount() != 2_000 {
		t.Fatalf("post-write-off recovery must book as income: %+v", res)
	}
	var income int64
	if err := f.db.Admin.QueryRow(ctx, `
		SELECT COALESCE(SUM(credit_minor),0) FROM journal_entries WHERE account_code='WRITEOFF_RECOVERY_INCOME'`).Scan(&income); err != nil {
		t.Fatal(err)
	}
	if income != 2_000 {
		t.Fatalf("income must be recognised: %d", income)
	}

	// The whole journey leaves balanced books.
	var unbalanced int
	if err := f.db.Admin.QueryRow(ctx, `
		SELECT count(*) FROM (
			SELECT journal_id FROM journal_entries GROUP BY journal_id, currency
			HAVING SUM(debit_minor) <> SUM(credit_minor)) x`).Scan(&unbalanced); err != nil {
		t.Fatal(err)
	}
	if unbalanced != 0 {
		t.Fatal("INV-004 violated across the write-off journey")
	}
}

// Defense-in-depth dispute gate (review F5a): the usecase itself refuses to crystallise a loss on
// a subscriber with an OPEN debt dispute unless the caller authorized an override — the
// un-bypassable backstop for any future DIRECT caller that skips the portal's governed gate.
// Mutation canary: remove the `if !overrideAuthorized` block in ApproveWriteOff (or make
// debtDisputeCategories return nil) and the un-overridden approve succeeds → this goes red.
func TestM3C_WriteOff_DisputeGate_DefenseInDepth(t *testing.T) {
	f := newFixture(t, "col_wo_dispute")
	adv := f.activeAdvance(t)
	ctx := context.Background()

	// Make it write-off-eligible (DPD_90_PLUS).
	f.backdate(t, adv.AdvanceID, 95)
	if _, err := f.col.Classify(tenantCtx(), "SIM_NG", "prg_sim_airtime01"); err != nil {
		t.Fatal(err)
	}
	// Open a DEBT dispute (a governed collections.policy category, seeded by 0070) on the subscriber.
	var sub string
	if err := f.db.Admin.QueryRow(ctx, `SELECT subscriber_account_id FROM advances WHERE advance_id=$1`, adv.AdvanceID).Scan(&sub); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(ctx, `
		INSERT INTO complaints (complaint_id, telco_id, subscriber_account_id, channel, category, narrative, state)
		VALUES ('cmp_dd','SIM_NG',$1,'IVR','DISPUTED_ADVANCE','disputes the advance','OPEN')`, sub); err != nil {
		t.Fatal(err)
	}

	wo, err := f.col.RequestWriteOff(tenantCtx(), "SIM_NG", adv.AdvanceID, "alice", "uncollectable")
	if err != nil {
		t.Fatal(err)
	}

	// A DIRECT approve with a DISTINCT actor (bob != alice, so four-eyes is satisfied) but NO
	// authorized override must be blocked — the disputed loss must not crystallise.
	if err := f.col.ApproveWriteOff(ctx, "SIM_NG", wo.WriteOffID, "bob", "cor-dd-1", false); !errors.Is(err, collections.ErrDisputeBlocked) {
		t.Fatalf("un-overridden approve on a disputed advance must be ErrDisputeBlocked, got %v", err)
	}
	// Nothing crystallised — the Decide rolled back, advance untouched, write-off still REQUESTED.
	var state, woState string
	if err := f.db.Admin.QueryRow(ctx,
		`SELECT a.state, w.state FROM advances a JOIN write_offs w ON w.advance_id = a.advance_id WHERE a.advance_id = $1`,
		adv.AdvanceID).Scan(&state, &woState); err != nil {
		t.Fatal(err)
	}
	if state == "WRITTEN_OFF" || woState != "REQUESTED" {
		t.Fatalf("a blocked approve must not mutate: advance=%s write_off=%s", state, woState)
	}

	// With the override authorized, the same distinct-actor approve crystallises the loss.
	if err := f.col.ApproveWriteOff(ctx, "SIM_NG", wo.WriteOffID, "bob", "cor-dd-2", true); err != nil {
		t.Fatalf("override-authorized approve must succeed: %v", err)
	}
	if err := f.db.Admin.QueryRow(ctx, `SELECT state FROM advances WHERE advance_id=$1`, adv.AdvanceID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "WRITTEN_OFF" {
		t.Fatalf("override-authorized approve must crystallise; advance state=%s", state)
	}
}

// Re-request after rejection (owner-approved money-model change, migration 0073). A rejection is a
// "not now", not a permanent bar. Proves the three guarantees at the INDEX level, directly on the
// partial unique index write_offs_one_live_per_advance: (a) at most one LIVE write-off per advance,
// (b) a REJECTED one no longer blocks a re-request, (c) a POSTED one STILL bars one. Mutation
// canary: drop the index's `WHERE state <> 'REJECTED'` predicate → (b) collides → red.
func TestM3C_WriteOff_PartialUniqueIndex(t *testing.T) {
	f := newFixture(t, "col_wo_partialidx")
	adv := f.activeAdvance(t)
	ctx := context.Background()

	ins := func(id string) error {
		_, err := f.db.Admin.Exec(ctx, `
			INSERT INTO write_offs (write_off_id, telco_id, advance_id, principal_minor, fee_minor, currency, reason, requested_by)
			VALUES ($1,'SIM_NG',$2,900,100,'NGN','t','maker')`, id, adv.AdvanceID)
		return err
	}
	// decide moves a write-off's state with a DISTINCT approver (satisfies the 0011/0072 four-eyes
	// CHECKs); OLD.state=REQUESTED so the 0021 immutability trigger allows it.
	decide := func(id, toState string) {
		if _, err := f.db.Admin.Exec(ctx,
			`UPDATE write_offs SET state=$2, approved_by='checker', decided_at=now() WHERE write_off_id=$1`, id, toState); err != nil {
			t.Fatalf("decide %s -> %s: %v", id, toState, err)
		}
	}
	liveConflict := func(err error) bool {
		return err != nil && strings.Contains(err.Error(), "write_offs_one_live_per_advance")
	}

	// (a) One LIVE write-off per advance — the second collides on the partial index.
	if err := ins("wof_idx_1"); err != nil {
		t.Fatalf("first write-off must insert: %v", err)
	}
	if err := ins("wof_idx_2"); !liveConflict(err) {
		t.Fatalf("a second LIVE write-off must collide on write_offs_one_live_per_advance, got %v", err)
	}
	// (b) After a REJECTION the slot frees — a re-request inserts (REJECTED is excluded from the index).
	decide("wof_idx_1", "REJECTED")
	if err := ins("wof_idx_3"); err != nil {
		t.Fatalf("re-request after a REJECTED write-off must insert, got %v", err)
	}
	// (c) A POSTED write-off is non-REJECTED, so it STILL bars a re-request via the index.
	decide("wof_idx_3", "POSTED")
	if err := ins("wof_idx_4"); !liveConflict(err) {
		t.Fatalf("re-request after a POSTED write-off must collide (POSTED is non-REJECTED), got %v", err)
	}
}

// The same guarantee through the REAL flow (RequestWriteOff → RejectWriteOff → RequestWriteOff):
// a rejected advance can be written off later. (c) here is blocked by the FSM (the advance is
// WRITTEN_OFF after POSTED) — the index test above proves the index-level POSTED bar.
func TestM3C_WriteOff_ReRequestAfterRejection(t *testing.T) {
	f := newFixture(t, "col_wo_rerequest")
	adv := f.activeAdvance(t)
	ctx := context.Background()
	f.backdate(t, adv.AdvanceID, 95)
	if _, err := f.col.Classify(tenantCtx(), "SIM_NG", "prg_sim_airtime01"); err != nil {
		t.Fatal(err)
	}

	// (a) A second live request while the first is REQUESTED is refused.
	wo1, err := f.col.RequestWriteOff(tenantCtx(), "SIM_NG", adv.AdvanceID, "alice", "uncollectable")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.col.RequestWriteOff(tenantCtx(), "SIM_NG", adv.AdvanceID, "alice", "again"); !errors.Is(err, collections.ErrAlreadyExists) {
		t.Fatalf("a second live request must be ErrWriteOffExists, got %v", err)
	}

	// Reject the first (distinct actor).
	if err := f.col.RejectWriteOff(tenantCtx(), "SIM_NG", wo1.WriteOffID, "bob", "advance recovering, keep it live"); err != nil {
		t.Fatal(err)
	}

	// (b) After the rejection, the advance can be re-requested — a NEW write-off, not the rejected one.
	wo2, err := f.col.RequestWriteOff(tenantCtx(), "SIM_NG", adv.AdvanceID, "alice", "still uncollectable, months later")
	if err != nil {
		t.Fatalf("re-request after a REJECTED write-off must succeed, got %v", err)
	}
	if wo2.WriteOffID == wo1.WriteOffID {
		t.Fatalf("the re-request must be a NEW write-off, not the rejected one")
	}

	// (c) After it is approved (POSTED → advance WRITTEN_OFF) a further re-request is blocked.
	if err := f.col.ApproveWriteOff(ctx, "SIM_NG", wo2.WriteOffID, "bob", "cor-rr", false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.col.RequestWriteOff(tenantCtx(), "SIM_NG", adv.AdvanceID, "alice", "after posted"); !errors.Is(err, collections.ErrNotWritable) {
		t.Fatalf("re-request after POSTED must be blocked (advance WRITTEN_OFF), got %v", err)
	}
}
