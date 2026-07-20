package recovery_test

// M1b-4 recovery matrix (V2-COL-001..008): partial -> PARTIALLY_RECOVERED,
// exact -> CLOSED, over-recovery -> applied+suspense (EDG-020), duplicate
// source event -> replay untouched (EDG-018), unmatched token -> preserved
// (V2-REP-004). Every path checks the ledger and pool arithmetic.

import (
	"context"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/ledger"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/mno"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/origination"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/recovery"
	"github.com/ArowuTest/telco-credit-platform/simulator/sim"
)

type fixture struct {
	db   *testutil.DB
	orig *origination.Service
	rec  *recovery.Service
}

func tenantCtx() context.Context { return platform.WithTenant(context.Background(), "SIM_NG") }

func newFixture(t *testing.T, suffix string) *fixture {
	t.Helper()
	db := testutil.MustSetup(t, suffix)
	simulator := sim.New(slog.Default(), "rec-test", 0)
	srv := httptest.NewServer(simulator.Handler())
	t.Cleanup(srv.Close)

	cfgW := configsvc.New(db.Worker)
	ctx := context.Background()
	content := fmt.Sprintf(`{"fulfilment_url":%q,"request_timeout_ms":2000,"retry_budget":0,"circuit_error_threshold_pct":50,"circuit_min_requests":20,"max_weekly_recharge_minor":100000000}`, srv.URL)
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
	orig := origination.New(db.App, appCfg, led, mno.NewHTTPAdapter(appCfg), slog.Default())
	rec := recovery.New(db.App, appCfg, led, slog.Default())
	return &fixture{db: db, orig: orig, rec: rec}
}

// activeAdvance originates a ₦50 advance for the seeded subscriber and
// returns it ACTIVE (outstanding 5000 kobo: fee 500 + principal 4500).
func (f *fixture) activeAdvance(t *testing.T) entity.Advance {
	t.Helper()
	offers, err := f.orig.GetOffers(tenantCtx(), "prg_sim_airtime01", "tok_sim_0001")
	if err != nil {
		t.Fatal(err)
	}
	res, err := f.orig.Confirm(tenantCtx(), origination.ConfirmCmd{
		ProgrammeID: "prg_sim_airtime01", OfferID: offers[0].OfferID, MSISDNToken: "tok_sim_0001",
		IdemKey: "rec-adv-1", CorrelationID: "cor-rec-adv",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Advance.State != entity.AdvActive {
		t.Fatalf("fixture advance not ACTIVE: %s", res.Advance.State)
	}
	return res.Advance
}

// A stable event timestamp: a real telco event carries its own occurred-at,
// and a REPLAY of that event carries the SAME one (R-P0-2 hashes it, so a
// wall-clock time.Now() here would make every "replay" look divergent).
var fixedOccurredAt = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

func (f *fixture) ingest(t *testing.T, sourceID string, minor int64) recovery.IngestResult {
	t.Helper()
	out, err := f.rec.Ingest(tenantCtx(), recovery.IngestCmd{
		SourceEventID: sourceID, MSISDNToken: "tok_sim_0001",
		Amount: entity.MustMoney(minor, entity.NGN), OccurredAt: fixedOccurredAt,
		CorrelationID: "cor-" + sourceID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRecovery_PartialThenClose_FeeFirstWaterfall(t *testing.T) {
	f := newFixture(t, "rec_partial")
	adv := f.activeAdvance(t)

	// Partial ₦3 (300 kobo... wait: 2000 kobo = ₦20): fee 500 first, rest principal.
	r1 := f.ingest(t, "src-1", 2_000)
	if r1.State != entity.RecoveryAllocated || r1.Applied.Amount() != 2_000 || r1.AdvanceClosed {
		t.Fatalf("partial: %+v", r1)
	}
	// Waterfall: FEE 500 then PRINCIPAL 1500.
	var feeSum, prinSum int64
	if err := f.db.Admin.QueryRow(context.Background(), `
		SELECT COALESCE(SUM(amount_minor) FILTER (WHERE component='FEE'),0),
		       COALESCE(SUM(amount_minor) FILTER (WHERE component='PRINCIPAL'),0)
		FROM recovery_allocations`).Scan(&feeSum, &prinSum); err != nil {
		t.Fatal(err)
	}
	if feeSum != 500 || prinSum != 1_500 {
		t.Fatalf("fee-first waterfall violated: fee=%d principal=%d", feeSum, prinSum)
	}
	// Advance state + outstanding.
	var state string
	var outstanding int64
	if err := f.db.Admin.QueryRow(context.Background(),
		`SELECT state, outstanding_minor FROM advances WHERE advance_id=$1`, adv.AdvanceID).
		Scan(&state, &outstanding); err != nil {
		t.Fatal(err)
	}
	if state != "PARTIALLY_RECOVERED" || outstanding != 3_000 {
		t.Fatalf("after partial: state=%s outstanding=%d", state, outstanding)
	}

	// Close with the exact remainder.
	r2 := f.ingest(t, "src-2", 3_000)
	if !r2.AdvanceClosed || r2.Applied.Amount() != 3_000 {
		t.Fatalf("close: %+v", r2)
	}
	// Pool fully released; receivable rebuilt to zero; all journals balanced.
	var reserved, utilised int64
	if err := f.db.Admin.QueryRow(context.Background(),
		`SELECT reserved_minor, utilised_minor FROM funding_pools WHERE pool_id='pool_sim_01'`).
		Scan(&reserved, &utilised); err != nil {
		t.Fatal(err)
	}
	if reserved != 0 || utilised != 0 {
		t.Fatalf("pool after close: reserved=%d utilised=%d", reserved, utilised)
	}
	var receivable int64
	if err := f.db.Admin.QueryRow(context.Background(), `
		SELECT COALESCE(SUM(debit_minor - credit_minor),0) FROM journal_entries
		WHERE account_code='SUBSCRIBER_RECEIVABLE'`).Scan(&receivable); err != nil {
		t.Fatal(err)
	}
	if receivable != 0 {
		t.Fatalf("SUBSCRIBER_RECEIVABLE must rebuild to 0 after full recovery, got %d", receivable)
	}
}

func TestEDG020_OverRecovery_AppliedPlusSuspense(t *testing.T) {
	f := newFixture(t, "rec_over")
	f.activeAdvance(t)

	// ₦70 against ₦50 outstanding: 5000 applied, 2000 suspense.
	r := f.ingest(t, "src-over", 7_000)
	if !r.AdvanceClosed || r.Applied.Amount() != 5_000 || r.Excess.Amount() != 2_000 {
		t.Fatalf("over-recovery split wrong: %+v", r)
	}
	var suspense int64
	if err := f.db.Admin.QueryRow(context.Background(),
		`SELECT COALESCE(SUM(amount_minor),0) FROM suspense_items WHERE state='OPEN'`).Scan(&suspense); err != nil {
		t.Fatal(err)
	}
	if suspense != 2_000 {
		t.Fatalf("suspense must hold the excess: %d", suspense)
	}
	// Suspense liability on the books, balanced.
	var suspenseBal int64
	if err := f.db.Admin.QueryRow(context.Background(), `
		SELECT COALESCE(SUM(credit_minor - debit_minor),0) FROM journal_entries
		WHERE account_code='RECOVERY_SUSPENSE'`).Scan(&suspenseBal); err != nil {
		t.Fatal(err)
	}
	if suspenseBal != 2_000 {
		t.Fatalf("RECOVERY_SUSPENSE liability must be 2000, got %d", suspenseBal)
	}
}

func TestEDG018_DuplicateSourceEvent_ReplaysUntouched(t *testing.T) {
	f := newFixture(t, "rec_dup")
	f.activeAdvance(t)

	r1 := f.ingest(t, "src-dup", 2_000)
	r2 := f.ingest(t, "src-dup", 2_000) // telco replay
	if !r2.Replayed || r2.RecoveryEventID != r1.RecoveryEventID {
		t.Fatalf("duplicate must replay original: %+v", r2)
	}
	var outstanding int64
	if err := f.db.Admin.QueryRow(context.Background(),
		`SELECT outstanding_minor FROM advances`).Scan(&outstanding); err != nil {
		t.Fatal(err)
	}
	if outstanding != 3_000 {
		t.Fatalf("duplicate must not double-recover: outstanding=%d want 3000", outstanding)
	}
	var allocations int
	if err := f.db.Admin.QueryRow(context.Background(),
		`SELECT count(*) FROM recovery_allocations`).Scan(&allocations); err != nil {
		t.Fatal(err)
	}
	if allocations != 2 { // FEE + PRINCIPAL from the first event only
		t.Fatalf("allocations=%d, want 2 (first event only)", allocations)
	}
}

func TestUnmatchedToken_PreservedNeverDiscarded(t *testing.T) {
	f := newFixture(t, "rec_unmatched")
	out, err := f.rec.Ingest(tenantCtx(), recovery.IngestCmd{
		SourceEventID: "src-ghost", MSISDNToken: "tok_never_seen",
		Amount: entity.MustMoney(1_000, entity.NGN), OccurredAt: time.Now().UTC(),
		CorrelationID: "cor-ghost",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.State != entity.RecoveryUnmatched {
		t.Fatalf("unknown token must be UNMATCHED, got %s", out.State)
	}
	var n int
	if err := f.db.Admin.QueryRow(context.Background(),
		`SELECT count(*) FROM recovery_events WHERE state='UNMATCHED'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("unmatched event must be preserved (V2-REP-004)")
	}
}

func TestNoOpenAdvance_QuarantinedWithSuspense(t *testing.T) {
	f := newFixture(t, "rec_noadvance")
	// Subscriber exists (seeded) but has no advance at all.
	out := f.ingest(t, "src-noadv", 1_500)
	if out.State != entity.RecoveryQuarantined || out.Excess.Amount() != 1_500 {
		t.Fatalf("no-open-advance must quarantine in full: %+v", out)
	}
	var suspense int64
	if err := f.db.Admin.QueryRow(context.Background(),
		`SELECT COALESCE(SUM(amount_minor),0) FROM suspense_items WHERE reason='NO_OPEN_ADVANCE'`).
		Scan(&suspense); err != nil {
		t.Fatal(err)
	}
	if suspense != 1_500 {
		t.Fatalf("suspense=%d want 1500", suspense)
	}
}
