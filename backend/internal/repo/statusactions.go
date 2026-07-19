package repo

// M4e-2 subscriber-status actions: the maker-checker record behind the ONLY
// production writer of subscriber_accounts.status (VR-35-F1 closure). Same
// discipline as write_offs: the schema arbitrates the two-actor rule and the
// one-open-action convergence (C5 partial unique); terminal rows are frozen
// by trigger; RLS scopes tenancy. The apply is compare-and-set (C2): a status
// that drifted since request refuses loudly, never overwrites blind.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type StatusActions struct{}

type StatusAction struct {
	ActionID            string
	TelcoID             string
	SubscriberAccountID string
	FromStatus          string
	ToStatus            string
	Reason              string
	RequestedBy         string
	ApprovedBy          string
	State               string // REQUESTED | REJECTED | APPLIED
	RequestedAt         time.Time
	DecidedAt           *time.Time
	AppliedAt           *time.Time
}

// ErrOpenActionExists surfaces the C5 one-open-action convergence rule.
var ErrOpenActionExists = errors.New("subscriber already has an open status action")

// Insert records a REQUESTED action. The partial unique index converges
// concurrent requests: the second inserter gets ErrOpenActionExists.
func (StatusActions) Insert(ctx context.Context, tx pgx.Tx, a StatusAction) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO subscriber_status_actions
		  (action_id, telco_id, subscriber_account_id, from_status, to_status, reason, requested_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		a.ActionID, a.TelcoID, a.SubscriberAccountID, a.FromStatus, a.ToStatus, a.Reason, a.RequestedBy)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "status_action_one_open_uq" {
		return fmt.Errorf("subscriber %s: %w", a.SubscriberAccountID, ErrOpenActionExists)
	}
	return err
}

// ClaimRequestedByID locks one REQUESTED action for decision (FOR UPDATE —
// two concurrent deciders serialise; the loser sees not-REQUESTED).
func (StatusActions) ClaimRequestedByID(ctx context.Context, tx pgx.Tx, actionID string) (StatusAction, error) {
	var a StatusAction
	var approvedBy *string
	err := tx.QueryRow(ctx, `
		SELECT action_id, telco_id, subscriber_account_id, from_status, to_status, reason,
		       requested_by, approved_by, state, requested_at, decided_at, applied_at
		FROM subscriber_status_actions
		WHERE action_id = $1 AND state = 'REQUESTED'
		FOR UPDATE`, actionID).
		Scan(&a.ActionID, &a.TelcoID, &a.SubscriberAccountID, &a.FromStatus, &a.ToStatus, &a.Reason,
			&a.RequestedBy, &approvedBy, &a.State, &a.RequestedAt, &a.DecidedAt, &a.AppliedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, fmt.Errorf("status action %q not open: %w", actionID, ErrNotFound)
	}
	if approvedBy != nil {
		a.ApprovedBy = *approvedBy
	}
	return a, err
}

// Decide moves a claimed REQUESTED action to REJECTED or (post-apply) APPLIED,
// stamping the distinct second actor. The schema CHECK re-verifies the
// two-actor rule regardless of what the code passed.
func (StatusActions) Decide(ctx context.Context, tx pgx.Tx, actionID, approvedBy, state string) error {
	ct, err := tx.Exec(ctx, `
		UPDATE subscriber_status_actions
		SET state = $3, approved_by = $2, decided_at = now(),
		    applied_at = CASE WHEN $3 = 'APPLIED' THEN now() ELSE applied_at END
		WHERE action_id = $1 AND state = 'REQUESTED'`, actionID, approvedBy, state)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("status action %q not open: %w", actionID, ErrNotFound)
	}
	return nil
}

// ErrStatusDrift is the C2 compare-and-set refusal: the subscriber's live
// status no longer matches the action's from_status.
var ErrStatusDrift = errors.New("subscriber status changed since the action was requested")

// SetStatusCAS flips the subscriber's status ONLY from the expected value —
// the sole production write of subscriber_accounts.status (grant 0027).
func (Subscribers) SetStatusCAS(ctx context.Context, tx pgx.Tx, subscriberAccountID, from, to string) error {
	ct, err := tx.Exec(ctx, `
		UPDATE subscriber_accounts SET status = $3
		WHERE subscriber_account_id = $1 AND status = $2`, subscriberAccountID, from, to)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("subscriber %s not in status %s: %w", subscriberAccountID, from, ErrStatusDrift)
	}
	return nil
}
