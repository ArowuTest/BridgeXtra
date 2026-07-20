package recon

// R-P0-6 Slice D2 (VR-50-F1 / REC-006): late-arrival re-reconcile.
//
// The incremental RunFulfilment advances the watermark past a window based on
// the telco records present at run time. A telco credit that arrives AFTER its
// window was reconciled is behind the watermark and is never revisited by
// future incremental runs — it would be stranded forever as a missing-telco
// break. ReconcileRecentPeriods is the scheduled recovery sweep: it re-reconciles
// settled windows that ended within the governed lookback, recovering the late
// credit as a MATCHED, while an unchanged window is skipped (no trail churn) and
// a window older than the lookback is left alone.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
)

// telcoFeed is a mutable telco-side record set the test can swap between runs,
// so a late record can "arrive" after a window has already been reconciled. It
// is mutex-guarded because the httptest handler goroutine reads it concurrently
// with the test goroutine's writes.
type telcoFeed struct {
	mu   sync.Mutex
	recs []telcoTransaction
}

func (tf *telcoFeed) set(recs []telcoTransaction) {
	tf.mu.Lock()
	defer tf.mu.Unlock()
	tf.recs = recs
}

func (tf *telcoFeed) snapshot() []telcoTransaction {
	tf.mu.Lock()
	defer tf.mu.Unlock()
	out := make([]telcoTransaction, len(tf.recs))
	copy(out, tf.recs)
	return out
}

// newReconFixtureFeed stands up a recon fixture whose telco endpoint serves a
// live, mutable feed the test controls.
func newReconFixtureFeed(t *testing.T, suffix string) (*reconFixture, *telcoFeed) {
	t.Helper()
	db := testutil.MustSetup(t, suffix)
	feed := &telcoFeed{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(feed.snapshot())
	}))
	t.Cleanup(srv.Close)
	return newReconFixtureURL(t, db, srv.URL), feed
}

// activeStatusCount counts recon_items of a given status that belong to a
// CURRENTLY ACTIVE run — i.e. the live reconciliation picture, excluding items
// that survive in superseded runs.
func (f *reconFixture) activeStatusCount(t *testing.T, status string) int {
	t.Helper()
	var n int
	if err := f.db.Admin.QueryRow(context.Background(), `
		SELECT count(*) FROM recon_items i
		JOIN recon_runs r ON r.run_id = i.run_id
		WHERE r.state='ACTIVE' AND i.status=$1`, status).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (f *reconFixture) runCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.db.Admin.QueryRow(context.Background(), `SELECT count(*) FROM recon_runs`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The adversarial recovery proof: a telco credit lands after its window was
// reconciled and the watermark advanced. Incremental reconciliation does NOT
// recover it; the scheduled re-reconcile does.
func TestRP06D2_LateArrival_RecoveredByReReconcile(t *testing.T) {
	f, feed := newReconFixtureFeed(t, "rp06d2_late") // feed starts EMPTY: the credit hasn't arrived
	f.seedConfirmedAdvance(t, "adv_late", 5_000, "NGN", "TR-adv_late")
	ctx := context.Background()

	// First incremental run: platform believes credited, telco has no record yet
	// → the advance is a BREAK_MISSING_TELCO, and the watermark advances past it.
	if _, err := f.svc.RunFulfilment(ctx, "SIM_NG", "prg_sim_airtime01"); err != nil {
		t.Fatal(err)
	}
	if got := f.activeStatusCount(t, "BREAK_MISSING_TELCO"); got != 1 {
		t.Fatalf("with no telco record the advance must be a missing-telco break, got %d", got)
	}
	if got := f.activeStatusCount(t, "MATCHED"); got != 0 {
		t.Fatalf("nothing may match before the credit arrives, got %d matched", got)
	}

	// The late telco credit arrives — credited in the past, INSIDE the window that
	// was already reconciled (behind the advanced watermark).
	feed.set([]telcoTransaction{matchTxn("adv_late", 5_000)})

	// A further incremental run must NOT recover it: the watermark has advanced
	// past the record's window, so incremental reconciliation never revisits it.
	if _, err := f.svc.RunFulfilment(ctx, "SIM_NG", "prg_sim_airtime01"); err != nil {
		t.Fatal(err)
	}
	if got := f.activeStatusCount(t, "MATCHED"); got != 0 {
		t.Fatalf("incremental reconciliation must NOT recover a record behind the watermark, got %d matched", got)
	}
	if got := f.activeStatusCount(t, "BREAK_MISSING_TELCO"); got != 1 {
		t.Fatalf("the stranded break must persist after an incremental run, got %d", got)
	}

	// The scheduled re-reconcile re-sweeps recent settled windows and recovers it.
	sums, err := f.svc.ReconcileRecentPeriods(ctx, "SIM_NG", "prg_sim_airtime01")
	if err != nil {
		t.Fatal(err)
	}
	if len(sums) == 0 {
		t.Fatal("the re-reconcile must have swept at least the recovered window")
	}
	if got := f.activeStatusCount(t, "MATCHED"); got != 1 {
		t.Fatalf("the late credit must be recovered as MATCHED in the live run, got %d", got)
	}
	if got := f.activeStatusCount(t, "BREAK_MISSING_TELCO"); got != 0 {
		t.Fatalf("the missing-telco break must be gone from the live run after recovery, got %d", got)
	}
	// The append-only trail is preserved: the superseded run still carries the
	// original break, so recovery does not erase the fact that it was once open.
	if got := f.statusCount(t, "BREAK_MISSING_TELCO"); got != 1 {
		t.Fatalf("the original break must survive in the superseded run, got %d", got)
	}
}

// A re-reconcile of a window whose telco source is unchanged is a no-op: it must
// write no new run (no money-trail churn) and flag the window Unchanged.
func TestRP06D2_ReReconcile_NoChange_IsNoOp(t *testing.T) {
	f := newReconFixture(t, "rp06d2_noop", []telcoTransaction{matchTxn("adv_ok", 5_000)})
	f.seedConfirmedAdvance(t, "adv_ok", 5_000, "NGN", "TR-adv_ok")
	ctx := context.Background()

	if _, err := f.svc.RunFulfilment(ctx, "SIM_NG", "prg_sim_airtime01"); err != nil {
		t.Fatal(err)
	}
	before := f.runCount(t)

	sums, err := f.svc.ReconcileRecentPeriods(ctx, "SIM_NG", "prg_sim_airtime01")
	if err != nil {
		t.Fatal(err)
	}
	if after := f.runCount(t); after != before {
		t.Fatalf("an unchanged re-reconcile must write no new run: before=%d after=%d", before, after)
	}
	if len(sums) == 0 {
		t.Fatal("the sweep must report the window it inspected")
	}
	for _, s := range sums {
		if !s.Unchanged {
			t.Fatalf("a window with an identical source must be flagged Unchanged, got %+v", s)
		}
	}
}

// The sweep must respect the governed lookback: a window that ended within the
// horizon is re-reconciled, but one older than the horizon is left untouched —
// otherwise the sweep would rescan all history every cycle.
func TestRP06D2_ReReconcile_RespectsLookbackBound(t *testing.T) {
	f := newReconFixture(t, "rp06d2_bound", nil) // empty feed
	ctx := context.Background()

	// Two ACTIVE runs seeded by hand. Both carry a source_hash that will NOT match
	// the recomputed manifest over the empty feed, so each WOULD be re-reconciled
	// (superseded) if the sweep selected it — isolating selection from no-op.
	seedActiveRun := func(runID string, startAgoSec, endAgoSec int) {
		if _, err := f.db.Admin.Exec(ctx, `
			INSERT INTO recon_runs (run_id, telco_id, programme_id, layer, period_start, period_end,
			  source_record_count, source_control_total_minor, source_hash,
			  platform_record_count, platform_control_total_minor, created_by)
			VALUES ($1,'SIM_NG','prg_sim_airtime01','FULFILMENT',
			  now()-make_interval(secs=>$2), now()-make_interval(secs=>$3),
			  0,0,'stale-hash-never-matches-empty-manifest', 0,0,'test')`,
			runID, startAgoSec, endAgoSec); err != nil {
			t.Fatalf("seed active run: %v", err)
		}
	}
	seedActiveRun("run_recent", 7200, 5400)        // ended 90m ago — within the 7d lookback
	seedActiveRun("run_old", 3_456_000, 3_369_600) // ended 39d ago — outside the 7d lookback

	if _, err := f.svc.ReconcileRecentPeriods(ctx, "SIM_NG", "prg_sim_airtime01"); err != nil {
		t.Fatal(err)
	}

	state := func(runID string) (string, *string) {
		var st string
		var by *string
		if err := f.db.Admin.QueryRow(ctx, `SELECT state, superseded_by FROM recon_runs WHERE run_id=$1`, runID).Scan(&st, &by); err != nil {
			t.Fatal(err)
		}
		return st, by
	}
	if st, by := state("run_recent"); st != "SUPERSEDED" || by == nil {
		t.Fatalf("the recent window (within lookback) must be re-reconciled/superseded, got state=%s superseded_by=%v", st, by)
	}
	if st, by := state("run_old"); st != "ACTIVE" || by != nil {
		t.Fatalf("the old window (outside lookback) must be left untouched, got state=%s superseded_by=%v", st, by)
	}
}
