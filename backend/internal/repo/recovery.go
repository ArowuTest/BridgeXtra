package repo

// Recovery-side repositories (M1b-4): event dedup at the DB (EDG-018),
// allocations (append-only by grants), suspense quarantine (EDG-020), and
// the resolver's due-attempt claims.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
)

type RecoveryEvents struct{}

// Insert dedupes on (telco_id, source_event_id): created=false means the
// telco replayed the event — the caller returns the original outcome.
func (RecoveryEvents) Insert(ctx context.Context, tx pgx.Tx, e entity.RecoveryEvent) (created bool, err error) {
	ct, err := tx.Exec(ctx, `
		INSERT INTO recovery_events (recovery_event_id, telco_id, source_event_id,
		  subscriber_account_id, msisdn_token, amount_minor, currency, state, occurred_at)
		VALUES ($1,$2,$3,NULLIF($4,''),NULLIF($5,''),$6,$7,$8,$9)
		ON CONFLICT (telco_id, source_event_id) DO NOTHING`,
		e.RecoveryEventID, e.TelcoID, e.SourceEventID, e.SubscriberAccountID, e.MSISDNToken,
		e.Amount.Amount(), string(e.Amount.Currency()), e.State, e.OccurredAt)
	if err != nil {
		return false, err
	}
	return ct.RowsAffected() == 1, nil
}

func (RecoveryEvents) GetBySource(ctx context.Context, tx pgx.Tx, sourceEventID string) (entity.RecoveryEvent, error) {
	var e entity.RecoveryEvent
	var minor int64
	var cur string
	var sub *string
	err := tx.QueryRow(ctx, `
		SELECT recovery_event_id, telco_id, source_event_id, subscriber_account_id,
		       amount_minor, currency, state, occurred_at, received_at
		FROM recovery_events WHERE source_event_id = $1`, sourceEventID).
		Scan(&e.RecoveryEventID, &e.TelcoID, &e.SourceEventID, &sub, &minor, &cur,
			&e.State, &e.OccurredAt, &e.ReceivedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, ErrNotFound
	}
	if err != nil {
		return e, err
	}
	if sub != nil {
		e.SubscriberAccountID = *sub
	}
	e.Amount, err = scanMoney(minor, cur)
	return e, err
}

func (RecoveryEvents) SetState(ctx context.Context, tx pgx.Tx, recoveryEventID string, from, to entity.RecoveryEventState) error {
	ct, err := tx.Exec(ctx,
		`UPDATE recovery_events SET state = $3 WHERE recovery_event_id = $1 AND state = $2`,
		recoveryEventID, from, to)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("recovery event %s not in state %s: %w", recoveryEventID, from, ErrNotFound)
	}
	return nil
}

type Allocations struct{}

func (Allocations) Insert(ctx context.Context, tx pgx.Tx, a entity.RecoveryAllocation) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO recovery_allocations (allocation_id, recovery_event_id, advance_id, component, amount_minor, currency)
		VALUES ($1,$2,$3,$4,$5,$6)`,
		a.AllocationID, a.RecoveryEventID, a.AdvanceID, a.Component,
		a.Amount.Amount(), string(a.Amount.Currency()))
	return err
}

// SumByComponent returns recovered-so-far per component for an advance
// (drives the waterfall split). Returns Money, not raw minor units — bare
// integer money never crosses a repo boundary (BC-1/ADR-0002; VR-8 NIT).
// Index: recovery_alloc_advance_ix.
func (Allocations) SumByComponent(ctx context.Context, tx pgx.Tx, advanceID string) (map[entity.AllocationComponent]entity.Money, error) {
	rows, err := tx.Query(ctx, `
		SELECT component, currency, COALESCE(SUM(amount_minor),0)
		FROM recovery_allocations WHERE advance_id = $1 GROUP BY component, currency`, advanceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[entity.AllocationComponent]entity.Money{}
	for rows.Next() {
		var c entity.AllocationComponent
		var cur string
		var sum int64
		if err := rows.Scan(&c, &cur, &sum); err != nil {
			return nil, err
		}
		m, err := scanMoney(sum, cur)
		if err != nil {
			return nil, err
		}
		if prev, ok := out[c]; ok {
			// Multi-currency per component would be a data-integrity breach
			// (one advance = one currency); surface it, never merge blindly.
			return nil, fmt.Errorf("allocation component %s spans currencies (%s, %s)", c, prev.Currency(), cur)
		}
		out[c] = m
	}
	return out, rows.Err()
}

type Suspense struct{}

func (Suspense) Insert(ctx context.Context, tx pgx.Tx, telcoID, recoveryEventID string, amount entity.Money, reason string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO suspense_items (suspense_id, telco_id, recovery_event_id, amount_minor, currency, reason)
		VALUES ($1,$2,$3,$4,$5,$6)`,
		"ssp_"+recoveryEventID, telcoID, recoveryEventID, amount.Amount(), string(amount.Currency()), reason)
	return err
}

// ---------------------------------------------------------------------------
// Advance-side recovery + resolver support
// ---------------------------------------------------------------------------

// FindOpenBySubscriber locks the subscriber's recoverable advance (oldest
// first; with max_concurrent=1 there is at most one — V2-COL-002 default).
func (Advances) FindOpenBySubscriber(ctx context.Context, tx pgx.Tx, subscriberAccountID string) (entity.Advance, error) {
	return advanceScan(tx.QueryRow(ctx,
		`SELECT `+advanceCols+` FROM advances
		 WHERE subscriber_account_id = $1 AND state IN ('ACTIVE','PARTIALLY_RECOVERED')
		 ORDER BY accepted_at
		 LIMIT 1
		 FOR UPDATE`, subscriberAccountID))
}

// ApplyRecovery reduces outstanding and transitions state, all under the
// optimistic version guard. newOutstanding must be >= 0 (schema CHECK is the
// backstop for INV-006).
func (Advances) ApplyRecovery(ctx context.Context, tx pgx.Tx, advanceID string, fromVersion int, from, to entity.AdvanceState, newOutstanding entity.Money) error {
	if !entity.CanTransition(from, to) && from != to {
		return fmt.Errorf("%w: %s -> %s", ErrIllegalTransition, from, to)
	}
	ct, err := tx.Exec(ctx, `
		UPDATE advances SET outstanding_minor = $5, state = $4, version = version + 1, updated_at = now(),
		  closed_at = CASE WHEN $4 = 'CLOSED' THEN now() ELSE closed_at END
		WHERE advance_id = $1 AND version = $2 AND state = $3`,
		advanceID, fromVersion, from, to, newOutstanding.Amount())
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("advance %s v%d recovery: %w", advanceID, fromVersion, ErrStaleVersion)
	}
	return nil
}

// DueEnquiries claims attempts needing resolution (worker, SKIP LOCKED —
// per-attempt parallelism is safe): UNKNOWN attempts whose enquiry is due,
// plus SENT attempts stale past the threshold (crash between tx1 and tx2 —
// EDG-007/008). Index: fulfilment_unknown_ix.
func (Attempts) DueEnquiries(ctx context.Context, tx pgx.Tx, now time.Time, staleSentBefore time.Time, limit int) ([]entity.FulfilmentAttempt, error) {
	rows, err := tx.Query(ctx, `
		SELECT attempt_id, advance_id, attempt_no, telco_idempotency_key, state,
		       COALESCE(telco_reference,''), request_evidence, response_evidence,
		       submitted_at, next_enquiry_at, enquiry_count, resolved_at
		FROM fulfilment_attempts
		WHERE (state = 'UNKNOWN' AND next_enquiry_at <= $1)
		   OR (state = 'SENT' AND submitted_at < $2)
		ORDER BY submitted_at
		LIMIT $3
		FOR UPDATE SKIP LOCKED`, now, staleSentBefore, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []entity.FulfilmentAttempt
	for rows.Next() {
		var at entity.FulfilmentAttempt
		if err := rows.Scan(&at.AttemptID, &at.AdvanceID, &at.AttemptNo, &at.TelcoIdempotencyKey,
			&at.State, &at.TelcoReference, &at.RequestEvidence, &at.ResponseEvidence,
			&at.SubmittedAt, &at.NextEnquiryAt, &at.EnquiryCount, &at.ResolvedAt); err != nil {
			return nil, err
		}
		out = append(out, at)
	}
	return out, rows.Err()
}

// RescheduleEnquiry bumps the enquiry counter and sets the next due time
// (still-unknown cycles — quiet, no state change, no events per VR-7b).
func (Attempts) RescheduleEnquiry(ctx context.Context, tx pgx.Tx, attemptID string, nextAt time.Time) error {
	_, err := tx.Exec(ctx, `
		UPDATE fulfilment_attempts
		SET enquiry_count = enquiry_count + 1, next_enquiry_at = $2,
		    state = CASE WHEN state = 'SENT' THEN 'UNKNOWN' ELSE state END
		WHERE attempt_id = $1`, attemptID, nextAt)
	return err
}
