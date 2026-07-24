package repo

// HeldRecharge — the durable, reviewable queue for over-limit webhook recharges.
// An event that trips a blast-radius clamp is parked here (never ingested,
// never dropped) for a governed maker-checker release (S2.3). Tenant-scoped
// (RLS): all methods run inside a WithTenantTx.

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
)

// Hold reasons (why a recharge was held rather than ingested).
const (
	HeldReasonPerEventClamp = "PER_EVENT_CLAMP"
	HeldReasonDailyCeiling  = "DAILY_CEILING"
)

type HeldRecharge struct{}

// HeldEvent is an over-limit recharge parked for review.
type HeldEvent struct {
	TelcoID       string
	SourceEventID string // namespaced "wh:"<event_id>
	MSISDNToken   string
	AmountMinor   int64
	Currency      string
	OccurredAt    time.Time
	Reason        string
}

// Hold parks an over-limit recharge. Idempotent per (telco, source_event_id): a
// re-delivered over-limit event does not create a second hold. Returns whether a
// NEW hold was created (false = already held).
func (HeldRecharge) Hold(ctx context.Context, tx pgx.Tx, e HeldEvent) (created bool, err error) {
	ct, err := tx.Exec(ctx, `
		INSERT INTO held_recharge_events
		  (held_id, telco_id, source_event_id, msisdn_token, amount_minor, currency, occurred_at, reason)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (telco_id, source_event_id) DO NOTHING`,
		platform.NewID("hld"), e.TelcoID, e.SourceEventID, e.MSISDNToken,
		e.AmountMinor, e.Currency, e.OccurredAt, e.Reason)
	if err != nil {
		return false, err
	}
	return ct.RowsAffected() == 1, nil
}

// PruneWebhookNonces deletes recharge-webhook nonce rows older than the horizon
// (S2.3b maintenance). The nonce is pure defence-in-depth for the freshness
// window — anything older than the window is STALE-rejected before the nonce is
// consulted, so pruning past the horizon loses nothing; recovery idempotency by
// source_event_id remains the durable backstop. Runs on the worker (cross-tenant
// maintenance, like the dispatcher).
func PruneWebhookNonces(ctx context.Context, pool *pgxpool.Pool, olderThan time.Duration) (int64, error) {
	ct, err := pool.Exec(ctx, `
		DELETE FROM idempotency_records
		WHERE operation = 'recharge.webhook' AND created_at < now() - $1::interval`,
		olderThan.String())
	if err != nil {
		return 0, err
	}
	return ct.RowsAffected(), nil
}

// HeldRow is a held recharge as read back for the review queue.
type HeldRow struct {
	HeldID        string
	TelcoID       string
	SourceEventID string
	MSISDNToken   string
	AmountMinor   int64
	Currency      string
	OccurredAt    time.Time
	Reason        string
	Status        string
	RequestedBy   string // "" until a maker requests release
	ApprovedBy    string
}

// Get reads one held recharge (any status).
func (HeldRecharge) Get(ctx context.Context, tx pgx.Tx, heldID string) (HeldRow, error) {
	var h HeldRow
	var reqBy, appBy *string
	err := tx.QueryRow(ctx, `
		SELECT held_id, telco_id, source_event_id, msisdn_token, amount_minor, currency,
		       occurred_at, reason, status, requested_by, approved_by
		FROM held_recharge_events WHERE held_id = $1`, heldID).Scan(
		&h.HeldID, &h.TelcoID, &h.SourceEventID, &h.MSISDNToken, &h.AmountMinor, &h.Currency,
		&h.OccurredAt, &h.Reason, &h.Status, &reqBy, &appBy)
	if err != nil {
		return HeldRow{}, err
	}
	if reqBy != nil {
		h.RequestedBy = *reqBy
	}
	if appBy != nil {
		h.ApprovedBy = *appBy
	}
	return h, nil
}

// ListOpen returns the telco's open (HELD) queue, newest first.
func (HeldRecharge) ListOpen(ctx context.Context, tx pgx.Tx, limit int) ([]HeldRow, error) {
	rows, err := tx.Query(ctx, `
		SELECT held_id, telco_id, source_event_id, msisdn_token, amount_minor, currency,
		       occurred_at, reason, status, requested_by, approved_by
		FROM held_recharge_events WHERE status = 'HELD'
		ORDER BY held_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HeldRow
	for rows.Next() {
		var h HeldRow
		var reqBy, appBy *string
		if err := rows.Scan(&h.HeldID, &h.TelcoID, &h.SourceEventID, &h.MSISDNToken, &h.AmountMinor,
			&h.Currency, &h.OccurredAt, &h.Reason, &h.Status, &reqBy, &appBy); err != nil {
			return nil, err
		}
		if reqBy != nil {
			h.RequestedBy = *reqBy
		}
		if appBy != nil {
			h.ApprovedBy = *appBy
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// RequestRelease records the MAKER on a still-open hold (once — a second
// request or a decided hold is refused). Returns false when nothing matched.
func (HeldRecharge) RequestRelease(ctx context.Context, tx pgx.Tx, heldID, maker string) (bool, error) {
	ct, err := tx.Exec(ctx, `
		UPDATE held_recharge_events SET requested_by = $2
		WHERE held_id = $1 AND status = 'HELD' AND requested_by IS NULL`, heldID, maker)
	if err != nil {
		return false, err
	}
	return ct.RowsAffected() == 1, nil
}

// ClaimReleased atomically transitions HELD -> RELEASED for a requested hold,
// enforcing the DISTINCT approver in the same statement (the schema CHECK is
// the backstop). Returns false when nothing matched (not requested, already
// decided, or same actor) — the caller re-reads to distinguish.
func (HeldRecharge) ClaimReleased(ctx context.Context, tx pgx.Tx, heldID, approver string) (bool, error) {
	ct, err := tx.Exec(ctx, `
		UPDATE held_recharge_events
		SET approved_by = $2, status = 'RELEASED', resolved_at = now()
		WHERE held_id = $1 AND status = 'HELD'
		  AND requested_by IS NOT NULL AND requested_by <> $2`, heldID, approver)
	if err != nil {
		return false, err
	}
	return ct.RowsAffected() == 1, nil
}

// MarkRejected closes an open hold without ingesting (the safe direction —
// single actor, recorded in the audit trail; approved_by stays NULL so a maker
// may withdraw their own request without tripping the distinct-actor CHECK).
func (HeldRecharge) MarkRejected(ctx context.Context, tx pgx.Tx, heldID string) (bool, error) {
	ct, err := tx.Exec(ctx, `
		UPDATE held_recharge_events SET status = 'REJECTED', resolved_at = now()
		WHERE held_id = $1 AND status = 'HELD'`, heldID)
	if err != nil {
		return false, err
	}
	return ct.RowsAffected() == 1, nil
}

// DailyIngestedMinor sums the recharge amounts already INGESTED (not held) for a
// telco via the webhook since the start of the current UTC day — the running
// total the per-telco daily ceiling is checked against. Webhook recoveries are
// namespaced "wh:", so this counts only this feed.
func (HeldRecharge) DailyIngestedMinor(ctx context.Context, tx pgx.Tx, telcoID string) (int64, error) {
	var sum int64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(amount_minor), 0)
		FROM recovery_events
		WHERE telco_id = $1
		  AND source_event_id LIKE 'wh:%'
		  AND received_at >= date_trunc('day', now() AT TIME ZONE 'UTC')`,
		telcoID).Scan(&sum); err != nil {
		return 0, err
	}
	return sum, nil
}
