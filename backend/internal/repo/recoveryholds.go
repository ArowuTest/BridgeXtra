package repo

// RecoveryHolds — the re-origination HOLD ledger (S3-C2). A wh: recovery that
// CLOSES a debt records a hold here; origination refuses a fresh advance for a
// subscriber with an open hold; the RECOVERY recon clears a hold when the feed
// confirms the subscriber's recovery (its token MATCHED on the business day). This
// records eligibility only — NO money movement (recovery still books finally at
// ingest). Tenant-scoped (RLS): all methods run inside a WithTenantTx.

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
)

type RecoveryHolds struct{}

// Insert records a hold for a debt CLOSED by a wh: recovery. Idempotent per
// recovery_event_id (a re-delivered close does not double-hold).
func (RecoveryHolds) Insert(ctx context.Context, tx pgx.Tx, telcoID, subscriberID, advanceID, recoveryEventID string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO recovery_unconfirmed_closes (id, telco_id, subscriber_account_id, advance_id, recovery_event_id)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (recovery_event_id) DO NOTHING`,
		platform.NewID("ruc"), telcoID, subscriberID, advanceID, recoveryEventID)
	return err
}

// HasUncleared reports whether a subscriber has any open (uncleared) hold — the
// origination precondition. Index-served (recovery_unconfirmed_closes_open_ix).
func (RecoveryHolds) HasUncleared(ctx context.Context, tx pgx.Tx, subscriberID string) (bool, error) {
	var held bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM recovery_unconfirmed_closes WHERE subscriber_account_id=$1 AND cleared_at IS NULL)`,
		subscriberID).Scan(&held); err != nil {
		return false, err
	}
	return held, nil
}

// ClearMatched clears every open hold whose closing recovery event falls in the
// business-day window [periodStart, periodEnd) AND whose subscriber's token
// MATCHED in the day's ACTIVE RECOVERY run — i.e. the feed confirmed that specific
// recovery. A phantom whose token did NOT match stays held (surfaced as a break,
// subscriber stays blocked). Precise per-token clearing, not day-wide. Returns the
// number cleared.
func (RecoveryHolds) ClearMatched(ctx context.Context, tx pgx.Tx, periodStart, periodEnd time.Time) (int64, error) {
	ct, err := tx.Exec(ctx, `
		UPDATE recovery_unconfirmed_closes h
		SET cleared_at = now()
		FROM recovery_events re
		WHERE h.recovery_event_id = re.recovery_event_id
		  AND h.cleared_at IS NULL
		  AND re.occurred_at >= $1 AND re.occurred_at < $2
		  AND EXISTS (
		    SELECT 1 FROM recon_items i
		    JOIN recon_runs r ON r.run_id = i.run_id
		    WHERE r.layer = 'RECOVERY' AND r.state = 'ACTIVE'
		      AND r.period_start = $1
		      AND i.status = 'MATCHED'
		      AND i.match_key = re.msisdn_token)`,
		periodStart, periodEnd)
	if err != nil {
		return 0, err
	}
	return ct.RowsAffected(), nil
}
