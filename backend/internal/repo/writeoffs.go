package repo

// M3c repositories: delinquency classification (set-based, config-driven)
// and the write-off lifecycle (maker-checker enforced by the 0011 schema —
// this layer surfaces it, it could not bypass it if it tried).

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
)

var (
	// ErrSelfApproval: the schema CHECK fired — approver == requester.
	ErrSelfApproval = errors.New("repo: write-off approver must differ from requester (maker-checker)")
	// ErrWriteOffExists: one write-off per advance, schema-arbitered.
	ErrWriteOffExists = errors.New("repo: advance already has a write-off")
)

// ---------------------------------------------------------------------------
// Delinquency classification (V2 §15) — overlay, never a state.
// ---------------------------------------------------------------------------

type DelinquencyBucket struct {
	Code           string
	MinDaysPastDue int
}

// ClassifyDelinquency stamps every OPEN money-bearing advance in the
// programme with its aging bucket in ONE set-based statement (owner scale
// rule): days-past-due = whole days since activation minus grace, floored at
// zero; the bucket is the highest ladder rung the age clears. Returns rows
// whose bucket CHANGED (the operational signal).
func (Advances) ClassifyDelinquency(ctx context.Context, tx pgx.Tx, programmeID string, buckets []DelinquencyBucket, graceDays int, asOf time.Time) (int64, error) {
	codes := make([]string, len(buckets))
	mins := make([]int, len(buckets))
	for i, b := range buckets {
		codes[i] = b.Code
		mins[i] = b.MinDaysPastDue
	}
	ct, err := tx.Exec(ctx, `
		WITH ladder AS (
			SELECT code, min_days FROM unnest($2::text[], $3::int[]) AS l(code, min_days)
		),
		aged AS (
			SELECT a.advance_id,
			       GREATEST(0, (EXTRACT(EPOCH FROM ($4::timestamptz - a.activated_at)) / 86400)::int - $5) AS dpd
			FROM advances a
			WHERE a.programme_id = $1
			  AND a.state IN ('ACTIVE','PARTIALLY_RECOVERED')
			  AND a.activated_at IS NOT NULL
		),
		assigned AS (
			SELECT aged.advance_id,
			       (SELECT l.code FROM ladder l WHERE l.min_days <= aged.dpd
			        ORDER BY l.min_days DESC LIMIT 1) AS bucket
			FROM aged
		)
		UPDATE advances a
		SET delinquency_bucket = assigned.bucket, bucket_as_of = $4
		FROM assigned
		WHERE a.advance_id = assigned.advance_id
		  AND (a.delinquency_bucket IS DISTINCT FROM assigned.bucket)`,
		programmeID, codes, mins, asOf, graceDays)
	if err != nil {
		return 0, fmt.Errorf("classify delinquency: %w", err)
	}
	return ct.RowsAffected(), nil
}

// ---------------------------------------------------------------------------
// Write-off lifecycle (V2 §15)
// ---------------------------------------------------------------------------

type WriteOffs struct{}

type WriteOff struct {
	WriteOffID  string
	TelcoID     string
	AdvanceID   string
	Principal   entity.Money
	Fee         entity.Money
	Reason      string
	RequestedBy string
	ApprovedBy  string
	State       string // REQUESTED | APPROVED | POSTED | REJECTED
	RequestedAt time.Time
	DecidedAt   *time.Time
	PostedAt    *time.Time
}

// Insert creates the REQUESTED write-off; a second request for the same
// advance returns ErrWriteOffExists (UNIQUE(advance_id)).
func (WriteOffs) Insert(ctx context.Context, tx pgx.Tx, w WriteOff) error {
	ct, err := tx.Exec(ctx, `
		INSERT INTO write_offs
		  (write_off_id, telco_id, advance_id, principal_minor, fee_minor, currency, reason, requested_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (advance_id) DO NOTHING`,
		w.WriteOffID, w.TelcoID, w.AdvanceID, w.Principal.Amount(), w.Fee.Amount(),
		string(w.Principal.Currency()), w.Reason, w.RequestedBy)
	if err != nil {
		return fmt.Errorf("insert write-off: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrWriteOffExists
	}
	return nil
}

func (WriteOffs) Get(ctx context.Context, tx pgx.Tx, writeOffID string) (WriteOff, error) {
	var w WriteOff
	var prin, fee int64
	var cur string
	err := tx.QueryRow(ctx, `
		SELECT write_off_id, telco_id, advance_id, principal_minor, fee_minor, currency,
		       reason, requested_by, COALESCE(approved_by,''), state, requested_at, decided_at, posted_at
		FROM write_offs WHERE write_off_id = $1`, writeOffID).
		Scan(&w.WriteOffID, &w.TelcoID, &w.AdvanceID, &prin, &fee, &cur,
			&w.Reason, &w.RequestedBy, &w.ApprovedBy, &w.State, &w.RequestedAt, &w.DecidedAt, &w.PostedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return w, fmt.Errorf("write-off %q: %w", writeOffID, ErrNotFound)
	}
	if err != nil {
		return w, err
	}
	if w.Principal, err = scanMoney(prin, cur); err != nil {
		return w, err
	}
	w.Fee, err = scanMoney(fee, cur)
	return w, err
}

// ListPending returns the REQUESTED write-offs awaiting a checker decision — the approvals
// inbox (Wave B.3 Phase B). Runs on the operatorRead RLS tx (write_offs granted 0071), so it
// is telco-scoped; fail-closed on a no-authority scope. telcoFilter is the OPTIONAL admin
// narrowing (?telco=): it can only further narrow the result — for a telco-bounded operator the
// scope's own telco already filters, and RLS is the boundary regardless — so it never widens.
func (WriteOffs) ListPending(ctx context.Context, q Querier, scope OperatorScope, telcoFilter string) ([]WriteOff, error) {
	telco, ok := scope.TelcoLevelBound()
	if !ok {
		return nil, nil
	}
	rows, err := q.Query(ctx, `
		SELECT write_off_id, telco_id, advance_id, principal_minor, fee_minor, currency,
		       reason, requested_by, COALESCE(approved_by,''), state, requested_at, decided_at, posted_at
		FROM write_offs
		WHERE state = 'REQUESTED' AND ($1 = '' OR telco_id = $1) AND ($2 = '' OR telco_id = $2)
		ORDER BY requested_at`, telco, telcoFilter)
	if err != nil {
		return nil, fmt.Errorf("list pending write-offs: %w", err)
	}
	defer rows.Close()
	var out []WriteOff
	for rows.Next() {
		var w WriteOff
		var prin, fee int64
		var cur string
		if err := rows.Scan(&w.WriteOffID, &w.TelcoID, &w.AdvanceID, &prin, &fee, &cur,
			&w.Reason, &w.RequestedBy, &w.ApprovedBy, &w.State, &w.RequestedAt, &w.DecidedAt, &w.PostedAt); err != nil {
			return nil, fmt.Errorf("scan pending write-off: %w", err)
		}
		var e error
		if w.Principal, e = scanMoney(prin, cur); e != nil {
			return nil, e
		}
		if w.Fee, e = scanMoney(fee, cur); e != nil {
			return nil, e
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// Decide moves REQUESTED -> APPROVED|REJECTED. The schema CHECKs are the
// maker-checker arbiter — a same-actor DECISION violates them and maps to
// ErrSelfApproval — this layer cannot weaken that even by bug. Both the
// APPROVED/POSTED CHECK (0011) and the REJECTED CHECK (0072) require a distinct
// approved_by, so approve AND reject are symmetric: neither can be self-decided.
func (WriteOffs) Decide(ctx context.Context, tx pgx.Tx, writeOffID, approver, toState string) error {
	ct, err := tx.Exec(ctx, `
		UPDATE write_offs SET state = $3, approved_by = $2, decided_at = now()
		WHERE write_off_id = $1 AND state = 'REQUESTED'`, writeOffID, approver, toState)
	if err != nil {
		// The only CHECKs this UPDATE can trip are the maker-checker ones (amounts
		// are untouched): 23514 here means a self-decision (self-approve or self-reject).
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23514" {
			return ErrSelfApproval
		}
		return fmt.Errorf("decide write-off: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("write-off %q not REQUESTED: %w", writeOffID, ErrNotFound)
	}
	return nil
}

// MarkPosted records the ledger movement completion (same tx as the posting).
func (WriteOffs) MarkPosted(ctx context.Context, tx pgx.Tx, writeOffID string) error {
	ct, err := tx.Exec(ctx, `
		UPDATE write_offs SET state = 'POSTED', posted_at = now()
		WHERE write_off_id = $1 AND state = 'APPROVED'`, writeOffID)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("write-off %q not APPROVED: %w", writeOffID, ErrNotFound)
	}
	return nil
}
