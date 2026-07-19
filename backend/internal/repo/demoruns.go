package repo

// M4e-3 demo runs: immutable pointer records tying a fault-demo run to the
// REAL artifacts the ordinary origination path produced. INSERT-only by
// grant; the chain is read from the real tables (advances, attempts,
// journals, notifications) — never denormalised copies that could drift.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
)

type DemoRuns struct{}

type DemoRun struct {
	RunID         string
	TelcoID       string
	ProgrammeID   string
	Scenario      string
	MSISDNToken   string
	OfferID       string
	AdvanceID     string
	CorrelationID string
	RequestedBy   string
}

func (DemoRuns) Insert(ctx context.Context, tx pgx.Tx, r DemoRun) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO demo_runs (run_id, telco_id, programme_id, scenario, msisdn_token,
		  offer_id, advance_id, correlation_id, requested_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		r.RunID, r.TelcoID, r.ProgrammeID, r.Scenario, r.MSISDNToken,
		r.OfferID, r.AdvanceID, r.CorrelationID, r.RequestedBy)
	return err
}

// HasOpenAdvanceByToken reports whether the token's LIVE identity currently
// holds an advance in the one-active open set — the C6 pool-rotation probe.
// An unknown token reports false (the caller surfaces "not scored" later).
func (Subscribers) HasOpenAdvanceByToken(ctx context.Context, tx pgx.Tx, msisdnToken string) (bool, error) {
	var open bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (
		  SELECT 1 FROM advances a
		  JOIN subscriber_accounts s ON s.subscriber_account_id = a.subscriber_account_id
		  WHERE s.msisdn_token = $1 AND s.effective_to IS NULL
		    AND a.state IN ('REQUESTED','VALIDATED','EXPOSURE_RESERVED','PENDING_FULFILMENT',
		                    'FULFILMENT_UNKNOWN','ACTIVE','PARTIALLY_RECOVERED'))`, msisdnToken).Scan(&open)
	return open, err
}

// --- operator reads (worker pool, telco-level bound) -----------------------

type DemoRunRow struct {
	RunID         string
	TelcoID       string
	ProgrammeID   string
	Scenario      string
	MSISDNToken   string
	OfferID       string
	AdvanceID     string
	CorrelationID string
	RequestedBy   string
	CreatedAt     string // RFC3339
}

const demoRunCols = `run_id, telco_id, programme_id, scenario, msisdn_token, offer_id,
	advance_id, correlation_id, requested_by,
	to_char(created_at,'YYYY-MM-DD"T"HH24:MI:SS.USOF')`

func scanDemoRun(row pgx.Row) (DemoRunRow, error) {
	var r DemoRunRow
	err := row.Scan(&r.RunID, &r.TelcoID, &r.ProgrammeID, &r.Scenario, &r.MSISDNToken,
		&r.OfferID, &r.AdvanceID, &r.CorrelationID, &r.RequestedBy, &r.CreatedAt)
	return r, err
}

// ListDemoRuns returns runs newest-first (telco-grained: TelcoLevelBound).
func ListDemoRuns(ctx context.Context, pool *pgxpool.Pool, scope OperatorScope, limit int) ([]DemoRunRow, error) {
	telco, ok := scope.TelcoLevelBound()
	if !ok {
		return nil, nil
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := pool.Query(ctx, `
		SELECT `+demoRunCols+` FROM demo_runs
		WHERE ($1 = '' OR telco_id = $1)
		ORDER BY created_at DESC, run_id
		LIMIT $2`, telco, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DemoRunRow
	for rows.Next() {
		r, err := scanDemoRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetDemoRun loads one run within the operator's bound (no-oracle 404).
func GetDemoRun(ctx context.Context, pool *pgxpool.Pool, scope OperatorScope, runID string) (DemoRunRow, error) {
	var r DemoRunRow
	telco, ok := scope.TelcoLevelBound()
	if !ok {
		return r, fmt.Errorf("demo run %q: %w", runID, ErrNotFound)
	}
	r, err := scanDemoRun(pool.QueryRow(ctx, `
		SELECT `+demoRunCols+` FROM demo_runs
		WHERE run_id = $1 AND ($2 = '' OR telco_id = $2)`, runID, telco))
	if errors.Is(err, pgx.ErrNoRows) {
		return r, fmt.Errorf("demo run %q: %w", runID, ErrNotFound)
	}
	return r, err
}

// DemoAdvanceView is the advance snapshot in a run's artifact chain.
type DemoAdvanceView struct {
	AdvanceID   string
	State       string
	FaceValue   entity.Money
	Outstanding entity.Money
	ActivatedAt string // '' until activation
	ClosedAt    string
}

// DemoAttemptView is one fulfilment attempt in the chain.
type DemoAttemptView struct {
	AttemptID     string
	AttemptNo     int
	State         string
	TelcoRef      string
	EnquiryCount  int
	SubmittedAt   string
	ResolvedAt    string
	NextEnquiryAt string
}

// DemoNotificationView is one customer notification in the chain.
type DemoNotificationView struct {
	Kind      string
	State     string
	CreatedAt string
	SentAt    string
}

// GetDemoChain reads the run's live artifact chain from the REAL tables,
// bounded to the run's telco (the run row was already scope-loaded).
func GetDemoChain(ctx context.Context, pool *pgxpool.Pool, run DemoRunRow) (DemoAdvanceView, []DemoAttemptView, []DemoNotificationView, error) {
	var adv DemoAdvanceView
	var minor, outMinor int64
	var cur string
	err := pool.QueryRow(ctx, `
		SELECT advance_id, state, face_value_minor, outstanding_minor, currency,
		       COALESCE(to_char(activated_at,'YYYY-MM-DD"T"HH24:MI:SS.USOF'),''),
		       COALESCE(to_char(closed_at,'YYYY-MM-DD"T"HH24:MI:SS.USOF'),'')
		FROM advances WHERE advance_id = $1 AND telco_id = $2`, run.AdvanceID, run.TelcoID).
		Scan(&adv.AdvanceID, &adv.State, &minor, &outMinor, &cur, &adv.ActivatedAt, &adv.ClosedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return adv, nil, nil, fmt.Errorf("demo advance: %w", ErrNotFound)
	}
	if err != nil {
		return adv, nil, nil, err
	}
	if adv.FaceValue, err = scanMoney(minor, cur); err != nil {
		return adv, nil, nil, err
	}
	if adv.Outstanding, err = scanMoney(outMinor, cur); err != nil {
		return adv, nil, nil, err
	}

	rows, err := pool.Query(ctx, `
		SELECT attempt_id, attempt_no, state, COALESCE(telco_reference,''), enquiry_count,
		       to_char(submitted_at,'YYYY-MM-DD"T"HH24:MI:SS.USOF'),
		       COALESCE(to_char(resolved_at,'YYYY-MM-DD"T"HH24:MI:SS.USOF'),''),
		       COALESCE(to_char(next_enquiry_at,'YYYY-MM-DD"T"HH24:MI:SS.USOF'),'')
		FROM fulfilment_attempts WHERE advance_id = $1
		ORDER BY attempt_no`, run.AdvanceID)
	if err != nil {
		return adv, nil, nil, err
	}
	defer rows.Close()
	var attempts []DemoAttemptView
	for rows.Next() {
		var a DemoAttemptView
		if err := rows.Scan(&a.AttemptID, &a.AttemptNo, &a.State, &a.TelcoRef, &a.EnquiryCount,
			&a.SubmittedAt, &a.ResolvedAt, &a.NextEnquiryAt); err != nil {
			return adv, nil, nil, err
		}
		attempts = append(attempts, a)
	}
	if err := rows.Err(); err != nil {
		return adv, nil, nil, err
	}

	nrows, err := pool.Query(ctx, `
		SELECT kind, state,
		       to_char(created_at,'YYYY-MM-DD"T"HH24:MI:SS.USOF'),
		       COALESCE(to_char(sent_at,'YYYY-MM-DD"T"HH24:MI:SS.USOF'),'')
		FROM notifications WHERE advance_id = $1 AND telco_id = $2
		ORDER BY created_at`, run.AdvanceID, run.TelcoID)
	if err != nil {
		return adv, attempts, nil, err
	}
	defer nrows.Close()
	var notes []DemoNotificationView
	for nrows.Next() {
		var n DemoNotificationView
		if err := nrows.Scan(&n.Kind, &n.State, &n.CreatedAt, &n.SentAt); err != nil {
			return adv, attempts, nil, err
		}
		notes = append(notes, n)
	}
	return adv, attempts, notes, nrows.Err()
}
