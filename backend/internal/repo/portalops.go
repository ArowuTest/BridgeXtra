package repo

// M4e ops operator reads: the ambiguity queues. Fulfilment attempts carry no
// tenant columns themselves, so the read joins advances (telco_id AND
// programme_id) and is bounded by the operator's OperatorScope in SQL — the
// non-bypassable M4C-F1 pattern on the worker (BYPASSRLS) operator-read pool.
// Pending reversals are TELCO-GRAINED (no programme dimension), so their reads
// take the TelcoLevelBound: a programme- or global-scoped operator reads
// NOTHING rather than everything.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
)

// AmbiguousAttempt is one fulfilment attempt an operator may need to chase:
// UNKNOWN (telco outcome unresolved) or SENT past the staleness threshold.
type AmbiguousAttempt struct {
	AttemptID     string
	AdvanceID     string
	TelcoID       string
	ProgrammeID   string
	AdvanceState  string
	FaceValue     entity.Money
	State         string
	AttemptNo     int
	EnquiryCount  int
	SubmittedAt   string // RFC3339
	NextEnquiryAt string // RFC3339, '' when unscheduled
}

const ambiguousCols = `f.attempt_id, f.advance_id, a.telco_id, a.programme_id, a.state,
	a.face_value_minor, a.currency, f.state, f.attempt_no, f.enquiry_count,
	to_char(f.submitted_at,'YYYY-MM-DD"T"HH24:MI:SS.USOF'),
	COALESCE(to_char(f.next_enquiry_at,'YYYY-MM-DD"T"HH24:MI:SS.USOF'),'')`

func scanAmbiguous(row pgx.Row) (AmbiguousAttempt, error) {
	var it AmbiguousAttempt
	var minor int64
	var cur string
	err := row.Scan(&it.AttemptID, &it.AdvanceID, &it.TelcoID, &it.ProgrammeID, &it.AdvanceState,
		&minor, &cur, &it.State, &it.AttemptNo, &it.EnquiryCount, &it.SubmittedAt, &it.NextEnquiryAt)
	if err != nil {
		return it, err
	}
	it.FaceValue, err = scanMoney(minor, cur)
	return it, err
}

// ListAmbiguousAttempts returns UNKNOWN attempts plus SENT attempts older than
// the governed staleness threshold, oldest first (the longest-ambiguous
// exposure surfaces on top). Scope-bounded via the advances join.
func ListAmbiguousAttempts(ctx context.Context, pool *pgxpool.Pool, scope OperatorScope, staleSentBeforeRFC3339 string, limit int) ([]AmbiguousAttempt, error) {
	if !scope.authority {
		return nil, nil
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := pool.Query(ctx, `
		SELECT `+ambiguousCols+`
		FROM fulfilment_attempts f
		JOIN advances a ON a.advance_id = f.advance_id
		WHERE ($1 = '' OR a.telco_id = $1)
		  AND ($2 = '' OR a.programme_id = $2)
		  AND (f.state = 'UNKNOWN'
		       OR (f.state = 'SENT' AND f.submitted_at < $3::timestamptz))
		ORDER BY f.submitted_at, f.attempt_id
		LIMIT $4`, scope.telco, scope.programme, staleSentBeforeRFC3339, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AmbiguousAttempt
	for rows.Next() {
		it, err := scanAmbiguous(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// GetAmbiguousAttempt loads one attempt WITHIN the operator's scope for
// load-scoped-then-act. Out-of-scope, absent, and already-resolved attempts
// all return ErrNotFound — the no-oracle 404 is structural, and an operator
// cannot nudge an attempt the resolver has already settled.
func GetAmbiguousAttempt(ctx context.Context, pool *pgxpool.Pool, scope OperatorScope, attemptID string) (AmbiguousAttempt, error) {
	var it AmbiguousAttempt
	if !scope.authority {
		return it, fmt.Errorf("attempt %q: %w", attemptID, ErrNotFound)
	}
	it, err := scanAmbiguous(pool.QueryRow(ctx, `
		SELECT `+ambiguousCols+`
		FROM fulfilment_attempts f
		JOIN advances a ON a.advance_id = f.advance_id
		WHERE f.attempt_id = $1
		  AND ($2 = '' OR a.telco_id = $2)
		  AND ($3 = '' OR a.programme_id = $3)
		  AND f.state IN ('UNKNOWN','SENT')`, attemptID, scope.telco, scope.programme))
	if errors.Is(err, pgx.ErrNoRows) {
		return it, fmt.Errorf("attempt %q: %w", attemptID, ErrNotFound)
	}
	return it, err
}

// ParkedReversalRow is one PARKED reversal awaiting operator attention, with
// the M3B-F1 park_reason naming the current blocker.
type ParkedReversalRow struct {
	PendingReversalID     string
	TelcoID               string
	OriginalSourceEventID string
	ReversalSourceEventID string
	Amount                entity.Money
	ParkReason            string
	ReceivedAt            string // RFC3339
}

const parkedCols = `pending_reversal_id, telco_id, original_source_event_id,
	reversal_source_event_id, amount_minor, currency, park_reason,
	to_char(received_at,'YYYY-MM-DD"T"HH24:MI:SS.USOF')`

func scanParked(row pgx.Row) (ParkedReversalRow, error) {
	var p ParkedReversalRow
	var minor int64
	var cur string
	err := row.Scan(&p.PendingReversalID, &p.TelcoID, &p.OriginalSourceEventID,
		&p.ReversalSourceEventID, &minor, &cur, &p.ParkReason, &p.ReceivedAt)
	if err != nil {
		return p, err
	}
	p.Amount, err = scanMoney(minor, cur)
	return p, err
}

// ListParkedReversals returns PARKED reversals oldest first. pending_reversals
// is telco-grained, so this takes the TelcoLevelBound — a programme-scoped
// operator sees nothing rather than every telco's money events.
func ListParkedReversals(ctx context.Context, pool *pgxpool.Pool, scope OperatorScope, limit int) ([]ParkedReversalRow, error) {
	telco, ok := scope.TelcoLevelBound()
	if !ok {
		return nil, nil
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := pool.Query(ctx, `
		SELECT `+parkedCols+`
		FROM pending_reversals
		WHERE state = 'PARKED'
		  AND ($1 = '' OR telco_id = $1)
		ORDER BY received_at, pending_reversal_id
		LIMIT $2`, telco, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ParkedReversalRow
	for rows.Next() {
		p, err := scanParked(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetParkedReversal loads one PARKED reversal within the operator's
// telco-level bound (load-scoped-then-act; no-oracle 404).
func GetParkedReversal(ctx context.Context, pool *pgxpool.Pool, scope OperatorScope, pendingReversalID string) (ParkedReversalRow, error) {
	var p ParkedReversalRow
	telco, ok := scope.TelcoLevelBound()
	if !ok {
		return p, fmt.Errorf("pending reversal %q: %w", pendingReversalID, ErrNotFound)
	}
	p, err := scanParked(pool.QueryRow(ctx, `
		SELECT `+parkedCols+`
		FROM pending_reversals
		WHERE pending_reversal_id = $1
		  AND state = 'PARKED'
		  AND ($2 = '' OR telco_id = $2)`, pendingReversalID, telco))
	if errors.Is(err, pgx.ErrNoRows) {
		return p, fmt.Errorf("pending reversal %q: %w", pendingReversalID, ErrNotFound)
	}
	return p, err
}
