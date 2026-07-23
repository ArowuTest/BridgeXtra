package repo

// ScoringCycles — the durable scoring scheduler's per-(programme, cycle) claim
// ledger. The claim is a LEASE claim-or-reclaim (not a fire-once lock): a crash
// or FAILED cycle must not remove that cycle from the fleet, but a SUCCEEDED or
// STALE_NO_REFRESH cycle is never re-run. cycle_key is computed from the DATABASE
// clock on the effective-cadence grid so instances with skewed wall clocks still
// agree on which bucket they are claiming.

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
)

type ScoringCycles struct{}

// ClaimOrReclaim atomically claims the CURRENT cycle for (telco, programme) on
// the effective-cadence grid (window = effectiveCadenceHours*3600 seconds), or
// reclaims it if the prior holder FAILED or let its lease expire. It returns
// claimed=false (all other outs zero) when the current cycle is already
// SUCCEEDED / STALE_NO_REFRESH, or still CLAIMED under a live lease elsewhere —
// the caller then simply skips this tick.
//
// On a reclaim the previously bound feature_file_id is PRESERVED and returned so
// the caller reuses the same file: scoringrun is idempotent per
// (feature_file_id, policy_version_id, programme_id) and will replay/complete
// rather than mint a second run.
func (ScoringCycles) ClaimOrReclaim(ctx context.Context, tx pgx.Tx, telcoID, programmeID, claimedBy string, windowSeconds, effectiveCadenceHours, leaseSeconds int) (claimed bool, cycleID, boundFileID string, attempts int, err error) {
	err = tx.QueryRow(ctx, `
		INSERT INTO scoring_schedule_cycles
		  (cycle_id, telco_id, programme_id, cycle_key, status, claimed_by, claimed_at, attempts, effective_cadence_hours)
		VALUES
		  ($1, $2, $3,
		   to_timestamp(floor(extract(epoch from now())/$4::float8) * $4::float8),
		   'CLAIMED', $5, now(), 1, $6)
		ON CONFLICT (telco_id, programme_id, cycle_key) DO UPDATE
		  SET status          = 'CLAIMED',
		      claimed_by      = $5,
		      claimed_at      = now(),
		      attempts        = scoring_schedule_cycles.attempts + 1,
		      scoring_run_id  = NULL,
		      subjects_scored = NULL,
		      finished_at     = NULL,
		      error           = NULL
		  WHERE scoring_schedule_cycles.status = 'FAILED'
		     OR (scoring_schedule_cycles.status = 'CLAIMED'
		         AND scoring_schedule_cycles.claimed_at < now() - make_interval(secs => $7))
		RETURNING cycle_id, COALESCE(feature_file_id, ''), attempts`,
		platform.NewID("cyc"), telcoID, programmeID, windowSeconds, claimedBy, effectiveCadenceHours, leaseSeconds,
	).Scan(&cycleID, &boundFileID, &attempts)
	if err == pgx.ErrNoRows {
		// Conflict row exists but is SUCCEEDED / STALE_NO_REFRESH / freshly CLAIMED
		// elsewhere — nothing to do this tick.
		return false, "", "", 0, nil
	}
	if err != nil {
		return false, "", "", 0, err
	}
	return true, cycleID, boundFileID, attempts, nil
}

// BindFeatureFile records the feature file the cycle ingested, written BEFORE
// scoring so a reclaim after a mid-score crash reuses it (no re-ingest).
func (ScoringCycles) BindFeatureFile(ctx context.Context, tx pgx.Tx, cycleID, featureFileID string) error {
	_, err := tx.Exec(ctx,
		`UPDATE scoring_schedule_cycles SET feature_file_id=$2 WHERE cycle_id=$1`, cycleID, featureFileID)
	return err
}

// MarkSucceeded closes the cycle: fresh decisions are on file.
func (ScoringCycles) MarkSucceeded(ctx context.Context, tx pgx.Tx, cycleID, scoringRunID string, subjectsScored int) error {
	_, err := tx.Exec(ctx, `
		UPDATE scoring_schedule_cycles
		SET status='SUCCEEDED', scoring_run_id=$2, subjects_scored=$3, finished_at=now(), error=NULL
		WHERE cycle_id=$1`, cycleID, scoringRunID, subjectsScored)
	return err
}

// MarkStaleNoRefresh closes the cycle as a feed-staleness incident: the run was a
// replay (no new upstream data) and the current decisions are near expiry.
func (ScoringCycles) MarkStaleNoRefresh(ctx context.Context, tx pgx.Tx, cycleID, scoringRunID string) error {
	_, err := tx.Exec(ctx, `
		UPDATE scoring_schedule_cycles
		SET status='STALE_NO_REFRESH', scoring_run_id=$2, finished_at=now()
		WHERE cycle_id=$1`, cycleID, scoringRunID)
	return err
}

// MarkFailed parks the cycle FAILED (re-claimable within max_attempts).
func (ScoringCycles) MarkFailed(ctx context.Context, tx pgx.Tx, cycleID, errMsg string) error {
	_, err := tx.Exec(ctx, `
		UPDATE scoring_schedule_cycles
		SET status='FAILED', error=$2, finished_at=now()
		WHERE cycle_id=$1`, cycleID, errMsg)
	return err
}

// FreshestValidUntil returns the latest valid_until among the programme's CURRENT
// scored decisions (ok=false when the programme has none yet). The scheduler uses
// it to decide, on a no-refresh replay cycle, whether decisions are about to
// expire — the STALE_NO_REFRESH signal.
func (ScoringCycles) FreshestValidUntil(ctx context.Context, tx pgx.Tx, programmeID string) (time.Time, bool, error) {
	var vu *time.Time
	if err := tx.QueryRow(ctx, `
		SELECT max(d.valid_until)
		FROM decision_snapshots d
		JOIN scoring_runs r ON r.scoring_run_id = d.scoring_run_id
		WHERE r.programme_id = $1 AND d.is_current AND d.valid_until IS NOT NULL`,
		programmeID).Scan(&vu); err != nil {
		return time.Time{}, false, err
	}
	if vu == nil {
		return time.Time{}, false, nil
	}
	return *vu, true, nil
}
