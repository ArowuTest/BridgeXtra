package repo

// Feature-store repositories (M2b, V2-SCR-001/002). All methods run inside a
// tenant transaction — RLS is the isolation boundary. Idempotency lives in
// the schema: feature_files UNIQUE(telco_id, content_hash) makes re-ingesting
// a file a recorded no-op; feature_snapshots UNIQUE(subscriber, as_of) makes
// a resumed partial ingest converge instead of double-writing.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
)

var ErrDuplicateFile = errors.New("feature file already ingested")

type FeatureFiles struct{}

// Insert records a new file. A content-hash duplicate returns
// ErrDuplicateFile with the existing file id — the caller reports "already
// ingested", it never re-processes. ON CONFLICT DO NOTHING keeps the
// transaction alive on the duplicate path (a plain failed INSERT would abort
// it — reference: the aborted-tx trap).
func (FeatureFiles) Insert(ctx context.Context, tx pgx.Tx, f entity.FeatureFile) (existingID string, err error) {
	ct, err := tx.Exec(ctx, `
		INSERT INTO feature_files
		  (feature_file_id, telco_id, source, as_of, content_hash, row_count, quarantined_rows, status)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (telco_id, content_hash) DO NOTHING`,
		f.FeatureFileID, f.TelcoID, f.Source, f.AsOf, f.ContentHash,
		f.RowCount, f.QuarantinedRows, f.Status)
	if err != nil {
		return "", fmt.Errorf("insert feature file: %w", err)
	}
	if ct.RowsAffected() == 1 {
		return "", nil
	}
	var id string
	if err := tx.QueryRow(ctx, `
		SELECT feature_file_id FROM feature_files
		WHERE telco_id = $1 AND content_hash = $2`, f.TelcoID, f.ContentHash).Scan(&id); err != nil {
		return "", fmt.Errorf("resolve duplicate feature file: %w", err)
	}
	return id, ErrDuplicateFile
}

// Finalize records the ingest control totals (row/quarantine counts, status).
func (FeatureFiles) Finalize(ctx context.Context, tx pgx.Tx, fileID string, rows, quarantined int, status string) error {
	ct, err := tx.Exec(ctx, `
		UPDATE feature_files SET row_count = $2, quarantined_rows = $3, status = $4
		WHERE feature_file_id = $1`, fileID, rows, quarantined, status)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("feature file %q: %w", fileID, ErrNotFound)
	}
	return nil
}

type FeatureSnapshots struct{}

// Upsert writes one subscriber's snapshot for an as-of cut. A duplicate
// (subscriber, as_of) is left untouched — the first write wins and a resumed
// ingest of the same file converges (content is deterministic per file).
// Returns whether a row was written.
func (FeatureSnapshots) Upsert(ctx context.Context, tx pgx.Tx, s entity.FeatureSnapshot) (bool, error) {
	ct, err := tx.Exec(ctx, `
		INSERT INTO feature_snapshots
		  (feature_snapshot_id, telco_id, subscriber_account_id, feature_file_id,
		   as_of, features, quality, content_hash)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (subscriber_account_id, as_of) DO NOTHING`,
		s.FeatureSnapshotID, s.TelcoID, s.SubscriberAccountID, s.FeatureFileID,
		s.AsOf, s.Features, s.Quality, s.ContentHash)
	if err != nil {
		return false, fmt.Errorf("insert feature snapshot: %w", err)
	}
	return ct.RowsAffected() == 1, nil
}

// LatestForSubscriber returns the newest snapshot for a subscriber (scoring
// hot path; covered by the UNIQUE(subscriber_account_id, as_of) index).
func (FeatureSnapshots) LatestForSubscriber(ctx context.Context, tx pgx.Tx, subscriberAccountID string) (entity.FeatureSnapshot, error) {
	var s entity.FeatureSnapshot
	err := tx.QueryRow(ctx, `
		SELECT feature_snapshot_id, telco_id, subscriber_account_id, feature_file_id,
		       as_of, features, quality, content_hash, created_at
		FROM feature_snapshots
		WHERE subscriber_account_id = $1
		ORDER BY as_of DESC LIMIT 1`, subscriberAccountID).
		Scan(&s.FeatureSnapshotID, &s.TelcoID, &s.SubscriberAccountID, &s.FeatureFileID,
			&s.AsOf, &s.Features, &s.Quality, &s.ContentHash, &s.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, fmt.Errorf("feature snapshot for %q: %w", subscriberAccountID, ErrNotFound)
	}
	return s, err
}

// Get returns one snapshot by id (the replay path reads the EXACT snapshot a
// decision pinned, never "the latest").
func (FeatureSnapshots) Get(ctx context.Context, tx pgx.Tx, snapshotID string) (entity.FeatureSnapshot, error) {
	var s entity.FeatureSnapshot
	err := tx.QueryRow(ctx, `
		SELECT feature_snapshot_id, telco_id, subscriber_account_id, feature_file_id,
		       as_of, features, quality, content_hash, created_at
		FROM feature_snapshots WHERE feature_snapshot_id = $1`, snapshotID).
		Scan(&s.FeatureSnapshotID, &s.TelcoID, &s.SubscriberAccountID, &s.FeatureFileID,
			&s.AsOf, &s.Features, &s.Quality, &s.ContentHash, &s.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, fmt.Errorf("feature snapshot %q: %w", snapshotID, ErrNotFound)
	}
	return s, err
}

// BulkEnsureByToken is the set-based twin of EnsureByToken (owner rule:
// "scale" = specialized repo methods, never bypassing the layer). One staging
// COPY + two set-based statements per chunk instead of 3 round trips per row.
// Returns token -> subscriber_account_id for every requested token.
func (Subscribers) BulkEnsureByToken(ctx context.Context, tx pgx.Tx, telcoID string, tokens []string, newIDs []string) (map[string]string, error) {
	if len(tokens) != len(newIDs) {
		return nil, fmt.Errorf("tokens and newIDs must pair")
	}
	if _, err := tx.Exec(ctx, `
		CREATE TEMP TABLE IF NOT EXISTS _sub_stage
		  (msisdn_token TEXT, new_id TEXT) ON COMMIT DROP`); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `TRUNCATE _sub_stage`); err != nil {
		return nil, err
	}
	rows := make([][]any, len(tokens))
	for i := range tokens {
		rows[i] = []any{tokens[i], newIDs[i]}
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"_sub_stage"},
		[]string{"msisdn_token", "new_id"}, pgx.CopyFromRows(rows)); err != nil {
		return nil, fmt.Errorf("stage subscribers: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO subscriber_accounts (subscriber_account_id, telco_id, msisdn_token, status)
		SELECT s.new_id, $1, s.msisdn_token, 'ACTIVE' FROM _sub_stage s
		ON CONFLICT DO NOTHING`, telcoID); err != nil {
		return nil, fmt.Errorf("bulk create subscribers: %w", err)
	}
	out := make(map[string]string, len(tokens))
	res, err := tx.Query(ctx, `
		SELECT sa.msisdn_token, sa.subscriber_account_id
		FROM subscriber_accounts sa
		JOIN _sub_stage s ON s.msisdn_token = sa.msisdn_token
		WHERE sa.effective_to IS NULL`)
	if err != nil {
		return nil, err
	}
	defer res.Close()
	for res.Next() {
		var token, id string
		if err := res.Scan(&token, &id); err != nil {
			return nil, err
		}
		out[token] = id
	}
	return out, res.Err()
}

// BulkUpsert is the set-based twin of Upsert: one staging COPY + one
// INSERT..SELECT ON CONFLICT DO NOTHING per chunk. Returns rows written.
func (FeatureSnapshots) BulkUpsert(ctx context.Context, tx pgx.Tx, snaps []entity.FeatureSnapshot) (int64, error) {
	if len(snaps) == 0 {
		return 0, nil
	}
	if _, err := tx.Exec(ctx, `
		CREATE TEMP TABLE IF NOT EXISTS _ftr_stage
		  (feature_snapshot_id TEXT, telco_id TEXT, subscriber_account_id TEXT,
		   feature_file_id TEXT, as_of TIMESTAMPTZ, features JSONB, quality JSONB,
		   content_hash TEXT) ON COMMIT DROP`); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `TRUNCATE _ftr_stage`); err != nil {
		return 0, err
	}
	rows := make([][]any, len(snaps))
	for i, s := range snaps {
		rows[i] = []any{s.FeatureSnapshotID, s.TelcoID, s.SubscriberAccountID,
			s.FeatureFileID, s.AsOf, s.Features, s.Quality, s.ContentHash}
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"_ftr_stage"},
		[]string{"feature_snapshot_id", "telco_id", "subscriber_account_id",
			"feature_file_id", "as_of", "features", "quality", "content_hash"},
		pgx.CopyFromRows(rows)); err != nil {
		return 0, fmt.Errorf("stage snapshots: %w", err)
	}
	ct, err := tx.Exec(ctx, `
		INSERT INTO feature_snapshots
		  (feature_snapshot_id, telco_id, subscriber_account_id, feature_file_id,
		   as_of, features, quality, content_hash)
		SELECT feature_snapshot_id, telco_id, subscriber_account_id, feature_file_id,
		       as_of, features, quality, content_hash
		FROM _ftr_stage
		ON CONFLICT (subscriber_account_id, as_of) DO NOTHING`)
	if err != nil {
		return 0, fmt.Errorf("bulk upsert snapshots: %w", err)
	}
	return ct.RowsAffected(), nil
}

// EnsureByToken resolves a live subscriber account for a token, creating one
// when the telco's feature file introduces a subscriber the platform has not
// seen (the subscriber base arrives through the data feed before any advance).
func (Subscribers) EnsureByToken(ctx context.Context, tx pgx.Tx, telcoID, msisdnToken, newID string) (entity.SubscriberAccount, error) {
	s, err := (Subscribers{}).GetLiveByToken(ctx, tx, msisdnToken)
	if err == nil {
		return s, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return s, err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO subscriber_accounts (subscriber_account_id, telco_id, msisdn_token, status)
		VALUES ($1,$2,$3,'ACTIVE')
		ON CONFLICT DO NOTHING`, newID, telcoID, msisdnToken)
	if err != nil {
		return s, fmt.Errorf("create subscriber for token: %w", err)
	}
	// Re-read (covers the concurrent-creator race: whoever won, we return it).
	return (Subscribers{}).GetLiveByToken(ctx, tx, msisdnToken)
}
