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
	CurrentRunID     string // scoring run that produced the current decision ('' if none)
}

// ListSubjectsByFile returns the file's snapshots joined with subscriber
// status and current-decision tier, keyset-paged by snapshot id.
func (FeatureSnapshots) ListSubjectsByFile(ctx context.Context, tx pgx.Tx, fileID, afterID string, limit int) ([]SnapshotSubject, error) {
	rows, err := tx.Query(ctx, `
		SELECT fs.feature_snapshot_id, fs.telco_id, fs.subscriber_account_id, fs.feature_file_id,
		       fs.as_of, fs.features, fs.quality, fs.content_hash, fs.created_at,
		       sa.status, COALESCE(d.tier_code, ''), COALESCE(d.scoring_run_id, '')
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
			&s.Snapshot.CreatedAt, &s.SubscriberStatus, &s.PriorTierCode, &s.CurrentRunID); err != nil {
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

// BulkInsertScored is the set-based twin of InsertScored for batch runs
// (owner rule: scale = specialized repo methods): ONE set-based close of the
// subjects' current decisions + ONE CopyFrom of the new rows per page,
// instead of two statements per subject. Same invariants — the partial
// unique decision_current_uq still arbitrates.
func (Decisions) BulkInsertScored(ctx context.Context, tx pgx.Tx, ds []entity.DecisionSnapshot) error {
	if len(ds) == 0 {
		return nil
	}
	subjects := make([]string, len(ds))
	for i, d := range ds {
		subjects[i] = d.SubscriberAccountID
	}
	if _, err := tx.Exec(ctx, `
		UPDATE decision_snapshots SET is_current = false
		WHERE subscriber_account_id = ANY($1) AND is_current`, subjects); err != nil {
		return fmt.Errorf("bulk close current decisions: %w", err)
	}
	rows := make([][]any, len(ds))
	for i, d := range ds {
		rows[i] = []any{d.DecisionSnapshotID, d.TelcoID, d.SubscriberAccountID,
			d.MaxFaceValue.Amount(), string(d.MaxFaceValue.Currency()), true,
			d.ConfigVersionID, d.TierCode, d.ReasonCodes, d.FeatureSnapshotID,
			d.ScoringRunID, d.ValidUntil, d.DecisionHash, d.DecisionDoc,
			d.PriorTierCode, d.ScoredAt}
	}
	// COPY cannot target an RLS-enforced table (0A000) — stage into a temp
	// table (no RLS), then INSERT..SELECT so the WITH CHECK policy applies
	// row-by-row exactly as for single inserts.
	if _, err := tx.Exec(ctx, `
		CREATE TEMP TABLE IF NOT EXISTS _dec_stage
		  (decision_snapshot_id TEXT, telco_id TEXT, subscriber_account_id TEXT,
		   max_face_value_minor BIGINT, currency CHAR(3), is_current BOOLEAN,
		   config_version_id TEXT, tier_code TEXT, reason_codes JSONB,
		   feature_snapshot_id TEXT, scoring_run_id TEXT, valid_until TIMESTAMPTZ,
		   decision_hash TEXT, decision_doc JSONB, prior_tier_code TEXT,
		   scored_at TIMESTAMPTZ) ON COMMIT DROP`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `TRUNCATE _dec_stage`); err != nil {
		return err
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"_dec_stage"},
		[]string{"decision_snapshot_id", "telco_id", "subscriber_account_id",
			"max_face_value_minor", "currency", "is_current", "config_version_id",
			"tier_code", "reason_codes", "feature_snapshot_id", "scoring_run_id",
			"valid_until", "decision_hash", "decision_doc", "prior_tier_code", "scored_at"},
		pgx.CopyFromRows(rows)); err != nil {
		return fmt.Errorf("stage scored decisions: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO decision_snapshots
		  (decision_snapshot_id, telco_id, subscriber_account_id, max_face_value_minor,
		   currency, is_current, config_version_id, tier_code, reason_codes,
		   feature_snapshot_id, scoring_run_id, valid_until, decision_hash,
		   decision_doc, prior_tier_code, scored_at)
		SELECT decision_snapshot_id, telco_id, subscriber_account_id, max_face_value_minor,
		       currency, is_current, config_version_id, tier_code, reason_codes,
		       feature_snapshot_id, scoring_run_id, valid_until, decision_hash,
		       decision_doc, prior_tier_code, scored_at
		FROM _dec_stage`); err != nil {
		return fmt.Errorf("bulk insert scored decisions: %w", err)
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
