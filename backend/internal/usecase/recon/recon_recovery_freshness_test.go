package recon

// S3-B arm-freshness pack (build/PHASE1_S3_DESIGN.md §3 I3-I7). The single new
// safety control: "live" means "armed AND a RECENT recon confirmed the feed",
// never merely "a run executed". Arming rows are created/deleted via the admin
// pool (production: the four-eyes tcp_worker path); IsLayerLive / AdvanceFreshness
// run on the recon Service's tcp_app Arming (the production freshness-advancer).

import (
	"context"
	"testing"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
)

func armLayer(t *testing.T, f *reconFixture, telco, layer string) {
	t.Helper()
	if _, err := f.db.Admin.Exec(context.Background(),
		`INSERT INTO recon_layer_arming (telco_id, layer) VALUES ($1,$2) ON CONFLICT DO NOTHING`, telco, layer); err != nil {
		t.Fatalf("arm %s/%s: %v", telco, layer, err)
	}
}

func lastReconAt(t *testing.T, f *reconFixture, telco, layer string) *time.Time {
	t.Helper()
	var ts *time.Time
	if err := f.db.Admin.QueryRow(context.Background(),
		`SELECT last_recon_at FROM recon_layer_arming WHERE telco_id=$1 AND layer=$2`, telco, layer).Scan(&ts); err != nil {
		t.Fatalf("read last_recon_at: %v", err)
	}
	return ts
}

// I3 (empty/feed-only) + I4 (quiet/matched): the pure confirmation gate.
func TestS3B_DayConfirmed(t *testing.T) {
	cases := []struct {
		name string
		sum  Summary
		want bool
	}{
		{"quiet day confirms", Summary{PlatformRecords: 0, SourceRecordCount: 0}, true},
		{"feed-only day (all missing-platform) does not", Summary{PlatformRecords: 0, SourceRecordCount: 3}, false},
		{"empty-feed booked day does not", Summary{PlatformRecords: 2, PlatformControlTotalMinor: 500, MatchedControlTotalMinor: 0}, false},
		{"under-floor does not", Summary{PlatformRecords: 2, PlatformControlTotalMinor: 1000, MatchedControlTotalMinor: 980}, false},
		{"fully matched confirms", Summary{PlatformRecords: 2, PlatformControlTotalMinor: 1000, MatchedControlTotalMinor: 1000}, true},
		{"at floor confirms", Summary{PlatformRecords: 2, PlatformControlTotalMinor: 1000, MatchedControlTotalMinor: 990}, true},

		// BX-HIGH-016: evidence the engine REJECTED is never positive confirmation. The first case is
		// the exact shape that reopened the money gate: a completeness-rejected run whose platform and
		// source sides are both zero would otherwise fall into the quiet-day branch and confirm.
		{"REJECTED quiet day does NOT confirm", Summary{Rejected: true, PlatformRecords: 0, SourceRecordCount: 0}, false},
		{"REJECTED fully-matched day does NOT confirm", Summary{Rejected: true, PlatformRecords: 2, PlatformControlTotalMinor: 1000, MatchedControlTotalMinor: 1000}, false},
		{"a summary that reconciled nothing does NOT confirm", Summary{NothingToReconcile: true, PlatformRecords: 0, SourceRecordCount: 0}, false},
	}
	for _, c := range cases {
		if got := dayConfirmed(c.sum, 0.99); got != c.want {
			t.Errorf("%s: dayConfirmed=%v want %v", c.name, got, c.want)
		}
	}
}

// The freshness gate: armed-but-never-confirmed is NOT live; a confirmed recon
// makes it live; a stale confirmation ages out to NOT live (fail-closed).
func TestS3B_IsLayerLive_FreshnessGate(t *testing.T) {
	f := newRecoveryFixture(t, "s3b_gate")
	ctx := context.Background()
	arm := f.svc.Arming
	armLayer(t, f, "SIM_NG", repo.ReconLayerRecovery)

	// Armed, never confirmed (last_recon_at NULL) -> NOT live.
	if live, _ := arm.IsLayerLive(ctx, "SIM_NG", repo.ReconLayerRecovery); live {
		t.Fatal("a freshly-armed layer must NOT be live until its first confirmed recon")
	}
	if armed, _ := arm.IsLayerArmed(ctx, "SIM_NG", repo.ReconLayerRecovery); !armed {
		t.Fatal("IsLayerArmed must be true for an armed layer")
	}

	// A confirmed recon -> live.
	if ok, err := arm.AdvanceFreshness(ctx, "SIM_NG", repo.ReconLayerRecovery, 172800); err != nil || !ok {
		t.Fatalf("AdvanceFreshness must update the armed row: ok=%v err=%v", ok, err)
	}
	if live, _ := arm.IsLayerLive(ctx, "SIM_NG", repo.ReconLayerRecovery); !live {
		t.Fatal("a confirmed, fresh layer must be live")
	}

	// Age the confirmation past the window (server-clock manipulation) -> NOT live.
	if _, err := f.db.Admin.Exec(ctx,
		`UPDATE recon_layer_arming SET last_recon_at = now() - interval '3 days' WHERE telco_id='SIM_NG' AND layer=$1`,
		repo.ReconLayerRecovery); err != nil {
		t.Fatal(err)
	}
	if live, _ := arm.IsLayerLive(ctx, "SIM_NG", repo.ReconLayerRecovery); live {
		t.Fatal("a stale confirmation (older than the freshness window) must NOT be live")
	}
}

// I5: AdvanceFreshness is UPDATE-only — it never inserts, never resurrects a
// disarmed row, and touches only the addressed (telco,layer).
func TestS3B_AdvanceFreshness_UpdateOnly(t *testing.T) {
	f := newRecoveryFixture(t, "s3b_updateonly")
	ctx := context.Background()
	arm := f.svc.Arming

	// Never-armed: 0 rows, NO insert.
	if ok, err := arm.AdvanceFreshness(ctx, "SIM_NG", repo.ReconLayerRecovery, 172800); err != nil || ok {
		t.Fatalf("AdvanceFreshness on an unarmed layer must affect 0 rows: ok=%v err=%v", ok, err)
	}
	if armed, _ := arm.IsLayerArmed(ctx, "SIM_NG", repo.ReconLayerRecovery); armed {
		t.Fatal("AdvanceFreshness must NEVER create an arming row (no resurrection)")
	}

	// Disarmed after arming: still no resurrection.
	armLayer(t, f, "SIM_NG", repo.ReconLayerRecovery)
	if _, err := f.db.Admin.Exec(ctx, `DELETE FROM recon_layer_arming WHERE telco_id='SIM_NG' AND layer=$1`, repo.ReconLayerRecovery); err != nil {
		t.Fatal(err)
	}
	if ok, _ := arm.AdvanceFreshness(ctx, "SIM_NG", repo.ReconLayerRecovery, 172800); ok {
		t.Fatal("AdvanceFreshness must not resurrect a disarmed layer")
	}

	// Tenant/layer isolation: advancing RECOVERY leaves a co-armed SETTLEMENT NULL.
	armLayer(t, f, "SIM_NG", repo.ReconLayerRecovery)
	armLayer(t, f, "SIM_NG", "SETTLEMENT")
	if _, err := arm.AdvanceFreshness(ctx, "SIM_NG", repo.ReconLayerRecovery, 172800); err != nil {
		t.Fatal(err)
	}
	if ts := lastReconAt(t, f, "SIM_NG", "SETTLEMENT"); ts != nil {
		t.Fatal("advancing RECOVERY must not touch SETTLEMENT's last_recon_at")
	}
	if ts := lastReconAt(t, f, "SIM_NG", repo.ReconLayerRecovery); ts == nil {
		t.Fatal("RECOVERY's last_recon_at must be set")
	}
}

// I6: the column CHECK makes an unbounded / zero freshness window unstorable by
// ANY writer.
func TestS3B_ArmFreshnessColumn_RejectsOutOfRange(t *testing.T) {
	f := newRecoveryFixture(t, "s3b_colcheck")
	ctx := context.Background()
	armLayer(t, f, "SIM_NG", repo.ReconLayerRecovery)
	for _, bad := range []int64{60, 1000000000, 0, -1} {
		if _, err := f.db.Admin.Exec(ctx,
			`UPDATE recon_layer_arming SET arm_freshness_max_seconds=$1 WHERE telco_id='SIM_NG' AND layer=$2`,
			bad, repo.ReconLayerRecovery); err == nil {
			t.Fatalf("arm_freshness_max_seconds=%d must violate the CHECK", bad)
		}
	}
}

// I4 (integration): an armed telco with a genuinely quiet newest settled day
// advances freshness -> live.
func TestS3B_RunRecovery_QuietDayArmsFreshness(t *testing.T) {
	f := newRecoveryFixture(t, "s3b_quietrun")
	ctx := context.Background()
	armLayer(t, f, "SIM_NG", repo.ReconLayerRecovery)
	if _, err := f.svc.RunRecovery(ctx, "SIM_NG"); err != nil {
		t.Fatalf("RunRecovery: %v", err)
	}
	if live, _ := f.svc.Arming.IsLayerLive(ctx, "SIM_NG", repo.ReconLayerRecovery); !live {
		t.Fatal("a quiet newest settled day must confirm and make the layer live")
	}
}

// I3 (integration): a booked recovery on the newest settled day with an empty feed
// is NOT confirmed -> freshness NOT advanced -> the gate stays closed.
func TestS3B_RunRecovery_UnconfirmedNewestDayStaysClosed(t *testing.T) {
	f := newRecoveryFixture(t, "s3b_shortfall")
	ctx := context.Background()
	armLayer(t, f, "SIM_NG", repo.ReconLayerRecovery)

	loc, _ := time.LoadLocation("Africa/Lagos")
	dLast, ok := lastSettledBusinessDay(loc, time.Now().UTC().Add(-time.Hour)) // lag seed = 3600s
	if !ok {
		t.Fatal("expected a settled day")
	}
	start, _, err := businessDayWindow(loc, dLast)
	if err != nil {
		t.Fatal(err)
	}
	// A booked recovery in the newest settled day; NO feed row confirms it.
	if _, err := f.db.Admin.Exec(ctx, `
		INSERT INTO recovery_events (recovery_event_id, telco_id, source_event_id,
		  msisdn_token, amount_minor, currency, state, occurred_at)
		VALUES ('ev_sf','SIM_NG','wh:ev_sf','tok_sf',500,'NGN','ALLOCATED',$1)`,
		start.Add(12*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.RunRecovery(ctx, "SIM_NG"); err != nil {
		t.Fatalf("RunRecovery: %v", err)
	}
	if live, _ := f.svc.Arming.IsLayerLive(ctx, "SIM_NG", repo.ReconLayerRecovery); live {
		t.Fatal("an unconfirmed newest day (booked money, empty feed) must NOT make the layer live")
	}
}

// I7: a backdated event landing in an already-reconciled day changes the platform
// fingerprint, so the re-sweep skip-gate does NOT skip it — forcing a re-reconcile
// that surfaces the phantom (the telco-only hash skip would have missed it).
func TestS3B_ReSweepSkipGate_DetectsBackdatedPlatform(t *testing.T) {
	f := newRecoveryFixture(t, "s3b_resweep")
	ctx := context.Background()
	cfg, adapter, err := f.svc.loadForRecovery(ctx, "SIM_NG")
	if err != nil {
		t.Fatal(err)
	}
	// A clean day: one booked recovery confirmed by the feed.
	f.seedRecoveryEvent(t, "ev_rs", "tokRS", 500, "")
	f.seedFeedRow(t, "tokRS", 500)
	sum := f.reconcileDay(t)
	if sum.Matched != 1 {
		t.Fatalf("setup day must be clean-matched, got %+v", sum)
	}
	// Nothing changed -> the skip-gate skips.
	if unchanged, err := f.svc.recoveryDayUnchanged(ctx, cfg, adapter, "SIM_NG", recBusinessDate); err != nil || !unchanged {
		t.Fatalf("an unchanged day must be skippable: unchanged=%v err=%v", unchanged, err)
	}
	// A backdated phantom lands in the already-reconciled day (platform-only change).
	f.seedRecoveryEvent(t, "ev_phantom", "tokPhantom", 900, "")
	if unchanged, err := f.svc.recoveryDayUnchanged(ctx, cfg, adapter, "SIM_NG", recBusinessDate); err != nil || unchanged {
		t.Fatalf("a backdated platform change must NOT be skippable: unchanged=%v err=%v", unchanged, err)
	}
	// Re-reconciling surfaces the phantom as a break.
	sum2 := f.reconcileDay(t)
	if sum2.MissingTelco != 1 {
		t.Fatalf("the re-reconcile must surface the backdated phantom as MISSING_TELCO, got %+v", sum2)
	}
}
