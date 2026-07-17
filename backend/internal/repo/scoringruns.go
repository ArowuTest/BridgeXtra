package repo

// Scoring-run repositories (M2c). The run is idempotent at the schema:
// UNIQUE(feature_file_id, policy_version_id, programme_id) — re-running the
// same inputs resumes/replays, it never double-scores.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
)

var ErrDuplicateRun = errors.New("scoring run already exists for these inputs")

type ScoringRuns struct{}

// Insert creates a run; a (file, policy, programme) duplicate returns
// ErrDuplicateRun with the existing run, so callers resume rather than fork.
func (ScoringRuns) Insert(ctx context.Context, tx pgx.Tx, r entity.ScoringRun) (entity.ScoringRun, error) {
	ct, err := tx.Exec(ctx, `
		INSERT INTO scoring_runs
		  (scoring_run_id, telco_id, programme_id, feature_file_id, policy_version_id, status, subjects_total)
		VALUES ($1,$2,$3,$4,$5,'RUNNING',$6)
		ON CONFLICT (feature_file_id, policy_version_id, programme_id) DO NOTHING`,
		r.ScoringRunID, r.TelcoID, r.ProgrammeID, r.FeatureFileID, r.PolicyVersionID, r.SubjectsTotal)
	if err != nil {
		return r, fmt.Errorf("insert scoring run: %w", err)
	}
	if ct.RowsAffected() == 1 {
		return (ScoringRuns{}).getByInputs(ctx, tx, r.FeatureFileID, r.PolicyVersionID, r.ProgrammeID, nil)
	}
	existing, err := (ScoringRuns{}).getByInputs(ctx, tx, r.FeatureFileID, r.PolicyVersionID, r.ProgrammeID, nil)
	if err != nil {
		return r, err
	}
	return existing, ErrDuplicateRun
}

func (ScoringRuns) getByInputs(ctx context.Context, tx pgx.Tx, fileID, policyID, programmeID string, _ any) (entity.ScoringRun, error) {
	var r entity.ScoringRun
	err := tx.QueryRow(ctx, `
		SELECT scoring_run_id, telco_id, programme_id, feature_file_id, policy_version_id,
		       status, subjects_total, subjects_scored, subjects_skipped, started_at, completed_at
		FROM scoring_runs
		WHERE feature_file_id = $1 AND policy_version_id = $2 AND programme_id = $3`,
		fileID, policyID, programmeID).
		Scan(&r.ScoringRunID, &r.TelcoID, &r.ProgrammeID, &r.FeatureFileID, &r.PolicyVersionID,
			&r.Status, &r.SubjectsTotal, &r.SubjectsScored, &r.SubjectsSkipped, &r.StartedAt, &r.CompletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, fmt.Errorf("scoring run: %w", ErrNotFound)
	}
	return r, err
}

// Progress adds to the run's control totals (called per committed batch).
func (ScoringRuns) Progress(ctx context.Context, tx pgx.Tx, runID string, scored, skipped int) error {
	ct, err := tx.Exec(ctx, `
		UPDATE scoring_runs
		SET subjects_scored = subjects_scored + $2, subjects_skipped = subjects_skipped + $3
		WHERE scoring_run_id = $1 AND status = 'RUNNING'`, runID, scored, skipped)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("scoring run %q not RUNNING: %w", runID, ErrNotFound)
	}
	return nil
}

// Complete finalises the run.
func (ScoringRuns) Complete(ctx context.Context, tx pgx.Tx, runID, status string) error {
	ct, err := tx.Exec(ctx, `
		UPDATE scoring_runs SET status = $2, completed_at = now()
		WHERE scoring_run_id = $1 AND status = 'RUNNING'`, runID, status)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("scoring run %q not RUNNING: %w", runID, ErrNotFound)
	}
	return nil
}

// SnapshotSubject pairs a feature snapshot with its subscriber's live status
// and prior tier for one scoring pass (single query per page — the batch
// scorer never does N+1 point reads).
type SnapshotSubject struct {
	Snapshot         entity.FeatureSnapshot
	SubscriberStatus string
	PriorTierCode    string // '' when no current scored/seed decision
}

// ListSubjectsByFile returns the file's snapshots joined with subscriber
// status and current-decision tier, keyset-paged by snapshot id.
func (FeatureSnapshots) ListSubjectsByFile(ctx context.Context, tx pgx.Tx, fileID, afterID string, limit int) ([]SnapshotSubject, error) {
	rows, err := tx.Query(ctx, `
		SELECT fs.feature_snapshot_id, fs.telco_id, fs.subscriber_account_id, fs.feature_file_id,
		       fs.as_of, fs.features, fs.quality, fs.content_hash, fs.created_at,
		       sa.status, COALESCE(d.tier_code, '')
		FROM feature_snapshots fs
		JOIN subscriber_accounts sa ON sa.subscriber_account_id = fs.subscriber_account_id
		LEFT JOIN decision_snapshots d ON d.subscriber_account_id = fs.subscriber_account_id AND d.is_current
		WHERE fs.feature_file_id = $1 AND fs.feature_snapshot_id > $2
		ORDER BY fs.feature_snapshot_id
		LIMIT $3`, fileID, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SnapshotSubject
	for rows.Next() {
		var s SnapshotSubject
		if err := rows.Scan(&s.Snapshot.FeatureSnapshotID, &s.Snapshot.TelcoID,
			&s.Snapshot.SubscriberAccountID, &s.Snapshot.FeatureFileID, &s.Snapshot.AsOf,
			&s.Snapshot.Features, &s.Snapshot.Quality, &s.Snapshot.ContentHash,
			&s.Snapshot.CreatedAt, &s.SubscriberStatus, &s.PriorTierCode); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// InsertScored writes a scored decision and makes it current in one step:
// the previous current row (if any) is closed first, so decision_current_uq
// arbitrates concurrent scorers — second writer errors, never two currents.
func (Decisions) InsertScored(ctx context.Context, tx pgx.Tx, d entity.DecisionSnapshot) error {
	if _, err := tx.Exec(ctx, `
		UPDATE decision_snapshots SET is_current = false
		WHERE subscriber_account_id = $1 AND is_current`, d.SubscriberAccountID); err != nil {
		return fmt.Errorf("close current decision: %w", err)
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO decision_snapshots
		  (decision_snapshot_id, telco_id, subscriber_account_id, max_face_value_minor, currency,
		   is_current, config_version_id, tier_code, reason_codes, feature_snapshot_id,
		   scoring_run_id, valid_until, decision_hash, decision_doc, prior_tier_code, scored_at)
		VALUES ($1,$2,$3,$4,$5,true,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		d.DecisionSnapshotID, d.TelcoID, d.SubscriberAccountID,
		d.MaxFaceValue.Amount(), string(d.MaxFaceValue.Currency()),
		d.ConfigVersionID, d.TierCode, d.ReasonCodes, d.FeatureSnapshotID,
		d.ScoringRunID, d.ValidUntil, d.DecisionHash, d.DecisionDoc, d.PriorTierCode, d.ScoredAt)
	if err != nil {
		return fmt.Errorf("insert scored decision: %w", err)
	}
	return nil
}

// ListScoredByRun returns the decisions a run produced (replay iterates these),
// keyset-paged.
func (Decisions) ListScoredByRun(ctx context.Context, tx pgx.Tx, runID, afterID string, limit int) ([]entity.DecisionSnapshot, error) {
	rows, err := tx.Query(ctx, `
		SELECT decision_snapshot_id, telco_id, subscriber_account_id, max_face_value_minor, currency,
		       is_current, config_version_id, tier_code, feature_snapshot_id, scoring_run_id,
		       valid_until, decision_hash, decision_doc, prior_tier_code, scored_at, created_at
		FROM decision_snapshots
		WHERE scoring_run_id = $1 AND decision_snapshot_id > $2
		ORDER BY decision_snapshot_id
		LIMIT $3`, runID, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []entity.DecisionSnapshot
	for rows.Next() {
		var d entity.DecisionSnapshot
		var minor int64
		var cur string
		if err := rows.Scan(&d.DecisionSnapshotID, &d.TelcoID, &d.SubscriberAccountID, &minor, &cur,
			&d.IsCurrent, &d.ConfigVersionID, &d.TierCode, &d.FeatureSnapshotID, &d.ScoringRunID,
			&d.ValidUntil, &d.DecisionHash, &d.DecisionDoc, &d.PriorTierCode, &d.ScoredAt, &d.CreatedAt); err != nil {
			return nil, err
		}
		m, err := scanMoney(minor, cur)
		if err != nil {
			return nil, err
		}
		d.MaxFaceValue = m
		out = append(out, d)
	}
	return out, rows.Err()
}
