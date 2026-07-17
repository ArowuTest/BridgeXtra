package repo

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
)

// Outbox implements the transactional outbox (V2-EVT-002) with the ADR-0001
// SF-4 dispatch stance: per-aggregate FIFO on the DB-assigned seq; SKIP LOCKED
// concurrency ACROSS aggregates.
type Outbox struct{}

// Append writes an event in the SAME transaction as the state change that
// produced it — the atomicity requirement of V2-EVT-002.
func (Outbox) Append(ctx context.Context, tx pgx.Tx, e entity.OutboxEvent) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO outbox (id, telco_id, aggregate_type, aggregate_id, event_type, schema_version, payload, occurred_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		e.ID, e.TelcoID, e.AggregateType, e.AggregateID, e.EventType, e.SchemaVersion, e.Payload, e.OccurredAt)
	return err
}

// ClaimBatch (worker pool, BYPASSRLS role): claims up to limit dispatchable
// events of the given REGISTERED event types (G0-F2 fix part 1: an event type
// nobody consumes never occupies a claim slot). An event is dispatchable only
// if NO older unpublished event exists for the same aggregate — per-aggregate
// FIFO (ADR-0001 SF-4). The inner guard deliberately ignores BOTH the type
// filter AND the dead-letter marker: a quarantined or unconsumed head still
// blocks its own aggregate's successors (ordering is never silently skipped);
// it just stops blocking other aggregates and stops consuming batch slots.
// SKIP LOCKED lets concurrent workers proceed on other aggregates.
// Indexes: outbox_unpublished_ix (claim window), outbox_agg_unpublished_ix (FIFO guard).
func (Outbox) ClaimBatch(ctx context.Context, tx pgx.Tx, limit int, eventTypes []string) ([]entity.OutboxEvent, error) {
	if len(eventTypes) == 0 {
		return nil, nil // no registered consumers: nothing is claimable
	}
	rows, err := tx.Query(ctx, `
		SELECT seq, id, telco_id, aggregate_type, aggregate_id, event_type, schema_version,
		       payload, occurred_at, attempts
		FROM outbox o
		WHERE o.published_at IS NULL
		  AND o.dead_lettered_at IS NULL
		  AND o.event_type = ANY($2)
		  AND NOT EXISTS (
		        SELECT 1 FROM outbox o2
		        WHERE o2.aggregate_id = o.aggregate_id
		          AND o2.seq < o.seq
		          AND o2.published_at IS NULL)
		ORDER BY o.seq
		LIMIT $1
		FOR UPDATE OF o SKIP LOCKED`, limit, eventTypes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []entity.OutboxEvent
	for rows.Next() {
		var e entity.OutboxEvent
		if err := rows.Scan(&e.Seq, &e.ID, &e.TelcoID, &e.AggregateType, &e.AggregateID,
			&e.EventType, &e.SchemaVersion, &e.Payload, &e.OccurredAt, &e.Attempts); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (Outbox) MarkPublished(ctx context.Context, tx pgx.Tx, seq int64, at time.Time) error {
	_, err := tx.Exec(ctx,
		`UPDATE outbox SET published_at = $2, attempts = attempts + 1 WHERE seq = $1`, seq, at)
	return err
}

func (Outbox) MarkFailed(ctx context.Context, tx pgx.Tx, seq int64, cause string) error {
	_, err := tx.Exec(ctx,
		`UPDATE outbox SET attempts = attempts + 1, last_error = $2 WHERE seq = $1`, seq, cause)
	return err
}

// MarkDeadLettered (G0-F2 fix part 2) removes a permanently-failed event from
// the claim window. It stays unpublished — so it keeps blocking its own
// aggregate's successors — and becomes explicit operator backlog (V2-EVT-008).
func (Outbox) MarkDeadLettered(ctx context.Context, tx pgx.Tx, seq int64, cause string) error {
	_, err := tx.Exec(ctx,
		`UPDATE outbox SET dead_lettered_at = now(), last_error = $2 WHERE seq = $1 AND published_at IS NULL`,
		seq, cause)
	return err
}

// Requeue is the authorised operator-replay action (V2-EVT-009): clears the
// dead-letter marker and resets the attempt budget.
func (Outbox) Requeue(ctx context.Context, tx pgx.Tx, seq int64) error {
	ct, err := tx.Exec(ctx,
		`UPDATE outbox SET dead_lettered_at = NULL, attempts = 0, last_error = NULL
		 WHERE seq = $1 AND dead_lettered_at IS NOT NULL`, seq)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeadLetteredCount is the DLQ health metric (V2-EVT-013 / V3-SRE-006).
func (Outbox) DeadLetteredCount(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	var n int64
	err := pool.QueryRow(ctx,
		`SELECT count(*) FROM outbox WHERE dead_lettered_at IS NOT NULL`).Scan(&n)
	return n, err
}

// UnpublishedCount is a worker-pool health metric (V2-EVT-013).
func (Outbox) UnpublishedCount(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	var n int64
	err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE published_at IS NULL`).Scan(&n)
	return n, err
}
