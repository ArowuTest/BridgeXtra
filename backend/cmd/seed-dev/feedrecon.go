package main

// Feed/recon state driver for cmd/seed-dev (-feed-recon). It populates the states a
// Wave B feed-health dashboard renders, entirely via the real engine — it NEVER writes
// a recon OUTPUT row (recon_runs / recon_items):
//
//   - A reconciliation BREAK the engine catches vs DB truth: seed a clean recovery day
//     (SeedCohort + SeedRecoveryDay — both sides derived from one ground truth), then
//     perturb the RAW INGEST tables (recovery_events = platform truth, recovery_eod_feed
//     = telco feed — the exact #70 integration path) with a phantom, a drop and an amount
//     mismatch, then run the REAL recon.ReconcileRecoveryDay so the ENGINE records the
//     breaks in recon_items. Writing the ingest tables is legitimate injection; writing
//     recon_items/recon_runs would be fabrication — we never do that.
//   - The RECOVERY layer STALE: re-arm without a confirming recon run so last_recon_at is
//     NULL (armed-but-never-confirmed) => IsLayerLive=false => the dashboard reads STALE.
//
// Runs AFTER the held-recharge slice (which needs the layer LIVE to accept webhooks): the
// recharges are created while live and persist, then the feed reads stale — held-recharge
// RELEASE does not depend on the layer being live, so this does not regress slice 2.
// Idempotent: deterministic seed + ON CONFLICT DO NOTHING injections + SetDown/SetLive.

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/simseed"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/recon"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	feedSeed          = "seed-dev-feed"
	feedCleanCount    = 6
	feedMismatchEvent = int64(50000) // platform SUM for the mismatch token
	feedMismatchFeed  = int64(51000) // telco feed control-total (> event => AMOUNT_MISMATCH)
)

func runFeedRecon(ctx context.Context, adminPool, appPool *pgxpool.Pool) error {
	telco := simseed.SyntheticTelco // "SIM_NG"

	// A settled past Lagos business day (yesterday) for the recon day.
	yst := time.Now().In(lagosLoc()).AddDate(0, 0, -1)
	businessDate := yst.Format("2006-01-02")
	y, mo, d := yst.Date()
	occurredAt := time.Date(y, mo, d, 12, 0, 0, 0, lagosLoc())

	// 1. Clean baseline day (both sides from one ground truth) so injected breaks land in
	// an all-MATCHED world — via the RLS app pool, exactly as cmd/simseed runs it.
	if _, err := simseed.SeedCohort(ctx, appPool, simseed.CohortPlan{Seed: feedSeed, Count: feedCleanCount}); err != nil {
		return fmt.Errorf("seed cohort: %w", err)
	}
	if _, err := simseed.SeedRecoveryDay(ctx, appPool, simseed.RecoveryDayPlan{Seed: feedSeed, BusinessDate: businessDate, Count: feedCleanCount}); err != nil {
		return fmt.Errorf("seed recovery day: %w", err)
	}

	// 2. Inject three breaks into the RAW INGEST tables (dedicated tokens, idempotent).
	if err := injectBreaks(ctx, adminPool, telco, businessDate, occurredAt); err != nil {
		return err
	}

	// 3. Run the REAL recon engine — it catches the breaks vs DB truth and writes
	// recon_runs + recon_items itself.
	svc := recon.New(appPool, configsvc.New(appPool), seedLogger())
	sum, err := svc.ReconcileRecoveryDay(ctx, telco, businessDate)
	if err != nil {
		return fmt.Errorf("reconcile recovery day %s: %w", businessDate, err)
	}
	log.Printf("seed-dev: recon %s — matched=%d missing_platform=%d missing_telco=%d amount_mismatch=%d",
		businessDate, sum.Matched, sum.MissingPlatform, sum.MissingTelco, sum.AmountMismatch)

	// 4. Leave the RECOVERY layer STALE: disarm then re-arm without a confirming run, so
	// last_recon_at is NULL (armed-never-confirmed) => IsLayerLive=false => STALE.
	arm := &repo.ReconArming{Pool: adminPool}
	if err := arm.SetDown(ctx, telco, repo.ReconLayerRecovery); err != nil {
		return fmt.Errorf("disarm recovery: %w", err)
	}
	if err := arm.SetLive(ctx, telco, repo.ReconLayerRecovery); err != nil {
		return fmt.Errorf("re-arm recovery: %w", err)
	}
	log.Print("seed-dev: RECOVERY layer left STALE (armed, never confirmed) + recon break inventory populated.")
	return nil
}

// injectBreaks perturbs the raw ingest tables to create one break of each class. The
// tokens are dedicated (not seeded subjects) and ON CONFLICT DO NOTHING, so re-runs add
// nothing. This is the #70 pattern: the engine, not the seeder, records the breaks.
func injectBreaks(ctx context.Context, adminPool *pgxpool.Pool, telco, businessDate string, occurredAt time.Time) error {
	// phantom (MISSING_TELCO): a platform event with no matching feed row.
	if _, err := adminPool.Exec(ctx, `
		INSERT INTO recovery_events (recovery_event_id, telco_id, source_event_id, msisdn_token, amount_minor, currency, state, occurred_at)
		VALUES ('ev_dev_phantom', $1, 'wh:ev_dev_phantom', 'tok_dev_phantom', 12345, 'NGN', 'ALLOCATED', $2)
		ON CONFLICT (recovery_event_id) DO NOTHING`, telco, occurredAt); err != nil {
		return fmt.Errorf("inject phantom: %w", err)
	}
	// drop (MISSING_PLATFORM): a feed deduction with no matching platform event.
	if _, err := adminPool.Exec(ctx, `
		INSERT INTO recovery_eod_feed (telco_id, business_date, msisdn_token, recovery_deducted_minor, currency)
		VALUES ($1, $2::date, 'tok_dev_drop', 6789, 'NGN')
		ON CONFLICT (telco_id, business_date, msisdn_token) DO NOTHING`, telco, businessDate); err != nil {
		return fmt.Errorf("inject drop: %w", err)
	}
	// mismatch (AMOUNT_MISMATCH): same token on both sides, feed total != platform SUM.
	if _, err := adminPool.Exec(ctx, `
		INSERT INTO recovery_events (recovery_event_id, telco_id, source_event_id, msisdn_token, amount_minor, currency, state, occurred_at)
		VALUES ('ev_dev_mismatch', $1, 'wh:ev_dev_mismatch', 'tok_dev_mismatch', $2, 'NGN', 'ALLOCATED', $3)
		ON CONFLICT (recovery_event_id) DO NOTHING`, telco, feedMismatchEvent, occurredAt); err != nil {
		return fmt.Errorf("inject mismatch event: %w", err)
	}
	if _, err := adminPool.Exec(ctx, `
		INSERT INTO recovery_eod_feed (telco_id, business_date, msisdn_token, recovery_deducted_minor, currency)
		VALUES ($1, $2::date, 'tok_dev_mismatch', $3, 'NGN')
		ON CONFLICT (telco_id, business_date, msisdn_token) DO NOTHING`, telco, businessDate, feedMismatchFeed); err != nil {
		return fmt.Errorf("inject mismatch feed: %w", err)
	}
	return nil
}

// lagosLoc is Africa/Lagos (WAT, UTC+1, no DST); falls back to a fixed +1 zone if the
// tzdata is unavailable in the runtime image.
func lagosLoc() *time.Location {
	if loc, err := time.LoadLocation("Africa/Lagos"); err == nil {
		return loc
	}
	return time.FixedZone("WAT", 1*60*60)
}
