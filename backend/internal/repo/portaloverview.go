package repo

// MI Overview (dashboard) read-model. Every function runs on the operatorRead RLS tx
// (tcp_operator), so rows AND aggregates are DB-scoped by telco; each fails closed on a
// no-authority scope. Money is server-computed; the handler formats via toMoneyView.
// The dashboard reuses LoanBookSummary for the "who owes us" spine; these add the
// money-collected-today figure, the write-off loss (for the honest paydown ratio), the
// per-programme health inputs (today-disbursed, pool exposure, state), and the last
// guardrail breach.

import (
	"context"
	"fmt"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
)

// OverviewExtras are the telco-scoped money figures the dashboard adds beyond the loan
// book: money collected today, and the face-value written off (the paydown ratio's third
// denominator term). Zero on no-authority.
type OverviewExtras struct {
	CollectedToday entity.Money // receivable reduction from RECOVERY_APPLIED postings dated today (money in)
	// WrittenOff is the FULL FACE written off (fee-inclusive) — Σ of the WRITE_OFF DR leg
	// (WRITE_OFF_EXPENSE side=DEBIT amount=AMOUNT, templates 0015/0049), the only event
	// that debits WRITE_OFF_EXPENSE. NOT the net expense (debit − credit): under DEFERRED
	// the WRITE_OFF CR FEE_UNEARNED_REVERSED leg nets the unearned fee back out, leaving a
	// principal-only figure — but recovered and open_outstanding are fee-inclusive, so the
	// paydown denominator must use the fee-inclusive face here for a consistent base (D1).
	WrittenOff entity.Money
}

// OverviewExtrasFor computes the collected-today and written-off figures, telco-scoped by
// the RLS tx plus the optional admin telco filter (mirrors the loan-book cross-foots).
func OverviewExtrasFor(ctx context.Context, q Querier, scope OperatorScope, telcoFilter string) (OverviewExtras, error) {
	zero := entity.MustMoney(0, entity.NGN)
	out := OverviewExtras{CollectedToday: zero, WrittenOff: zero}
	if !scope.authority {
		return out, nil
	}
	// Collected today: the SUBSCRIBER_RECEIVABLE reduction (credit − debit, so money IN is
	// positive) from RECOVERY_APPLIED journals dated today. Same account + telco scope as
	// the loan-book receivable cross-foot (one ledger truth), plus a date predicate.
	var collectedMinor int64
	if err := q.QueryRow(ctx, `
SELECT COALESCE(SUM(je.credit_minor - je.debit_minor), 0)
FROM journal_entries je JOIN journals j ON j.journal_id = je.journal_id
WHERE je.account_code = 'SUBSCRIBER_RECEIVABLE' AND j.event_type = 'RECOVERY_APPLIED'
  AND j.accounting_date = CURRENT_DATE AND ($1 = '' OR j.telco_id = $1)`, telcoFilter).Scan(&collectedMinor); err != nil {
		return out, fmt.Errorf("collected today: %w", err)
	}
	// Written off: the FULL FACE written off (fee-inclusive) — Σ of the WRITE_OFF DR leg
	// only (SUM(debit_minor), NOT debit − credit). WRITE_OFF is the only event that debits
	// WRITE_OFF_EXPENSE, and its DR leg is AMOUNT = full outstanding; the sole credit leg is
	// the DEFERRED FEE_UNEARNED_REVERSED, which nets the unearned fee out of the *expense*
	// but must NOT reduce the paydown denominator — recovered and open_outstanding are both
	// fee-inclusive, so the write-off term must be the fee-inclusive face for a consistent
	// base (D1). Post-write-off recoveries hit WRITEOFF_RECOVERY_INCOME, never this account,
	// and there is no write-off-reversal event, so SUM(debit_minor) is exactly Σ AMOUNT.
	var writtenOffMinor int64
	if err := q.QueryRow(ctx, `
SELECT COALESCE(SUM(je.debit_minor), 0)
FROM journal_entries je JOIN journals j ON j.journal_id = je.journal_id
WHERE je.account_code = 'WRITE_OFF_EXPENSE' AND ($1 = '' OR j.telco_id = $1)`, telcoFilter).Scan(&writtenOffMinor); err != nil {
		return out, fmt.Errorf("written off: %w", err)
	}
	var e error
	if out.CollectedToday, e = scanMoney(collectedMinor, "NGN"); e != nil {
		return out, e
	}
	if out.WrittenOff, e = scanMoney(writtenOffMinor, "NGN"); e != nil {
		return out, e
	}
	return out, nil
}

// ProgrammeHealthRow is one programme in scope (for the per-programme health tiles).
type ProgrammeHealthRow struct {
	ProgrammeID string
	TelcoID     string
	Status      string // ACTIVE / SUSPENDED
}

// ProgrammesInScope lists the programmes the operator can see (RLS-scoped; the '*' admin
// sees the estate via op_all_programmes, 0067). Empty on no-authority.
func ProgrammesInScope(ctx context.Context, q Querier, scope OperatorScope) ([]ProgrammeHealthRow, error) {
	if !scope.authority {
		return nil, nil
	}
	rows, err := q.Query(ctx, `SELECT programme_id, telco_id, status FROM programmes ORDER BY programme_id`)
	if err != nil {
		return nil, fmt.Errorf("programmes in scope: %w", err)
	}
	defer rows.Close()
	var out []ProgrammeHealthRow
	for rows.Next() {
		var r ProgrammeHealthRow
		if err := rows.Scan(&r.ProgrammeID, &r.TelcoID, &r.Status); err != nil {
			return nil, fmt.Errorf("scan programme: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// TodayDisbursedForProgramme is the guardrail's own DAILY_DISBURSED today-total — the
// EXACT SQL treasury.EvaluateInTx uses (accepted_at-based, excludes declined/failed) so
// the headroom tile can never disagree with the guardrail that would actually trip. The
// programmeID comes from a scope-resolved enumeration; the RLS tx already bounds it by
// telco, and the explicit no-authority guard fails closed as defense-in-depth (matching
// the other operator reads) even if the caller ever runs it outside the programme loop.
func TodayDisbursedForProgramme(ctx context.Context, q Querier, scope OperatorScope, programmeID string) (entity.Money, error) {
	if !scope.authority {
		return entity.MustMoney(0, entity.NGN), nil
	}
	var minor int64
	if err := q.QueryRow(ctx, `
SELECT COALESCE(SUM(disbursed_minor), 0) FROM advances
WHERE programme_id = $1
  AND accepted_at >= date_trunc('day', now())
  AND state NOT IN ('DECLINED', 'FULFILMENT_FAILED')`, programmeID).Scan(&minor); err != nil {
		return entity.Money{}, fmt.Errorf("today disbursed: %w", err)
	}
	return scanMoney(minor, "NGN")
}

// PoolExposure is a programme's funding-pool position: committed capital and the open
// exposure (reserved + utilised) against it. The handler compares exposure to
// committed × (governed exposure bps) — the ceiling is read from config, never hardcoded.
type PoolExposure struct {
	Committed entity.Money
	Reserved  entity.Money
	Utilised  entity.Money
	HasPool   bool
}

// PoolExposureForProgramme sums the programme's funding pools (funding_pools granted to
// the operator role in 0067). programmeID is scope-resolved; the RLS tx bounds funding_pools
// by telco, and the explicit no-authority guard fails closed as defense-in-depth (matching
// the other operator reads).
func PoolExposureForProgramme(ctx context.Context, q Querier, scope OperatorScope, programmeID string) (PoolExposure, error) {
	if !scope.authority {
		return PoolExposure{}, nil
	}
	var committed, reserved, utilised int64
	var has bool
	if err := q.QueryRow(ctx, `
SELECT COALESCE(SUM(committed_minor), 0), COALESCE(SUM(reserved_minor), 0),
       COALESCE(SUM(utilised_minor), 0), count(*) > 0
FROM funding_pools WHERE programme_id = $1`, programmeID).Scan(&committed, &reserved, &utilised, &has); err != nil {
		return PoolExposure{}, fmt.Errorf("pool exposure: %w", err)
	}
	out := PoolExposure{HasPool: has}
	var e error
	if out.Committed, e = scanMoney(committed, "NGN"); e != nil {
		return out, e
	}
	if out.Reserved, e = scanMoney(reserved, "NGN"); e != nil {
		return out, e
	}
	if out.Utilised, e = scanMoney(utilised, "NGN"); e != nil {
		return out, e
	}
	return out, nil
}
