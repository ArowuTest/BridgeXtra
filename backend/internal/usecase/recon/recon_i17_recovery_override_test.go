package recon

// I17 — the RECOVERY completeness override. Before I17,
// ProposeCompletenessOverride hard-coded the FULFILMENT layer, so an override for a
// legitimately-quiet RECOVERY day could not be created in a form the RECOVERY
// re-reconcile would ever consume (the consumer matches on layer=spec.name):
// armed-but-dead. These tests prove the layer now threads end-to-end — an override
// tagged RECOVERY is found and consumed by a RECOVERY re-reconcile — and that an
// unknown layer is refused fail-closed.

import (
	"context"
	"strings"
	"testing"
	"time"
)

// recWinStart is the UTC window start of the Lagos business day recBusinessDate
// (2026-06-15) — Lagos is UTC+1 with no DST, so 2026-06-14T23:00:00Z. It must equal
// the recon run's period_start for the override to scope to exactly that window.
var recWinStart = time.Date(2026, 6, 14, 23, 0, 0, 0, time.UTC)

func (f *reconFixture) deleteFeedRow(t *testing.T, token string) {
	t.Helper()
	if _, err := f.db.Admin.Exec(context.Background(),
		`DELETE FROM recovery_eod_feed WHERE telco_id='SIM_NG' AND business_date=DATE '2026-06-15' AND msisdn_token=$1`, token); err != nil {
		t.Fatalf("delete feed row %s: %v", token, err)
	}
}

func (f *reconFixture) deleteRecoveryEvent(t *testing.T, evID string) {
	t.Helper()
	if _, err := f.db.Admin.Exec(context.Background(),
		`DELETE FROM recovery_events WHERE recovery_event_id=$1`, evID); err != nil {
		t.Fatalf("delete recovery event %s: %v", evID, err)
	}
}

// I17: a RECOVERY day the completeness floor REJECTS on a shrunk re-delivery is
// accepted by a two-actor override that names layer=RECOVERY — proving the layer
// threads from propose through to the RECOVERY re-reconcile's consumer.
func TestI17_RecoveryCompletenessOverride_Consumed(t *testing.T) {
	f := newRecoveryFixture(t, "i17_recovery_override")
	ctx := context.Background()

	// Day 1: 4 booked recoveries, all confirmed by a 4-row feed → ACTIVE (count=4).
	for _, tok := range []struct {
		ev, token string
		minor     int64
	}{{"ev1", "tok1", 500}, {"ev2", "tok2", 500}, {"ev3", "tok3", 500}, {"ev4", "tok4", 500}} {
		f.seedRecoveryEvent(t, tok.ev, tok.token, tok.minor, "")
		f.seedFeedRow(t, tok.token, tok.minor)
	}
	if sum := f.reconcileDay(t); sum.Matched != 4 || sum.Rejected {
		t.Fatalf("day 1 must be a clean 4-match ACTIVE run, got %+v", sum)
	}

	// Re-delivery: the day is legitimately down to a single subscriber (tok1). The
	// feed drops to 1 row (below the 0.5 completeness floor of the count-4 baseline)
	// and the other three booked recoveries are gone too (so the accepted window is
	// genuinely clean, not phantom breaks).
	for _, x := range []struct{ ev, tok string }{{"ev2", "tok2"}, {"ev3", "tok3"}, {"ev4", "tok4"}} {
		f.deleteFeedRow(t, x.tok)
		f.deleteRecoveryEvent(t, x.ev)
	}
	if sum := f.reconcileDay(t); !sum.Rejected {
		t.Fatalf("a 1-row re-delivery under a count-4 baseline must be REJECTED by the completeness floor, got %+v", sum)
	}

	// Two-actor override, tagged RECOVERY, scoped to exactly this window.
	ovID, err := f.svc.ProposeCompletenessOverride(ctx, "SIM_NG", recoveryProgrammeSentinel, layerRecovery, recWinStart, "maker", "single-subscriber day, reviewed")
	if err != nil {
		t.Fatalf("propose RECOVERY override: %v", err)
	}
	if err := f.svc.ApproveCompletenessOverride(ctx, "SIM_NG", ovID, "checker"); err != nil {
		t.Fatalf("approve RECOVERY override: %v", err)
	}

	// Re-reconcile: the RECOVERY consumer must FIND the RECOVERY-tagged override and
	// accept the window — before I17 the override was FULFILMENT-tagged and invisible.
	sum := f.reconcileDay(t)
	if sum.Rejected {
		t.Fatalf("the approved RECOVERY override must let the re-reconcile through, still REJECTED: %+v", sum)
	}
	if !sum.CompletenessOverridden {
		t.Fatalf("the run must record it consumed a completeness override, got %+v", sum)
	}
	if sum.Matched != 1 || sum.MissingTelco != 0 || sum.MissingPlatform != 0 {
		t.Fatalf("the accepted single-subscriber window must be a clean 1-match, got %+v", sum)
	}
}

// I17 fail-closed: an override for a layer the engine cannot reconcile is refused up
// front, so no override can ever be created that no run will consume.
func TestI17_CompletenessOverride_UnknownLayerRefused(t *testing.T) {
	f := newRecoveryFixture(t, "i17_unknown_layer")
	for _, layer := range []string{"BUREAU", "SETTLEMENT", "", "fulfilment"} {
		_, err := f.svc.ProposeCompletenessOverride(context.Background(), "SIM_NG", recoveryProgrammeSentinel, layer, recWinStart, "maker", "x")
		if err == nil || !strings.Contains(err.Error(), "unknown layer") {
			t.Fatalf("layer %q must be refused with an unknown-layer error, got %v", layer, err)
		}
	}
}
