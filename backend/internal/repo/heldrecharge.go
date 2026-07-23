package repo

// HeldRecharge — the durable, reviewable queue for over-limit webhook recharges.
// An event that trips a blast-radius clamp is parked here (never ingested,
// never dropped) for a governed maker-checker release (S2.3). Tenant-scoped
// (RLS): all methods run inside a WithTenantTx.

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

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
