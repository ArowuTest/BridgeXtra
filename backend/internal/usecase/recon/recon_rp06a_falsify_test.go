package recon

// R-P0-6 Slice A falsification pack (reviewer checklist). Recon is a
// financial-integrity control, so the run-header/manifest/supersession
// foundation must survive these specific attacks: a duplicate that a set-hash
// would collapse, a control-total sum that overflows int64, an empty/truncated
// rerun that would wipe a good run, and concurrent reruns that must never leave
// zero or two live runs.

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
)

// newMutableReconFixture serves a telco feed that the test can change between
// runs, so successive RunFulfilment calls see different source sets on one DB.
func newMutableReconFixture(t *testing.T, suffix string) (*reconFixture, *[]telcoTransaction) {
	t.Helper()
	feed := &[]telcoTransaction{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(*feed)
	}))
	t.Cleanup(srv.Close)
	db := testutil.MustSetup(t, suffix)
	return newReconFixtureURL(t, db, srv.URL), feed
}

func (f *reconFixture) activeRun(t *testing.T) (runID string, srcCount int64) {
	t.Helper()
	if err := f.db.Admin.QueryRow(context.Background(),
		`SELECT run_id, source_record_count FROM recon_runs WHERE state='ACTIVE'`).Scan(&runID, &srcCount); err != nil {
		t.Fatalf("expected exactly one ACTIVE run: %v", err)
	}
	return
}

func (f *reconFixture) runState(t *testing.T, runID string) string {
	t.Helper()
	var st string
	if err := f.db.Admin.QueryRow(context.Background(),
		`SELECT state FROM recon_runs WHERE run_id=$1`, runID).Scan(&st); err != nil {
		t.Fatal(err)
	}
	return st
}

// #1: a duplicated source record is caught by the record COUNT even though a
// naive set-hash could collapse it; and altering only the amount (IDs held
// constant) still changes the multiset hash.
func TestRP06A_DuplicateCaughtByCount(t *testing.T) {
	base := []telcoTransaction{matchTxn("x", 100), matchTxn("y", 200)}
	dup := []telcoTransaction{matchTxn("x", 100), matchTxn("x", 100), matchTxn("y", 200)}
	c0, _, h0, err0 := sourceManifest(base, 1_000_000)
	c1, _, h1, err1 := sourceManifest(dup, 1_000_000)
	if err0 != nil || err1 != nil {
		t.Fatalf("unexpected error: %v %v", err0, err1)
	}
	if c0 != 2 || c1 != 3 {
		t.Fatalf("count must catch the duplicate: %d vs %d", c0, c1)
	}
	if h0 == h1 {
		t.Fatal("a duplicated record must change the multiset hash")
	}
}

// #2: the monetary control total must not overflow int64 — a feed engineered to
// overflow the SUM is refused, never wrapped to a bogus negative total.
func TestRP06A_ControlTotalOverflowRefused(t *testing.T) {
	big := int64(math.MaxInt64/2 + 1)
	recs := []telcoTransaction{
		{PlatformRequestID: "a", FaceValueMinor: big, Currency: "NGN", Status: "SUCCESS"},
		{PlatformRequestID: "b", FaceValueMinor: big, Currency: "NGN", Status: "SUCCESS"},
	}
	// Ceiling high enough that each value is individually credible, so only the
	// SUM overflows — the exact case per-record checks miss.
	if _, _, _, err := sourceManifest(recs, math.MaxInt64); err == nil {
		t.Fatal("a control-total sum overflow must be refused, not wrapped negative")
	}
}

// #3: an empty or truncated rerun must NOT supersede a good run — a failed fetch
// cannot wipe reconciliation state.
func TestRP06A_EmptyOrPartialRerunDoesNotSupersede(t *testing.T) {
	f, feed := newMutableReconFixture(t, "rp06a_complete")
	// Seed four advances so the first run has a populated source (count 4).
	for _, id := range []string{"a1", "a2", "a3", "a4"} {
		f.seedConfirmedAdvance(t, id, 5_000, "NGN", "TR-"+id)
	}
	*feed = []telcoTransaction{matchTxn("a1", 5_000), matchTxn("a2", 5_000), matchTxn("a3", 5_000), matchTxn("a4", 5_000)}
	ctx := context.Background()
	if _, err := f.svc.ReconcilePeriod(ctx, "SIM_NG", "prg_sim_airtime01", winStart, winEnd); err != nil {
		t.Fatal(err)
	}
	good, goodCount := f.activeRun(t)
	if goodCount != 4 {
		t.Fatalf("first run source count = %d, want 4", goodCount)
	}

	// Empty rerun → REJECTED, the good run stays ACTIVE.
	*feed = []telcoTransaction{}
	sumEmpty, err := f.svc.ReconcilePeriod(ctx, "SIM_NG", "prg_sim_airtime01", winStart, winEnd)
	if err != nil {
		t.Fatal(err)
	}
	if !sumEmpty.Rejected || f.runState(t, sumEmpty.RunID) != "REJECTED" {
		t.Fatalf("empty rerun must be REJECTED, got rejected=%v state=%s", sumEmpty.Rejected, f.runState(t, sumEmpty.RunID))
	}
	if stillActive, _ := f.activeRun(t); stillActive != good {
		t.Fatalf("the good run must stay ACTIVE after an empty rerun: active=%s want=%s", stillActive, good)
	}

	// Truncated rerun (1 of 4 = 25% < 50% floor) → REJECTED, good run stays.
	*feed = []telcoTransaction{matchTxn("a1", 5_000)}
	sumPartial, err := f.svc.ReconcilePeriod(ctx, "SIM_NG", "prg_sim_airtime01", winStart, winEnd)
	if err != nil {
		t.Fatal(err)
	}
	if !sumPartial.Rejected {
		t.Fatal("a rerun below the completeness floor must be REJECTED")
	}
	if stillActive, _ := f.activeRun(t); stillActive != good {
		t.Fatalf("the good run must stay ACTIVE after a truncated rerun: active=%s", stillActive)
	}

	// A complete rerun (all 4) supersedes normally.
	*feed = []telcoTransaction{matchTxn("a1", 5_000), matchTxn("a2", 5_000), matchTxn("a3", 5_000), matchTxn("a4", 5_000)}
	sumFull, err := f.svc.ReconcilePeriod(ctx, "SIM_NG", "prg_sim_airtime01", winStart, winEnd)
	if err != nil {
		t.Fatal(err)
	}
	if sumFull.Rejected {
		t.Fatal("a complete rerun must supersede, not be rejected")
	}
	if f.runState(t, good) != "SUPERSEDED" {
		t.Fatal("the prior run must be SUPERSEDED by a complete rerun")
	}
	if active, _ := f.activeRun(t); active != sumFull.RunID {
		t.Fatalf("the complete rerun must be ACTIVE: active=%s want=%s", active, sumFull.RunID)
	}
}

// #4: concurrent re-reconciles of the SAME period — the DB must leave exactly
// ONE ACTIVE run for that period; the losers fail cleanly, never a second
// ACTIVE. (The one-active guarantee is per-period under Slice C: concurrent
// runs of the same period_start contend on recon_runs_active_uq. Production
// recon is single-writer, so this exercises the DB guard directly.)
func TestRP06A_ConcurrentSamePeriod_ExactlyOneActive(t *testing.T) {
	f := newReconFixture(t, "rp06a_conc", []telcoTransaction{matchTxn("adv_ok", 5_000)})
	f.seedConfirmedAdvance(t, "adv_ok", 5_000, "NGN", "TR-adv_ok")
	ctx := context.Background()

	const n = 4
	var wg sync.WaitGroup
	errs := make([]error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			_, errs[i] = f.svc.ReconcilePeriod(ctx, "SIM_NG", "prg_sim_airtime01", winStart, winEnd)
		}(i)
	}
	wg.Wait()

	success := 0
	for _, e := range errs {
		if e == nil {
			success++
		} else if !strings.Contains(e.Error(), "recon_runs_active_uq") && !strings.Contains(e.Error(), "duplicate key") {
			t.Fatalf("a losing concurrent re-reconcile must fail on the active-run uniqueness, got: %v", e)
		}
	}
	if success < 1 {
		t.Fatal("at least one concurrent re-reconcile must succeed")
	}
	// Exactly one ACTIVE run for this period.
	var active int
	if err := f.db.Admin.QueryRow(ctx,
		`SELECT count(*) FROM recon_runs WHERE state='ACTIVE' AND period_start=$1`, winStart).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 1 {
		t.Fatalf("exactly one ACTIVE run for the period must exist after a concurrent storm, got %d", active)
	}
}

// #6: supersession is non-destructive — a superseded run's items (and any
// operator break-resolution attached to them) survive, so a rerun never orphans
// or silently discards break-resolution history.
func TestRP06A_SupersessionPreservesPriorItems(t *testing.T) {
	f, feed := newMutableReconFixture(t, "rp06a_preserve")
	f.seedConfirmedAdvance(t, "adv_b", 5_000, "NGN", "TR-adv_b")
	ctx := context.Background()

	// First run: empty feed → the platform advance is a BREAK_MISSING_TELCO item
	// under run1 (first run has no prior, so an empty feed is allowed).
	*feed = []telcoTransaction{}
	sum1, err := f.svc.ReconcilePeriod(ctx, "SIM_NG", "prg_sim_airtime01", winStart, winEnd)
	if err != nil {
		t.Fatal(err)
	}
	var itemsRun1 int
	if err := f.db.Admin.QueryRow(ctx, `SELECT count(*) FROM recon_items WHERE run_id=$1`, sum1.RunID).Scan(&itemsRun1); err != nil {
		t.Fatal(err)
	}
	if itemsRun1 != 1 {
		t.Fatalf("run1 should have 1 break item, got %d", itemsRun1)
	}

	// A complete rerun supersedes run1 (floor of a 0-count prior is 0).
	*feed = []telcoTransaction{matchTxn("adv_b", 5_000)}
	if _, err := f.svc.ReconcilePeriod(ctx, "SIM_NG", "prg_sim_airtime01", winStart, winEnd); err != nil {
		t.Fatal(err)
	}
	if f.runState(t, sum1.RunID) != "SUPERSEDED" {
		t.Fatal("run1 must be SUPERSEDED")
	}
	// run1's items must STILL exist — supersession never deletes prior evidence.
	var stillThere int
	if err := f.db.Admin.QueryRow(ctx, `SELECT count(*) FROM recon_items WHERE run_id=$1`, sum1.RunID).Scan(&stillThere); err != nil {
		t.Fatal(err)
	}
	if stillThere != 1 {
		t.Fatalf("a superseded run's items must survive (break-resolution not orphaned), got %d", stillThere)
	}
}
