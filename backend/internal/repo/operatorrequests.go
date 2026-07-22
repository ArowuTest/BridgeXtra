package repo

// Governed operator provisioning (v1): the maker-checker record behind CREATE
// of an operator credential. Same discipline as subscriber_status_actions — the
// schema arbitrates the two-actor rule (a decision needs a DISTINCT approver)
// and the one-open convergence (partial unique); terminal rows are frozen by
// trigger. Platform-scope (operators are not telco data), so no RLS — access is
// gated by the ADMIN-only API and the column grants in migration 0047.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type OperatorRequests struct{}

type OperatorRequest struct {
	RequestID   string
	Actor       string // the proposed new operator's stable identity
	Role        string
	Scope       string
	Reason      string
	RequestedBy string
	ApprovedBy  string
	State       string // REQUESTED | REJECTED | APPLIED
	RequestedAt time.Time
	DecidedAt   *time.Time
	AppliedAt   *time.Time
}

// ErrOpenRequestExists surfaces the one-open-create-per-actor convergence rule.
var ErrOpenRequestExists = errors.New("operator already has an open create request")

// ErrSelfApproveOperator is the schema two-actor refusal mapped to a sentinel:
// the approver of a create request must differ from the proposer.
var ErrSelfApproveOperator = errors.New("an operator create request cannot be approved by its proposer")

// Insert records a REQUESTED create. The partial unique index converges
// concurrent proposals: the second inserter gets ErrOpenRequestExists.
func (OperatorRequests) Insert(ctx context.Context, tx pgx.Tx, r OperatorRequest) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO operator_create_requests
		  (request_id, actor, role, scope, reason, requested_by)
		VALUES ($1,$2,$3,$4,$5,$6)`,
		r.RequestID, r.Actor, r.Role, r.Scope, r.Reason, r.RequestedBy)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "operator_create_one_open_uq" {
		return fmt.Errorf("operator %s: %w", r.Actor, ErrOpenRequestExists)
	}
	return err
}

// ClaimRequestedByID locks one REQUESTED create for decision (FOR UPDATE — two
// concurrent deciders serialise; the loser sees not-REQUESTED).
func (OperatorRequests) ClaimRequestedByID(ctx context.Context, tx pgx.Tx, requestID string) (OperatorRequest, error) {
	var r OperatorRequest
	var approvedBy *string
	err := tx.QueryRow(ctx, `
		SELECT request_id, actor, role, scope, reason, requested_by, approved_by,
		       state, requested_at, decided_at, applied_at
		FROM operator_create_requests
		WHERE request_id = $1 AND state = 'REQUESTED'
		FOR UPDATE`, requestID).
		Scan(&r.RequestID, &r.Actor, &r.Role, &r.Scope, &r.Reason, &r.RequestedBy, &approvedBy,
			&r.State, &r.RequestedAt, &r.DecidedAt, &r.AppliedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, fmt.Errorf("operator create request %q not open: %w", requestID, ErrNotFound)
	}
	if approvedBy != nil {
		r.ApprovedBy = *approvedBy
	}
	return r, err
}

// Decide moves a claimed REQUESTED create to REJECTED or (post-insert) APPLIED,
// stamping the distinct second actor. The schema CHECK re-verifies the two-actor
// rule regardless of what the code passed — a self-approve surfaces as
// ErrSelfApproveOperator.
func (OperatorRequests) Decide(ctx context.Context, tx pgx.Tx, requestID, approvedBy, state string) error {
	ct, err := tx.Exec(ctx, `
		UPDATE operator_create_requests
		SET state = $3, approved_by = $2, decided_at = now(),
		    applied_at = CASE WHEN $3 = 'APPLIED' THEN now() ELSE applied_at END
		WHERE request_id = $1 AND state = 'REQUESTED'`, requestID, approvedBy, state)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23514" {
		return fmt.Errorf("request %s: %w", requestID, ErrSelfApproveOperator)
	}
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("operator create request %q not open: %w", requestID, ErrNotFound)
	}
	return nil
}

// ListOpen returns the pending create requests for the admin console.
func (OperatorRequests) ListOpen(ctx context.Context, q Querier) ([]OperatorRequest, error) {
	rows, err := q.Query(ctx, `
		SELECT request_id, actor, role, scope, reason, requested_by, state, requested_at
		FROM operator_create_requests
		WHERE state = 'REQUESTED'
		ORDER BY requested_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OperatorRequest
	for rows.Next() {
		var r OperatorRequest
		if err := rows.Scan(&r.RequestID, &r.Actor, &r.Role, &r.Scope, &r.Reason,
			&r.RequestedBy, &r.State, &r.RequestedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
