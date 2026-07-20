package repo

// Self-exclusion register (R1-MUST): the authoritative lifecycle of a
// subscriber's opt-out from credit. The subscriber_accounts.status mirror is
// kept in sync by the usecase in the same tx; this register owns the cool-off.

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type SelfExclusions struct{}

type SelfExclusion struct {
	ExclusionID         string
	TelcoID             string
	SubscriberAccountID string
	State               string
	Channel             string
	Reason              string
	RequestedAt         time.Time
	MinUntil            time.Time
}

// Insert records a new ACTIVE self-exclusion. The partial unique index rejects a
// second ACTIVE exclusion for the same subscriber (fail-closed).
func (SelfExclusions) Insert(ctx context.Context, tx pgx.Tx, e SelfExclusion) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO self_exclusions
		  (exclusion_id, telco_id, subscriber_account_id, channel, reason, min_until)
		VALUES ($1,$2,$3,$4,NULLIF($5,''),$6)`,
		e.ExclusionID, e.TelcoID, e.SubscriberAccountID, e.Channel, e.Reason, e.MinUntil)
	return err
}

// GetActiveBySubscriber returns the subscriber's ACTIVE self-exclusion, if any.
// This is the AUTHORITATIVE enforcement read — the origination gate refuses on it
// so the control never depends on the status mirror being in sync.
func (SelfExclusions) GetActiveBySubscriber(ctx context.Context, tx pgx.Tx, subscriberAccountID string) (SelfExclusion, bool, error) {
	var e SelfExclusion
	err := tx.QueryRow(ctx, `
		SELECT exclusion_id, telco_id, subscriber_account_id, state, channel,
		       COALESCE(reason,''), requested_at, min_until
		FROM self_exclusions
		WHERE subscriber_account_id = $1 AND state = 'ACTIVE'`, subscriberAccountID).
		Scan(&e.ExclusionID, &e.TelcoID, &e.SubscriberAccountID, &e.State, &e.Channel,
			&e.Reason, &e.RequestedAt, &e.MinUntil)
	if errors.Is(err, pgx.ErrNoRows) {
		return SelfExclusion{}, false, nil
	}
	if err != nil {
		return SelfExclusion{}, false, err
	}
	return e, true, nil
}

// MarkReinstated flips an ACTIVE exclusion to REINSTATED. The cool-off is
// enforced structurally: only a row whose min_until has elapsed is affected, so a
// reinstatement before the cool-off touches nothing (RowsAffected 0).
func (SelfExclusions) MarkReinstated(ctx context.Context, tx pgx.Tx, exclusionID, channel string) (bool, error) {
	ct, err := tx.Exec(ctx, `
		UPDATE self_exclusions
		SET state = 'REINSTATED', reinstated_at = now(), reinstated_channel = $2
		WHERE exclusion_id = $1 AND state = 'ACTIVE' AND min_until <= now()`,
		exclusionID, channel)
	if err != nil {
		return false, err
	}
	return ct.RowsAffected() == 1, nil
}

// SetStatus transitions a LIVE subscriber's status with an explicit from-guard so
// concurrent transitions converge instead of clobbering. Returns ErrNotFound if
// the subscriber is not in the expected state.
func (Subscribers) SetStatus(ctx context.Context, tx pgx.Tx, subscriberAccountID, from, to string) error {
	ct, err := tx.Exec(ctx, `
		UPDATE subscriber_accounts SET status = $3
		WHERE subscriber_account_id = $1 AND status = $2 AND effective_to IS NULL`,
		subscriberAccountID, from, to)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
