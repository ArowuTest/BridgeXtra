package repo

// M4c operator reads for the portal risk workspace. These run on the WORKER
// pool (BYPASSRLS): a platform operator's authority is their SCOPE, not a
// single RLS tenant, so these queries span telcos and enforce the operator's
// scope as a MANDATORY SQL filter (the same zero-config-floor discipline as
// config restrict — absent filter is never "see everything" for a scoped
// operator; '*' passes empty bounds deliberately). The subscriber-facing app
// role stays RLS'd as everywhere else.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const tripCols = `trip_id, telco_id, programme_id, guardrail, measured_minor, limit_minor, currency,
	state, tripped_at, COALESCE(rearm_requested_by,''), COALESCE(rearm_approved_by,''), rearmed_at`

func scanTrip(row pgx.Row) (GuardrailTrip, error) {
	var t GuardrailTrip
	var measured, limit int64
	var cur string
	if err := row.Scan(&t.TripID, &t.TelcoID, &t.ProgrammeID, &t.Guardrail, &measured, &limit, &cur,
		&t.State, &t.TrippedAt, &t.RearmRequestedBy, &t.RearmApprovedBy, &t.RearmedAt); err != nil {
		return t, err
	}
	var err error
	if t.Measured, err = scanMoney(measured, cur); err != nil {
		return t, err
	}
	t.Limit, err = scanMoney(limit, cur)
	return t, err
}

// ListOpenTrips returns trips still holding a programme (state <> 'REARMED'),
// newest-first, bounded to the operator's telco/programme scope. Empty bounds
// (a '*' operator) return every open trip.
func ListOpenTrips(ctx context.Context, pool *pgxpool.Pool, restrictTelco, restrictProgramme string) ([]GuardrailTrip, error) {
	rows, err := pool.Query(ctx, `
		SELECT `+tripCols+`
		FROM guardrail_trips
		WHERE state <> 'REARMED'
		  AND ($1 = '' OR telco_id = $1)
		  AND ($2 = '' OR programme_id = $2)
		ORDER BY tripped_at DESC`, restrictTelco, restrictProgramme)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GuardrailTrip
	for rows.Next() {
		t, err := scanTrip(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// GetTripByID loads one trip on the operator-read pool. The caller authorizes
// against the returned telco/programme scope before acting (a cross-scope
// lookup must surface as a no-oracle 404, not a 403 that leaks existence).
func GetTripByID(ctx context.Context, pool *pgxpool.Pool, tripID string) (GuardrailTrip, error) {
	t, err := scanTrip(pool.QueryRow(ctx, `SELECT `+tripCols+` FROM guardrail_trips WHERE trip_id = $1`, tripID))
	if errors.Is(err, pgx.ErrNoRows) {
		return GuardrailTrip{}, fmt.Errorf("guardrail trip %q: %w", tripID, ErrNotFound)
	}
	return t, err
}
