package recon

// Phase 1 #70 — the arm-and-verify INTEGRATION SEAM: the capstone that proves the two
// parallel tracks (the cmd/simseed seeder + the S3 RECOVERY recon layer) actually
// agree end-to-end, and that the recon catches real breaks.
//
// ANTI-CIRCULARITY (the reviewer's BLOCKER, honored here): every assertion is against
// (a) breaks the TEST itself injects by direct DB mutation — never the seeder's own
// return value / intent — and (b) an INDEPENDENT DB-truth query written as NOT-EXISTS
// joins over the raw tables, structurally different from the engine's map-based
// reconcileLayer. If the engine mis-keyed, mis-windowed, or dropped a class of break,
// the Summary would diverge from the hand-written SQL and this fails. The seeder is
// used ONLY to stand up a realistic clean baseline; it is never the oracle.
//
// Two parts:
//   A. The engine catches a phantom (booked, feed-missing → MISSING_TELCO), a drop
//      (feed-only → MISSING_PLATFORM) and a mutation (amounts differ → AMOUNT_MISMATCH)
//      injected on top of a clean seeded day — cross-checked against raw-table truth.
//   B. The four-eyes ARMING door admits the seeded SIM_NG telco (self-approve rejected,
//      distinct approve accepted, armed-but-not-yet-live), and a confirmed newest
//      settled day through the real RunRecovery makes the armed layer LIVE.

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/simseed"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
)

const seamSeed = "phase1-seam-test"

// seamSeedClean seeds a cohort of n and one clean recovery day (via the RLS app pool,
// exactly as cmd/simseed runs it) for the given business date.
func seamSeedClean(t *testing.T, app *pgxpool.Pool, date string, n int) {
	t.Helper()
	ctx := context.Background()
	if _, err := simseed.SeedCohort(ctx, app, simseed.CohortPlan{Seed: seamSeed, Count: n}); err != nil {
		t.Fatalf("SeedCohort: %v", err)
	}
	if _, err := simseed.SeedRecoveryDay(ctx, app, simseed.RecoveryDayPlan{Seed: seamSeed, BusinessDate: date, Count: n}); err != nil {
		t.Fatalf("SeedRecoveryDay: %v", err)
	}
}

// TestIntegrationSeam_ReconCatchesInjectedBreaks_vsDBTruth is the anti-circularity
// proof: on a clean seeded day the recon reports nothing; after the test injects one
// phantom, one drop and one mutation, the recon reports exactly those — and its
// Summary equals an independent NOT-EXISTS query over the raw tables.
func TestIntegrationSeam_ReconCatchesInjectedBreaks_vsDBTruth(t *testing.T) {
	f := newRecoveryFixture(t, "seam_breaks")
	ctx := context.Background()
	const n = 6
	const date = "2026-06-15"
	seamSeedClean(t, f.db.App, date, n)

	// Baseline: the seeded world reconciles perfectly BEFORE we perturb it — otherwise
	// injecting breaks into an already-broken world would prove nothing.
	base, err := f.svc.ReconcileRecoveryDay(ctx, simseed.SyntheticTelco, date)
	if err != nil {
		t.Fatalf("baseline reconcile: %v", err)
	}
	if base.Matched != n || base.MissingTelco != 0 || base.MissingPlatform != 0 || base.AmountMismatch != 0 {
		t.Fatalf("baseline is not clean: matched=%d mt=%d mp=%d am=%d (want %d,0,0,0)",
			base.Matched, base.MissingTelco, base.MissingPlatform, base.AmountMismatch, n)
	}

	// --- INJECT three KNOWN breaks by direct DB mutation (test-controlled, not seeder intent). ---
	// (1) phantom: a wh: recovery event with NO matching feed row -> MISSING_TELCO.
	if _, err := f.db.Admin.Exec(ctx, `
		INSERT INTO recovery_events (recovery_event_id, telco_id, source_event_id,
		  msisdn_token, amount_minor, currency, state, occurred_at)
		VALUES ('ev_seam_phantom','SIM_NG','wh:ev_seam_phantom','tok_seam_phantom',12345,'NGN','ALLOCATED',$1)`,
		time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("inject phantom: %v", err)
	}
	// (2) drop: a feed deduction with NO matching platform event -> MISSING_PLATFORM.
	if _, err := f.db.Admin.Exec(ctx, `
		INSERT INTO recovery_eod_feed (telco_id, business_date, msisdn_token, recovery_deducted_minor, currency)
		VALUES ('SIM_NG', DATE '2026-06-15', 'tok_seam_drop', 6789, 'NGN')`); err != nil {
		t.Fatalf("inject drop: %v", err)
	}
	// (3) mutation: bump one GENUINE seeded subscriber's feed deduction so it no longer
	// matches its platform SUM -> AMOUNT_MISMATCH. The target must have BOTH a feed row
	// AND a wh: event (a real matched subject) — so it is neither the phantom (no feed)
	// nor the drop (no event). Selecting from the feed with an EXISTS-event guard picks
	// exactly a seeded subject regardless of token ordering.
	ct, err := f.db.Admin.Exec(ctx, `
		UPDATE recovery_eod_feed SET recovery_deducted_minor = recovery_deducted_minor + 1000
		WHERE telco_id='SIM_NG' AND business_date = DATE '2026-06-15'
		  AND msisdn_token = (
		    SELECT fd.msisdn_token FROM recovery_eod_feed fd
		    WHERE fd.telco_id='SIM_NG' AND fd.business_date = DATE '2026-06-15'
		      AND EXISTS (SELECT 1 FROM recovery_events e
		                  WHERE e.telco_id='SIM_NG' AND e.source_event_id LIKE 'wh:%'
		                    AND e.msisdn_token = fd.msisdn_token)
		    ORDER BY fd.msisdn_token LIMIT 1)`)
	if err != nil {
		t.Fatalf("inject mutation: %v", err)
	}
	if ct.RowsAffected() != 1 {
		t.Fatalf("mutation must hit exactly one seeded subject, updated %d rows", ct.RowsAffected())
	}

	// --- The engine must now report exactly the injected breaks. ---
	sum, err := f.svc.ReconcileRecoveryDay(ctx, simseed.SyntheticTelco, date)
	if err != nil {
		t.Fatalf("post-injection reconcile: %v", err)
	}
	if sum.MissingTelco != 1 || sum.MissingPlatform != 1 || sum.AmountMismatch != 1 || sum.Matched != n-1 {
		t.Fatalf("engine did not catch the injected breaks: matched=%d MISSING_TELCO=%d MISSING_PLATFORM=%d AMOUNT_MISMATCH=%d (want %d,1,1,1)",
			sum.Matched, sum.MissingTelco, sum.MissingPlatform, sum.AmountMismatch, n-1)
	}

	// --- ANTI-CIRCULARITY: independent DB-truth over the RAW tables (NOT the engine). ---
	loc, err := time.LoadLocation("Africa/Lagos")
	if err != nil {
		t.Fatalf("load tz: %v", err)
	}
	winStart, winEnd, err := businessDayWindow(loc, date)
	if err != nil {
		t.Fatalf("window: %v", err)
	}
	var wantPhantom, wantDrop, wantMismatch int
	if err := f.db.Admin.QueryRow(ctx, `
		WITH ev AS (
		  SELECT msisdn_token, SUM(amount_minor) AS s
		  FROM recovery_events
		  WHERE telco_id='SIM_NG' AND source_event_id LIKE 'wh:%'
		    AND occurred_at >= $1 AND occurred_at < $2
		  GROUP BY msisdn_token
		),
		fd AS (
		  SELECT msisdn_token, recovery_deducted_minor AS d
		  FROM recovery_eod_feed
		  WHERE telco_id='SIM_NG' AND business_date = $3::date
		)
		SELECT
		  (SELECT count(*) FROM ev WHERE NOT EXISTS (SELECT 1 FROM fd WHERE fd.msisdn_token=ev.msisdn_token)),
		  (SELECT count(*) FROM fd WHERE NOT EXISTS (SELECT 1 FROM ev WHERE ev.msisdn_token=fd.msisdn_token)),
		  (SELECT count(*) FROM ev JOIN fd ON fd.msisdn_token=ev.msisdn_token WHERE ev.s <> fd.d)`,
		winStart, winEnd, date).Scan(&wantPhantom, &wantDrop, &wantMismatch); err != nil {
		t.Fatalf("independent DB-truth query: %v", err)
	}
	if sum.MissingTelco != wantPhantom || sum.MissingPlatform != wantDrop || sum.AmountMismatch != wantMismatch {
		t.Fatalf("engine Summary disagrees with independent DB truth: engine(mt=%d mp=%d am=%d) vs raw-SQL(phantom=%d drop=%d mismatch=%d)",
			sum.MissingTelco, sum.MissingPlatform, sum.AmountMismatch, wantPhantom, wantDrop, wantMismatch)
	}
}

// TestIntegrationSeam_FourEyesArmThenRunRecoveryGoesLive drives the whole armed
// pipeline: the four-eyes door admits SIM_NG (self-approve rejected, distinct approve
// accepted, armed-but-not-yet-live), then a confirmed newest settled day through the
// real RunRecovery advances freshness and makes the layer LIVE.
func TestIntegrationSeam_FourEyesArmThenRunRecoveryGoesLive(t *testing.T) {
	// Arming touches worker-owned control tables (recon_layer_arming_requests +
	// recon_layer_arming), so the arm Service runs as tcp_worker; the recon/seed
	// Service runs as tcp_app, exactly as the -recon worker job wires them.
	armSvc, db := newArmingSvc(t, "seam_armed")
	reconSvc := New(db.App, configsvc.New(db.App), slog.Default())
	ctx := context.Background()

	reqID, err := armSvc.ProposeArmRecovery(ctx, ArmProposal{
		TelcoID:                    simseed.SyntheticTelco,
		ProposedBy:                 "maker@seam",
		Reason:                     "integration seam",
		ConfirmedFeedReversalBasis: "NET_SAME_DAY",
		ConfirmedBusinessDateBasis: "occurred_at_lagos_date",
	})
	if err != nil {
		t.Fatalf("ProposeArmRecovery: %v", err)
	}
	// Self-approval is rejected by the two-person rule.
	if err := armSvc.ApproveArmRecovery(ctx, reqID, "maker@seam"); !errors.Is(err, ErrArmSelfApprove) {
		t.Fatalf("self-approve must be rejected, got %v", err)
	}
	// A distinct checker approves.
	if err := armSvc.ApproveArmRecovery(ctx, reqID, "checker@seam"); err != nil {
		t.Fatalf("ApproveArmRecovery (distinct): %v", err)
	}
	// Armed, but NOT live until a confirmed recon proves the feed fresh.
	if armed, _ := armSvc.Arming.IsLayerArmed(ctx, simseed.SyntheticTelco, repo.ReconLayerRecovery); !armed {
		t.Fatal("telco must be armed after four-eyes approval")
	}
	if live, _ := armSvc.Arming.IsLayerLive(ctx, simseed.SyntheticTelco, repo.ReconLayerRecovery); live {
		t.Fatal("armed-but-never-reconciled layer must NOT be live yet (freshness coupling)")
	}

	// Seed the NEWEST settled Lagos business day (what RunRecovery treats as newest, so
	// its confirmation drives the freshness gate) and run the real armed pipeline.
	loc, err := time.LoadLocation("Africa/Lagos")
	if err != nil {
		t.Fatalf("load tz: %v", err)
	}
	nowLag := time.Now().UTC().Add(-1 * time.Hour) // matches recon_lag_seconds=3600
	day, ok := lastSettledBusinessDay(loc, nowLag)
	if !ok {
		t.Fatal("no settled business day available")
	}
	seamSeedClean(t, db.App, day, 5)

	if _, err := reconSvc.RunRecovery(ctx, simseed.SyntheticTelco); err != nil {
		t.Fatalf("RunRecovery: %v", err)
	}
	// A confirmed newest settled day advances freshness -> the armed layer is now LIVE
	// (the sole satisfier of the S2 recharge-webhook gate).
	if live, _ := armSvc.Arming.IsLayerLive(ctx, simseed.SyntheticTelco, repo.ReconLayerRecovery); !live {
		t.Fatal("a confirmed newest settled day must make the armed RECOVERY layer live")
	}
}
