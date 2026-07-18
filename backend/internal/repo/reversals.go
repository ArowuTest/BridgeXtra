package repo

// M3b repositories: reversal parking (EDG-019), reversal-aware allocation
// sums, written-off advance lookup (EDG-021), and the pool/advance mutations
// the reversal path needs. Same discipline as everything else: the schema
// arbitrates idempotency; RLS scopes tenancy.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
)

type PendingReversals struct{}

type PendingReversal struct {
	PendingReversalID     string
	TelcoID               string
	OriginalSourceEventID string
	ReversalSourceEventID string
	Amount                entity.Money
	State                 string // PARKED | APPLIED | EXPIRED
	ParkReason            string // why it waits (M3B-F1 operator signal)
	ReceivedAt            time.Time
	AppliedAt             *time.Time
}

// Park stores a reversal whose original has not arrived (EDG-019). Duplicate
// parking (same original or same reversal source) returns false — the first
// record wins, replays are no-ops.
func (PendingReversals) Park(ctx context.Context, tx pgx.Tx, p PendingReversal) (bool, error) {
	if p.ParkReason == "" {
		p.ParkReason = "ORIGINAL_UNSEEN"
	}
	ct, err := tx.Exec(ctx, `
		INSERT INTO pending_reversals
		  (pending_reversal_id, telco_id, original_source_event_id, reversal_source_event_id,
		   amount_minor, currency, state, park_reason)
		VALUES ($1,$2,$3,$4,$5,$6,'PARKED',$7)
		ON CONFLICT DO NOTHING`,
		p.PendingReversalID, p.TelcoID, p.OriginalSourceEventID, p.ReversalSourceEventID,
		p.Amount.Amount(), string(p.Amount.Currency()), p.ParkReason)
	if err != nil {
		return false, fmt.Errorf("park reversal: %w", err)
	}
	return ct.RowsAffected() == 1, nil
}

// SetParkReason updates why a PARKED reversal waits (e.g. its original
// arrived but application collided with an invariant — M3B-F1).
func (PendingReversals) SetParkReason(ctx context.Context, tx pgx.Tx, pendingReversalID, reason string) error {
	_, err := tx.Exec(ctx, `
		UPDATE pending_reversals SET park_reason = $2
		WHERE pending_reversal_id = $1 AND state = 'PARKED'`, pendingReversalID, reason)
	return err
}

// FindParkedForOriginal returns the PARKED reversal awaiting this original
// source event, if any.
func (PendingReversals) FindParkedForOriginal(ctx context.Context, tx pgx.Tx, originalSourceEventID string) (PendingReversal, error) {
	var p PendingReversal
	var minor int64
	var cur string
	err := tx.QueryRow(ctx, `
		SELECT pending_reversal_id, telco_id, original_source_event_id, reversal_source_event_id,
		       amount_minor, currency, state, park_reason, received_at, applied_at
		FROM pending_reversals
		WHERE original_source_event_id = $1 AND state = 'PARKED'`, originalSourceEventID).
		Scan(&p.PendingReversalID, &p.TelcoID, &p.OriginalSourceEventID, &p.ReversalSourceEventID,
			&minor, &cur, &p.State, &p.ParkReason, &p.ReceivedAt, &p.AppliedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, fmt.Errorf("parked reversal for %q: %w", originalSourceEventID, ErrNotFound)
	}
	if err != nil {
		return p, err
	}
	p.Amount, err = scanMoney(minor, cur)
	return p, err
}

// MarkApplied closes a parked reversal after its application transaction.
func (PendingReversals) MarkApplied(ctx context.Context, tx pgx.Tx, pendingReversalID string) error {
	ct, err := tx.Exec(ctx, `
		UPDATE pending_reversals SET state = 'APPLIED', applied_at = now()
		WHERE pending_reversal_id = $1 AND state = 'PARKED'`, pendingReversalID)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("pending reversal %q not PARKED: %w", pendingReversalID, ErrNotFound)
	}
	return nil
}

// NetAppliedByEvent returns the event's NET receivable allocations
// (FEE + PRINCIPAL, reversal rows included) — the maximum still reversible.
func (Allocations) NetAppliedByEvent(ctx context.Context, tx pgx.Tx, recoveryEventID string) (entity.Money, string, error) {
	var minor int64
	var cur *string
	var advanceID *string
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(amount_minor),0), MIN(currency), MIN(advance_id)
		FROM recovery_allocations
		WHERE recovery_event_id = $1 AND component IN ('FEE','PRINCIPAL')`, recoveryEventID).
		Scan(&minor, &cur, &advanceID)
	if err != nil {
		return entity.Money{}, "", err
	}
	if cur == nil || advanceID == nil {
		return entity.Money{}, "", fmt.Errorf("event %q has no receivable allocations: %w", recoveryEventID, ErrNotFound)
	}
	m, err := scanMoney(minor, *cur)
	return m, *advanceID, err
}

// ListNetByEventComponent returns net per-component sums for one event
// (reverse-waterfall un-allocation reads these).
func (Allocations) ListNetByEventComponent(ctx context.Context, tx pgx.Tx, recoveryEventID string) (map[entity.AllocationComponent]entity.Money, error) {
	rows, err := tx.Query(ctx, `
		SELECT component, SUM(amount_minor), MIN(currency)
		FROM recovery_allocations
		WHERE recovery_event_id = $1
		GROUP BY component`, recoveryEventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[entity.AllocationComponent]entity.Money{}
	for rows.Next() {
		var comp string
		var minor int64
		var cur string
		if err := rows.Scan(&comp, &minor, &cur); err != nil {
			return nil, err
		}
		m, err := scanMoney(minor, cur)
		if err != nil {
			return nil, err
		}
		out[entity.AllocationComponent(comp)] = m
	}
	return out, rows.Err()
}

// FindWrittenOffBySubscriber returns the most recent WRITTEN_OFF advance for
// the subscriber (EDG-021: post-write-off recoveries attach here as income).
func (Advances) FindWrittenOffBySubscriber(ctx context.Context, tx pgx.Tx, subscriberAccountID string) (entity.Advance, error) {
	return advanceScan(tx.QueryRow(ctx, `
		SELECT `+advanceCols+`
		FROM advances
		WHERE subscriber_account_id = $1 AND state = 'WRITTEN_OFF'
		ORDER BY updated_at DESC LIMIT 1`, subscriberAccountID))
}

// ApplyReversal re-opens the book on an advance: outstanding increases by the
// reversed amount and the state moves (CLOSED -> PARTIALLY_RECOVERED via the
// FSM, or stays in its open state). Optimistic-versioned like every
// transition.
func (Advances) ApplyReversal(ctx context.Context, tx pgx.Tx, advanceID string, version int, from, to entity.AdvanceState, newOutstanding entity.Money) error {
	if !entity.CanTransition(from, to) && from != to {
		return fmt.Errorf("%w: %s -> %s", ErrIllegalTransition, from, to)
	}
	ct, err := tx.Exec(ctx, `
		UPDATE advances
		SET state = $4, outstanding_minor = $5, version = version + 1,
		    closed_at = NULL, updated_at = now()
		WHERE advance_id = $1 AND version = $2 AND state = $3`,
		advanceID, version, string(from), string(to), newOutstanding.Amount())
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrStaleVersion
	}
	return nil
}

// ReAddUtilisation restores pool utilisation for a reversed recovery: the
// money is owed (and funded) again. The 0004 over-allocation CHECK still
// guards — if the pool no longer has headroom this FAILS LOUDLY and the
// whole reversal transaction rolls back for operator attention.
func (FundingPools) ReAddUtilisation(ctx context.Context, tx pgx.Tx, poolID string, amount entity.Money) error {
	ct, err := tx.Exec(ctx, `
		UPDATE funding_pools SET utilised_minor = utilised_minor + $2
		WHERE pool_id = $1`, poolID, amount.Amount())
	if err != nil {
		return fmt.Errorf("re-add utilisation (pool headroom check may have fired): %w", err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("funding pool %q: %w", poolID, ErrNotFound)
	}
	return nil
}
