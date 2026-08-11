package recon

// BX-HIGH-016: a REJECTED RECOVERY reconciliation must never advance freshness and reopen the
// recharge money gate.
//
// THE DEFECT. The completeness floor exists to refuse a snapshot it cannot trust: it sets
// sum.Rejected, persists a REJECTED run and deliberately preserves the previous ACTIVE run. That
// Summary then returns NORMALLY — no error. The hold-clearing path already refused it
// (`!sum.Rejected && !sum.NothingToReconcile`), but the freshness path did not, so the same rejected
// snapshot could renew last_recon_at and hold the webhook open. The engine said "do not trust this"
// and the money gate used it as proof.
//
// The reachable shape is narrow and is precisely what the floor is for: a prior ACTIVE run with
// rows, a window whose platform side is now zero, and a feed that collapses to an authenticated
// zero. The floor rejects; the quiet-day branch reads (0,0) as confirmed; the gate reopens.
//
// This test drives the REAL path — ReconcileRecoveryDay to establish the trusted baseline, then
// RunRecovery (the freshness-advancing caller) for the rejected run — and pins all eight properties
// the fix must hold, not just the final gate state.

import (
	"context"
	"testing"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
)

func TestBXHIGH016_RejectedRecoveryRunMustNotAdvanceFreshness(t *testing.T) {
	f := newRecoveryFixture(t, "high016_rejected")
	ctx := context.Background()

	// (1) Armed, with NO prior freshness — the layer starts armed-but-not-live.
	armLayer(t, f, "SIM_NG", repo.ReconLayerRecovery)
	if ts := lastReconAt(t, f, "SIM_NG", repo.ReconLayerRecovery); ts != nil {
		t.Fatalf("precondition: a freshly armed layer must have no freshness, got %v", ts)
	}

	loc, err := time.LoadLocation("Africa/Lagos")
	if err != nil {
		t.Fatal(err)
	}
	// The day RunRecovery will actually evaluate as newest-settled (lag seed = 3600s). The baseline
	// must live on THIS day, or the rejected run would be compared against nothing.
	dLast, ok := lastSettledBusinessDay(loc, time.Now().UTC().Add(-time.Hour))
	if !ok {
		t.Fatal("expected a settled business day")
	}
	start, _, err := businessDayWindow(loc, dLast)
	if err != nil {
		t.Fatal(err)
	}
	at := start.Add(12 * time.Hour)

	// (2) A trusted baseline on that day: four booked recoveries, each confirmed by a feed row, so
	//     the run is ACTIVE with a source count of 4. Driven through ReconcileRecoveryDay rather than
	//     RunRecovery deliberately — establishing the baseline must NOT itself advance freshness, or
	//     assertions (7) and (8) below could not distinguish the fix from the defect.
	for _, ev := range []struct {
		id, token string
		minor     int64
	}{{"h016a", "tok_h016a", 500}, {"h016b", "tok_h016b", 500}, {"h016c", "tok_h016c", 500}, {"h016d", "tok_h016d", 500}} {
		if _, err := f.db.Admin.Exec(ctx, `
			INSERT INTO recovery_events (recovery_event_id, telco_id, source_event_id,
			  msisdn_token, amount_minor, currency, state, occurred_at)
			VALUES ($1,'SIM_NG',$2,$3,$4,'NGN','ALLOCATED',$5)`,
			ev.id, "wh:"+ev.id, ev.token, ev.minor, at); err != nil {
			t.Fatalf("seed recovery event %s: %v", ev.id, err)
		}
		if _, err := f.db.Admin.Exec(ctx, `
			INSERT INTO recovery_eod_feed (telco_id, business_date, msisdn_token, recovery_deducted_minor, currency)
			VALUES ('SIM_NG', $1::date, $2, $3, 'NGN')`, dLast, ev.token, ev.minor); err != nil {
			t.Fatalf("seed feed row %s: %v", ev.token, err)
		}
	}
	base, err := f.svc.ReconcileRecoveryDay(ctx, "SIM_NG", dLast)
	if err != nil {
		t.Fatalf("baseline ReconcileRecoveryDay: %v", err)
	}
	if base.Rejected || base.Matched != 4 {
		t.Fatalf("baseline must be a clean 4-match ACTIVE run, got %+v", base)
	}
	if ts := lastReconAt(t, f, "SIM_NG", repo.ReconLayerRecovery); ts != nil {
		t.Fatalf("ReconcileRecoveryDay must not advance freshness on its own, got %v", ts)
	}

	// (3) Collapse BOTH sides to zero: no booked recoveries left, and a feed that authenticates but
	//     reports nothing. Against a count-4 baseline this is below the completeness floor.
	if _, err := f.db.Admin.Exec(ctx, `DELETE FROM recovery_events WHERE telco_id='SIM_NG'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(ctx, `DELETE FROM recovery_eod_feed WHERE telco_id='SIM_NG'`); err != nil {
		t.Fatal(err)
	}

	// Drive the FRESHNESS-ADVANCING caller, which is where the defect lived.
	out, err := f.svc.RunRecovery(ctx, "SIM_NG")
	if err != nil {
		t.Fatalf("RunRecovery: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("RunRecovery reconciled no days — the fixture never reached the code under test")
	}
	sum := out[len(out)-1] // the newest settled day is the one freshness is derived from

	// (4) The engine must have refused this snapshot.
	if !sum.Rejected {
		t.Fatalf("a zero re-delivery under a count-4 baseline must be REJECTED by the completeness floor, got %+v", sum)
	}
	// (5) …and that refusal must be durable, not just in-memory.
	if st := f.runState(t, sum.RunID); st != "REJECTED" {
		t.Fatalf("the rejected run must be persisted REJECTED, got %s", st)
	}
	// (6) …while the trusted baseline survives as the ACTIVE run.
	if st := f.runState(t, base.RunID); st != "ACTIVE" {
		t.Fatalf("the prior good run must remain ACTIVE, got %s", st)
	}

	// (7) THE FIX: freshness must be untouched by evidence the engine rejected.
	if ts := lastReconAt(t, f, "SIM_NG", repo.ReconLayerRecovery); ts != nil {
		t.Fatalf("BX-HIGH-016: a REJECTED run advanced freshness to %v — the completeness control "+
			"refused this snapshot and the money gate used it as proof anyway", ts)
	}
	// (8) …so the recharge webhook money gate stays shut.
	if live, err := f.svc.Arming.IsLayerLive(ctx, "SIM_NG", repo.ReconLayerRecovery); err != nil || live {
		t.Fatalf("the RECOVERY layer must not be live off a REJECTED run (live=%v err=%v)", live, err)
	}
}

// The other half of the invariant: the fix must not close the legitimate door. An authenticated
// quiet day that the completeness floor did NOT reject still confirms and still advances freshness.
// Without this, "reject everything" would pass the test above and silently disarm the platform.
func TestBXHIGH016_NonRejectedQuietDayStillAdvancesFreshness(t *testing.T) {
	f := newRecoveryFixture(t, "high016_quiet_ok")
	ctx := context.Background()
	armLayer(t, f, "SIM_NG", repo.ReconLayerRecovery)

	// No baseline run, nothing booked, nothing in the feed: a genuine quiet day, not a rejected one.
	if _, err := f.svc.RunRecovery(ctx, "SIM_NG"); err != nil {
		t.Fatalf("RunRecovery: %v", err)
	}
	if live, err := f.svc.Arming.IsLayerLive(ctx, "SIM_NG", repo.ReconLayerRecovery); err != nil || !live {
		t.Fatalf("a legitimate non-rejected quiet day must still confirm and make the layer live "+
			"(live=%v err=%v) — BX-HIGH-016 must not change quiet-day semantics", live, err)
	}
}
