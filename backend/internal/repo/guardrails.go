package repo

// M3d repositories: guardrail trip lifecycle (schema-enforced two-person
// re-arm) and programme status transitions.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
)

// ErrSelfRearm: the schema CHECK fired — re-arm approver == requester.
var ErrSelfRearm = errors.New("repo: guardrail re-arm approver must differ from requester (two-person decision)")

type GuardrailTrips struct{}

type GuardrailTrip struct {
	TripID           string
	TelcoID          string
	ProgrammeID      string
	Guardrail        string // DAILY_DISBURSED | OPEN_EXPOSURE
	Measured         entity.Money
	Limit            entity.Money
	State            string // TRIPPED | REARM_REQUESTED | REARMED
	TrippedAt        time.Time
	RearmRequestedBy string
	RearmApprovedBy  string
	RearmedAt        *time.Time
}

// Insert records a trip; concurrent detection of the same breach converges
// on the single open row (partial unique). Returns whether a new row landed.
func (GuardrailTrips) Insert(ctx context.Context, tx pgx.Tx, t GuardrailTrip) (bool, error) {
	ct, err := tx.Exec(ctx, `
		INSERT INTO guardrail_trips
		  (trip_id, telco_id, programme_id, guardrail, measured_minor, limit_minor, currency)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (programme_id, guardrail) WHERE state <> 'REARMED' DO NOTHING`,
		t.TripID, t.TelcoID, t.ProgrammeID, t.Guardrail,
		t.Measured.Amount(), t.Limit.Amount(), string(t.Measured.Currency()))
	if err != nil {
		return false, fmt.Errorf("insert guardrail trip: %w", err)
	}
	return ct.RowsAffected() == 1, nil
}

// RequestRearm: maker step, TRIPPED -> REARM_REQUESTED.
func (GuardrailTrips) RequestRearm(ctx context.Context, tx pgx.Tx, tripID, actor string) error {
	ct, err := tx.Exec(ctx, `
		UPDATE guardrail_trips SET state = 'REARM_REQUESTED', rearm_requested_by = $2
		WHERE trip_id = $1 AND state = 'TRIPPED'`, tripID, actor)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("guardrail trip %q not TRIPPED: %w", tripID, ErrNotFound)
	}
	return nil
}

// ApproveRearm: checker step, REARM_REQUESTED -> REARMED. The 0014 schema
// CHECK arbitrates the two-person rule; a same-actor approval maps to
// ErrSelfRearm. Returns the closed trip.
func (GuardrailTrips) ApproveRearm(ctx context.Context, tx pgx.Tx, tripID, approver string) (GuardrailTrip, error) {
	ct, err := tx.Exec(ctx, `
		UPDATE guardrail_trips SET state = 'REARMED', rearm_approved_by = $2, rearmed_at = now()
		WHERE trip_id = $1 AND state = 'REARM_REQUESTED'`, tripID, approver)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23514" {
			return GuardrailTrip{}, ErrSelfRearm
		}
		return GuardrailTrip{}, fmt.Errorf("approve rearm: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return GuardrailTrip{}, fmt.Errorf("guardrail trip %q not REARM_REQUESTED: %w", tripID, ErrNotFound)
	}
	return (GuardrailTrips{}).Get(ctx, tx, tripID)
}

func (GuardrailTrips) Get(ctx context.Context, tx pgx.Tx, tripID string) (GuardrailTrip, error) {
	var t GuardrailTrip
	var measured, limit int64
	var cur string
	err := tx.QueryRow(ctx, `
		SELECT trip_id, telco_id, programme_id, guardrail, measured_minor, limit_minor, currency,
		       state, tripped_at, COALESCE(rearm_requested_by,''), COALESCE(rearm_approved_by,''), rearmed_at
		FROM guardrail_trips WHERE trip_id = $1`, tripID).
		Scan(&t.TripID, &t.TelcoID, &t.ProgrammeID, &t.Guardrail, &measured, &limit, &cur,
			&t.State, &t.TrippedAt, &t.RearmRequestedBy, &t.RearmApprovedBy, &t.RearmedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, fmt.Errorf("guardrail trip %q: %w", tripID, ErrNotFound)
	}
	if err != nil {
		return t, err
	}
	if t.Measured, err = scanMoney(measured, cur); err != nil {
		return t, err
	}
	t.Limit, err = scanMoney(limit, cur)
	return t, err
}

// CountOpenForProgramme: trips still holding the programme (any guardrail).
func (GuardrailTrips) CountOpenForProgramme(ctx context.Context, tx pgx.Tx, programmeID string) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `
		SELECT count(*) FROM guardrail_trips
		WHERE programme_id = $1 AND state <> 'REARMED'`, programmeID).Scan(&n)
	return n, err
}

// SetStatus transitions a programme's lifecycle status with an explicit
// from-guard (concurrent transitions converge instead of clobbering).
func (Programmes) SetStatus(ctx context.Context, tx pgx.Tx, programmeID string, from, to entity.ProgrammeStatus) error {
	ct, err := tx.Exec(ctx, `
		UPDATE programmes SET status = $3
		WHERE programme_id = $1 AND status = $2`, programmeID, string(from), string(to))
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("programme %q not in %s: %w", programmeID, from, ErrNotFound)
	}
	return nil
}

// GetStatus reads a programme's current status (offer/confirm gate).
func (Programmes) GetStatus(ctx context.Context, tx pgx.Tx, programmeID string) (entity.ProgrammeStatus, error) {
	var s string
	err := tx.QueryRow(ctx,
		`SELECT status FROM programmes WHERE programme_id = $1`, programmeID).Scan(&s)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("programme %q: %w", programmeID, ErrNotFound)
	}
	return entity.ProgrammeStatus(s), err
}
