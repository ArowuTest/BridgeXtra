package recon

// R-P0-6 Slice D3 (VR-48): completeness maker-checker override.
//
// The completeness floor rejects a re-reconcile whose source shrank below
// min_completeness_ratio of the prior run — protection against a truncated feed
// wiping a good run. But it cannot tell a truncated feed from a LEGITIMATELY
// low-volume window. A two-actor override lets a maker propose and a distinct
// checker approve accepting such a window anyway; the next re-reconcile consumes
// the override to supersede despite the floor. The override is tightly scoped:
// four-eyes, bound to the reviewed source count and the exact ACTIVE run
// reviewed, and single-use.

import (
	"context"
	"errors"
	"testing"
)

// seedFourMatched stands up a live-feed fixture with four matched advances and
// reconciles the fixed window once, leaving an ACTIVE run of source count 4.
func seedFourMatched(t *testing.T, suffix string) (*reconFixture, *telcoFeed) {
	t.Helper()
	f, feed := newReconFixtureFeed(t, suffix)
	ctx := context.Background()
	recs := make([]telcoTransaction, 0, 4)
	for _, id := range []string{"adv1", "adv2", "adv3", "adv4"} {
		f.seedConfirmedAdvance(t, id, 5_000, "NGN", "TR-"+id)
		recs = append(recs, matchTxn(id, 5_000))
	}
	feed.set(recs)
	sum, err := f.svc.ReconcilePeriod(ctx, "SIM_NG", "prg_sim_airtime01", winStart, winEnd)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Matched != 4 {
		t.Fatalf("setup: want 4 matched, got %+v", sum)
	}
	return f, feed
}

func (f *reconFixture) overrideState(t *testing.T, overrideID string) (string, *string) {
	t.Helper()
	var state string
	var consumedBy *string
	if err := f.db.Admin.QueryRow(context.Background(),
		`SELECT state, consumed_by_run_id FROM recon_completeness_overrides WHERE override_id=$1`, overrideID).
		Scan(&state, &consumedBy); err != nil {
		t.Fatal(err)
	}
	return state, consumedBy
}

// The happy path: a legitimately low-volume window rejected by the floor is
// accepted by an approved two-actor override — and only AFTER approval.
func TestRP06D3_TwoActorOverride_AcceptsRejectedLowVolume(t *testing.T) {
	f, feed := seedFourMatched(t, "rp06d3_happy")
	ctx := context.Background()

	// Shrink to 1 record (< floor = ceil(4*0.5)=2) → the re-reconcile is REJECTED,
	// the 4-record ACTIVE run stays live.
	feed.set([]telcoTransaction{matchTxn("adv1", 5_000)})
	rej, err := f.svc.ReconcilePeriod(ctx, "SIM_NG", "prg_sim_airtime01", winStart, winEnd)
	if err != nil {
		t.Fatal(err)
	}
	if !rej.Rejected {
		t.Fatalf("a shrink below the completeness floor must be REJECTED, got %+v", rej)
	}

	// Maker proposes.
	overrideID, err := f.svc.ProposeCompletenessOverride(ctx, "SIM_NG", "prg_sim_airtime01", winStart, "maker", "telco voided 3 records — legitimately quiet window")
	if err != nil {
		t.Fatal(err)
	}

	// Before approval, the override does NOT authorize: the re-reconcile is still
	// REJECTED (a PENDING override is not an approval).
	stillRej, err := f.svc.ReconcilePeriod(ctx, "SIM_NG", "prg_sim_airtime01", winStart, winEnd)
	if err != nil {
		t.Fatal(err)
	}
	if !stillRej.Rejected || stillRej.CompletenessOverridden {
		t.Fatalf("a PENDING override must not authorize a supersede, got %+v", stillRej)
	}

	// Checker (distinct actor) approves.
	if err := f.svc.ApproveCompletenessOverride(ctx, "SIM_NG", overrideID, "checker"); err != nil {
		t.Fatal(err)
	}

	// Now the re-reconcile is accepted: it supersedes despite the floor, and the
	// override is consumed.
	ok, err := f.svc.ReconcilePeriod(ctx, "SIM_NG", "prg_sim_airtime01", winStart, winEnd)
	if err != nil {
		t.Fatal(err)
	}
	if ok.Rejected || !ok.CompletenessOverridden {
		t.Fatalf("an approved override must accept the low-volume window, got %+v", ok)
	}
	if ok.Matched != 1 {
		t.Fatalf("the accepted window must reconcile its 1 record, got matched=%d", ok.Matched)
	}
	// The live run is now the 1-record run; the override is CONSUMED, naming it.
	var activeCount int64
	if err := f.db.Admin.QueryRow(ctx, `
		SELECT source_record_count FROM recon_runs
		WHERE state='ACTIVE' AND period_start=$1`, winStart).Scan(&activeCount); err != nil {
		t.Fatal(err)
	}
	if activeCount != 1 {
		t.Fatalf("the live run must be the accepted 1-record run, got source_count=%d", activeCount)
	}
	state, consumedBy := f.overrideState(t, overrideID)
	if state != "CONSUMED" || consumedBy == nil || *consumedBy != ok.RunID {
		t.Fatalf("override must be CONSUMED naming the run that used it: state=%s by=%v want=%s", state, consumedBy, ok.RunID)
	}
}

// Four-eyes: the same actor cannot both propose and approve.
func TestRP06D3_SelfApprove_Rejected(t *testing.T) {
	f, feed := seedFourMatched(t, "rp06d3_self")
	ctx := context.Background()
	feed.set([]telcoTransaction{matchTxn("adv1", 5_000)})
	if _, err := f.svc.ReconcilePeriod(ctx, "SIM_NG", "prg_sim_airtime01", winStart, winEnd); err != nil {
		t.Fatal(err)
	}
	overrideID, err := f.svc.ProposeCompletenessOverride(ctx, "SIM_NG", "prg_sim_airtime01", winStart, "solo", "trying to self-approve")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.ApproveCompletenessOverride(ctx, "SIM_NG", overrideID, "solo"); !errors.Is(err, ErrSelfApproveOverride) {
		t.Fatalf("self-approval must be refused by the four-eyes rule, got %v", err)
	}
	if state, _ := f.overrideState(t, overrideID); state != "PENDING" {
		t.Fatalf("a refused self-approval must leave the override PENDING, got %s", state)
	}
}

// Single-use: once consumed, the override cannot authorize a later shrink.
func TestRP06D3_ConsumedOverride_NotReusable(t *testing.T) {
	f, feed := seedFourMatched(t, "rp06d3_once")
	ctx := context.Background()

	feed.set([]telcoTransaction{matchTxn("adv1", 5_000)})
	if _, err := f.svc.ReconcilePeriod(ctx, "SIM_NG", "prg_sim_airtime01", winStart, winEnd); err != nil {
		t.Fatal(err)
	}
	overrideID, err := f.svc.ProposeCompletenessOverride(ctx, "SIM_NG", "prg_sim_airtime01", winStart, "maker", "legit")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.ApproveCompletenessOverride(ctx, "SIM_NG", overrideID, "checker"); err != nil {
		t.Fatal(err)
	}
	// Consume it (accepts the 1-record window → ACTIVE is now 1 record).
	consumed, err := f.svc.ReconcilePeriod(ctx, "SIM_NG", "prg_sim_airtime01", winStart, winEnd)
	if err != nil || !consumed.CompletenessOverridden {
		t.Fatalf("first re-reconcile should consume the override: %+v err=%v", consumed, err)
	}
	// A later shrink to 0 (< new floor ceil(1*0.5)=1) has no live override → REJECTED.
	feed.set(nil)
	again, err := f.svc.ReconcilePeriod(ctx, "SIM_NG", "prg_sim_airtime01", winStart, winEnd)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Rejected || again.CompletenessOverridden {
		t.Fatalf("a consumed override must not authorize a later shrink, got %+v", again)
	}
}

// Bound by the reviewed count: an override approved for a source count of 1 does
// NOT authorize an even-worse (0-record) shrink — that is a different feed.
func TestRP06D3_Override_DoesNotAuthorizeWorseShrink(t *testing.T) {
	f, feed := seedFourMatched(t, "rp06d3_bound")
	ctx := context.Background()

	feed.set([]telcoTransaction{matchTxn("adv1", 5_000)}) // 1 record → authorized_source_count=1
	if _, err := f.svc.ReconcilePeriod(ctx, "SIM_NG", "prg_sim_airtime01", winStart, winEnd); err != nil {
		t.Fatal(err)
	}
	overrideID, err := f.svc.ProposeCompletenessOverride(ctx, "SIM_NG", "prg_sim_airtime01", winStart, "maker", "reviewed 1-record window")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.ApproveCompletenessOverride(ctx, "SIM_NG", overrideID, "checker"); err != nil {
		t.Fatal(err)
	}
	// A WORSE shrink than the reviewed count (0 < authorized 1) is not authorized.
	feed.set(nil)
	worse, err := f.svc.ReconcilePeriod(ctx, "SIM_NG", "prg_sim_airtime01", winStart, winEnd)
	if err != nil {
		t.Fatal(err)
	}
	if !worse.Rejected || worse.CompletenessOverridden {
		t.Fatalf("an override must not authorize a shrink worse than the reviewed count, got %+v", worse)
	}
	if state, _ := f.overrideState(t, overrideID); state != "APPROVED" {
		t.Fatalf("an un-consumed override must remain APPROVED, got %s", state)
	}
}

// Bound to the reviewed baseline: if the window is legitimately re-reconciled
// after approval (the reviewed ACTIVE run is superseded), the override is stale
// and does not authorize a later shrink against the new baseline.
func TestRP06D3_Override_StaleAfterInterveningReReconcile(t *testing.T) {
	f, feed := seedFourMatched(t, "rp06d3_stale")
	ctx := context.Background()

	feed.set([]telcoTransaction{matchTxn("adv1", 5_000)})
	if _, err := f.svc.ReconcilePeriod(ctx, "SIM_NG", "prg_sim_airtime01", winStart, winEnd); err != nil {
		t.Fatal(err)
	}
	overrideID, err := f.svc.ProposeCompletenessOverride(ctx, "SIM_NG", "prg_sim_airtime01", winStart, "maker", "reviewed against the 4-record baseline")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.ApproveCompletenessOverride(ctx, "SIM_NG", overrideID, "checker"); err != nil {
		t.Fatal(err)
	}
	// Intervening LEGIT re-reconcile: the full 4-record feed returns and supersedes
	// the reviewed baseline (no floor issue) → the ACTIVE run is now a different one.
	full := make([]telcoTransaction, 0, 4)
	for _, id := range []string{"adv1", "adv2", "adv3", "adv4"} {
		full = append(full, matchTxn(id, 5_000))
	}
	feed.set(full)
	if sum, err := f.svc.ReconcilePeriod(ctx, "SIM_NG", "prg_sim_airtime01", winStart, winEnd); err != nil || sum.Matched != 4 {
		t.Fatalf("intervening re-reconcile should restore 4 matched: %+v err=%v", sum, err)
	}
	// Now shrink again: the approved override was reviewed against the OLD baseline,
	// which no longer exists → it does not authorize; the shrink is REJECTED.
	feed.set([]telcoTransaction{matchTxn("adv1", 5_000)})
	stale, err := f.svc.ReconcilePeriod(ctx, "SIM_NG", "prg_sim_airtime01", winStart, winEnd)
	if err != nil {
		t.Fatal(err)
	}
	if !stale.Rejected || stale.CompletenessOverridden {
		t.Fatalf("an override reviewed against a superseded baseline must be stale, got %+v", stale)
	}
}
