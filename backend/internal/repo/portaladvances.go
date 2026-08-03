package repo

// Wave B.1 — loan-book read-model. Scope-bound operator reads over the advances book:
// a keyset-paginated list, a SQL summary aggregate (counts + portfolio money totals +
// the ledger cross-foot that proves outstanding reconciles), a single-advance detail,
// and the per-advance ledger history (paydown timeline + running outstanding).
//
// EVERY function runs on the tx passed by OperatorReader.Read (the RLS-enforced
// tcp_operator pool with app.telco_id / app.op_all set LOCAL from the trusted session
// scope), so BOTH the rows AND the SUM/COUNT aggregates are telco-scoped by the DB —
// a telco operator's totals are their telco only, an admin's span the estate, with no
// second scoping mechanism. A read that reaches here with no authority returns nothing
// (fail-closed). Money crosses as entity.Money; the handler masks the MSISDN token.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
)

// AdvanceRow is one loan-book row (list) or the header of the detail. It carries the
// FULL msisdn_token — the handler masks it at egress (never store/return raw here).
type AdvanceRow struct {
	AdvanceID         string
	TelcoID           string
	ProgrammeID       string
	State             string
	DelinquencyBucket string // "" when unclassified
	BucketAsOf        *time.Time
	FeeRecognition    string
	MSISDNToken       string
	FaceValue         entity.Money
	Fee               entity.Money
	Disbursed         entity.Money
	Outstanding       entity.Money
	Recovered         entity.Money // net of reversals (Σ recovery_allocations)
	AcceptedAt        time.Time
	ActivatedAt       *time.Time
	ClosedAt          *time.Time
}

// AdvanceFilter is the loan-book query filter. Empty fields are ignored. Cursor is the
// last advance_id of the previous page (keyset); empty = first page.
type AdvanceFilter struct {
	Telco       string
	ProgrammeID string
	State       string
	Bucket      string
	MSISDNToken string // exact token match
	DateFrom    string // activated_at >= (YYYY-MM-DD, inclusive)
	DateTo      string // activated_at <  DateTo+1 (YYYY-MM-DD, inclusive day)
	Cursor      string
	Limit       int
}

const advanceListSQL = `
SELECT a.advance_id, a.telco_id, a.programme_id, a.state,
       COALESCE(a.delinquency_bucket, ''), a.bucket_as_of, COALESCE(a.fee_recognition, 'UPFRONT'),
       sa.msisdn_token,
       a.face_value_minor, a.fee_minor, a.disbursed_minor, a.outstanding_minor,
       COALESCE(r.recovered_minor, 0), a.currency,
       a.accepted_at, a.activated_at, a.closed_at
FROM advances a
JOIN subscriber_accounts sa ON sa.subscriber_account_id = a.subscriber_account_id
LEFT JOIN LATERAL (
  SELECT SUM(amount_minor) AS recovered_minor
  FROM recovery_allocations ra
  WHERE ra.advance_id = a.advance_id AND ra.component IN ('FEE', 'PRINCIPAL')
) r ON true
WHERE ($1 = '' OR a.telco_id = $1)
  AND ($2 = '' OR a.programme_id = $2)
  AND ($3 = '' OR a.state = $3)
  AND ($4 = '' OR a.delinquency_bucket = $4)
  AND ($5 = '' OR sa.msisdn_token = $5)
  AND ($6 = '' OR a.activated_at >= $6::date)
  AND ($7 = '' OR a.activated_at < ($7::date + 1))
  AND ($8 = '' OR a.advance_id < $8)
ORDER BY a.advance_id DESC
LIMIT $9`

// ListAdvances returns one keyset page of the loan book, newest-first (advance_id is a
// time-ordered ULID backed by the PK index, so it is a stable, indexed cursor).
func ListAdvances(ctx context.Context, q Querier, scope OperatorScope, f AdvanceFilter) ([]AdvanceRow, error) {
	if !scope.authority {
		return nil, nil // fail-closed: no authority => no rows, no query cost
	}
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := q.Query(ctx, advanceListSQL,
		f.Telco, f.ProgrammeID, f.State, f.Bucket, f.MSISDNToken, f.DateFrom, f.DateTo, f.Cursor, limit)
	if err != nil {
		return nil, fmt.Errorf("list advances: %w", err)
	}
	defer rows.Close()
	var out []AdvanceRow
	for rows.Next() {
		r, err := scanAdvanceRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func scanAdvanceRow(row pgx.Row) (AdvanceRow, error) {
	var a AdvanceRow
	var faceMinor, feeMinor, disbMinor, outMinor, recMinor int64
	var currency string
	if err := row.Scan(
		&a.AdvanceID, &a.TelcoID, &a.ProgrammeID, &a.State,
		&a.DelinquencyBucket, &a.BucketAsOf, &a.FeeRecognition,
		&a.MSISDNToken,
		&faceMinor, &feeMinor, &disbMinor, &outMinor, &recMinor, &currency,
		&a.AcceptedAt, &a.ActivatedAt, &a.ClosedAt,
	); err != nil {
		return AdvanceRow{}, fmt.Errorf("scan advance: %w", err)
	}
	var e error
	if a.FaceValue, e = scanMoney(faceMinor, currency); e != nil {
		return AdvanceRow{}, e
	}
	if a.Fee, e = scanMoney(feeMinor, currency); e != nil {
		return AdvanceRow{}, e
	}
	if a.Disbursed, e = scanMoney(disbMinor, currency); e != nil {
		return AdvanceRow{}, e
	}
	if a.Outstanding, e = scanMoney(outMinor, currency); e != nil {
		return AdvanceRow{}, e
	}
	if a.Recovered, e = scanMoney(recMinor, currency); e != nil {
		return AdvanceRow{}, e
	}
	return a, nil
}

// GetAdvanceByID loads one advance, scoped. Unknown or out-of-scope both return
// ErrNotFound (no oracle) — the RLS tx already removes out-of-scope rows.
func GetAdvanceByID(ctx context.Context, q Querier, scope OperatorScope, advanceID string) (AdvanceRow, error) {
	if !scope.authority {
		return AdvanceRow{}, fmt.Errorf("advance %q: %w", advanceID, ErrNotFound)
	}
	// Same projection as the list, keyed by id (no filters/keyset).
	row := q.QueryRow(ctx, `
SELECT a.advance_id, a.telco_id, a.programme_id, a.state,
       COALESCE(a.delinquency_bucket, ''), a.bucket_as_of, COALESCE(a.fee_recognition, 'UPFRONT'),
       sa.msisdn_token,
       a.face_value_minor, a.fee_minor, a.disbursed_minor, a.outstanding_minor,
       COALESCE(r.recovered_minor, 0), a.currency,
       a.accepted_at, a.activated_at, a.closed_at
FROM advances a
JOIN subscriber_accounts sa ON sa.subscriber_account_id = a.subscriber_account_id
LEFT JOIN LATERAL (
  SELECT SUM(amount_minor) AS recovered_minor
  FROM recovery_allocations ra
  WHERE ra.advance_id = a.advance_id AND ra.component IN ('FEE', 'PRINCIPAL')
) r ON true
WHERE a.advance_id = $1`, advanceID)
	a, err := scanAdvanceRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return AdvanceRow{}, fmt.Errorf("advance %q: %w", advanceID, ErrNotFound)
	}
	return a, err
}

// --- summary aggregate ------------------------------------------------------

// LoanBookSummaryResult is the SQL-computed portfolio summary for the scoped set (the
// SAME filter as the list, minus the keyset cursor). Money is server-computed — the
// client never sums.
type LoanBookSummaryResult struct {
	TotalCount       int64
	ByStatus         map[string]int64
	ByBucket         map[string]int64        // "UNCLASSIFIED" folds NULL bucket
	ByBucketValue    map[string]entity.Money // ₦ outstanding per bucket (arrears by bucket) — same FILTER as OpenOutstanding
	Disbursed        entity.Money
	Recovered        entity.Money
	OpenOutstanding  entity.Money // Σ outstanding over ACTIVE/PARTIALLY_RECOVERED (the book side of INV-016)
	LedgerReceivable entity.Money // ledger SUBSCRIBER_RECEIVABLE balance — must equal OpenOutstanding
	// RevenueRecognized is the ledger FEE_INCOME balance (credit − debit): fee revenue
	// EARNED to date, net of reversals. Under DEFERRED the fee sits in UNEARNED_FEE until
	// recovery earns it, so this is revenue MADE — reported BESIDE outstanding, never
	// inside it. Same journal_entries + telco scope as the receivable cross-foot (one truth).
	RevenueRecognized entity.Money
	Currency          string
}

// LoanBookSummary computes counts-by-status, counts-by-bucket, portfolio money totals,
// and the ledger cross-foot in the scoped tx. The GROUPING SETS give per-status,
// per-bucket, and grand-total rows in one pass; the ledger receivable is a second
// scoped query (journal_entries is telco-scoped through journals under the same tx).
func LoanBookSummary(ctx context.Context, q Querier, scope OperatorScope, f AdvanceFilter) (LoanBookSummaryResult, error) {
	res := LoanBookSummaryResult{ByStatus: map[string]int64{}, ByBucket: map[string]int64{}, ByBucketValue: map[string]entity.Money{}, Currency: "NGN"}
	if !scope.authority {
		res.Disbursed = entity.MustMoney(0, entity.NGN)
		res.Recovered = res.Disbursed
		res.OpenOutstanding = res.Disbursed
		res.LedgerReceivable = res.Disbursed
		res.RevenueRecognized = res.Disbursed
		return res, nil
	}
	byBucketMinor := map[string]int64{}

	rows, err := q.Query(ctx, `
SELECT GROUPING(a.state) AS g_state, GROUPING(a.delinquency_bucket) AS g_bucket,
       COALESCE(a.state, '') AS state, COALESCE(a.delinquency_bucket, 'UNCLASSIFIED') AS bucket,
       COUNT(*) AS n,
       COALESCE(SUM(a.disbursed_minor), 0) AS disbursed,
       COALESCE(SUM(a.outstanding_minor) FILTER (WHERE a.state IN ('ACTIVE', 'PARTIALLY_RECOVERED')), 0) AS open_outstanding,
       COALESCE(SUM(COALESCE(r.recovered_minor, 0)), 0) AS recovered,
       MAX(a.currency) AS currency
FROM advances a
LEFT JOIN LATERAL (
  SELECT SUM(amount_minor) AS recovered_minor
  FROM recovery_allocations ra
  WHERE ra.advance_id = a.advance_id AND ra.component IN ('FEE', 'PRINCIPAL')
) r ON true
WHERE ($1 = '' OR a.telco_id = $1)
  AND ($2 = '' OR a.programme_id = $2)
  AND ($3 = '' OR a.state = $3)
  AND ($4 = '' OR a.delinquency_bucket = $4)
  AND ($5 = '' OR a.activated_at >= $5::date)
  AND ($6 = '' OR a.activated_at < ($6::date + 1))
GROUP BY GROUPING SETS ((a.state), (a.delinquency_bucket), ())`,
		f.Telco, f.ProgrammeID, f.State, f.Bucket, f.DateFrom, f.DateTo)
	if err != nil {
		return res, fmt.Errorf("loan-book summary: %w", err)
	}
	defer rows.Close()
	var disbursed, recovered, openOut int64
	for rows.Next() {
		var gState, gBucket int
		var state, bucket string
		var n, disb, open, rec int64
		var currency *string
		if err := rows.Scan(&gState, &gBucket, &state, &bucket, &n, &disb, &open, &rec, &currency); err != nil {
			return res, fmt.Errorf("scan summary: %w", err)
		}
		switch {
		case gState == 1 && gBucket == 1: // grand total
			res.TotalCount = n
			disbursed, openOut, recovered = disb, open, rec // match SELECT column order
			if currency != nil && *currency != "" {
				res.Currency = *currency
			}
		case gState == 0: // per-status row
			res.ByStatus[state] = n
		case gBucket == 0: // per-bucket row
			res.ByBucket[bucket] = n
			byBucketMinor[bucket] = open // ₦ arrears in this bucket (same FILTER as OpenOutstanding)
		}
	}
	if err := rows.Err(); err != nil {
		return res, err
	}

	// Ledger cross-foot: SUBSCRIBER_RECEIVABLE running balance (do NOT filter by advance
	// state — CLOSED/WRITTEN_OFF already net to 0). Telco-scoped by RLS + the optional
	// admin telco filter; must equal OpenOutstanding (INV-016).
	var ledgerMinor int64
	if err := q.QueryRow(ctx, `
SELECT COALESCE(SUM(je.debit_minor - je.credit_minor), 0)
FROM journal_entries je
JOIN journals j ON j.journal_id = je.journal_id
WHERE je.account_code = 'SUBSCRIBER_RECEIVABLE' AND ($1 = '' OR j.telco_id = $1)`,
		f.Telco).Scan(&ledgerMinor); err != nil {
		return res, fmt.Errorf("ledger cross-foot: %w", err)
	}

	// Revenue recognized: the ledger FEE_INCOME balance. FEE_INCOME is credit-normal
	// (revenue), so the balance is credit − debit, and recovery reversals that
	// de-recognize fee (DR FEE_INCOME) reduce it — i.e. earned-to-date, net. SAME
	// journal_entries and telco scope as the receivable cross-foot above (one ledger
	// truth); reported beside OpenOutstanding, never mixed into it.
	var revenueMinor int64
	if err := q.QueryRow(ctx, `
SELECT COALESCE(SUM(je.credit_minor - je.debit_minor), 0)
FROM journal_entries je
JOIN journals j ON j.journal_id = je.journal_id
WHERE je.account_code = 'FEE_INCOME' AND ($1 = '' OR j.telco_id = $1)`,
		f.Telco).Scan(&revenueMinor); err != nil {
		return res, fmt.Errorf("revenue cross-foot: %w", err)
	}

	cur := entity.Currency(res.Currency)
	var e error
	if res.Disbursed, e = scanMoney(disbursed, string(cur)); e != nil {
		return res, e
	}
	if res.Recovered, e = scanMoney(recovered, string(cur)); e != nil {
		return res, e
	}
	if res.OpenOutstanding, e = scanMoney(openOut, string(cur)); e != nil {
		return res, e
	}
	if res.LedgerReceivable, e = scanMoney(ledgerMinor, string(cur)); e != nil {
		return res, e
	}
	if res.RevenueRecognized, e = scanMoney(revenueMinor, string(cur)); e != nil {
		return res, e
	}
	for bucket, minor := range byBucketMinor {
		if res.ByBucketValue[bucket], e = scanMoney(minor, string(cur)); e != nil {
			return res, e
		}
	}
	return res, nil
}

// --- per-advance ledger history (detail timeline) ---------------------------

// AdvanceEvent is one ledger event on an advance: the SUBSCRIBER_RECEIVABLE movement
// (+ increases outstanding at issue, − reduces on recovery) and the running outstanding
// after it. Money uses the advance's currency, applied by the caller.
type AdvanceEvent struct {
	EventType          string
	PostedAt           time.Time
	ReceivableMovement int64 // signed minor units
	RunningOutstanding int64 // cumulative minor units
}

// AdvanceLedgerHistory returns the advance's ledger event timeline (ADVANCE_ISSUED,
// RECOVERY_APPLIED, RECOVERY_REVERSED, WRITE_OFF, …) ordered by posting time, with the
// running SUBSCRIBER_RECEIVABLE (= outstanding) after each — the paydown trajectory,
// anchored to the ledger so it can never diverge from the book. Scoped by the tx RLS.
func AdvanceLedgerHistory(ctx context.Context, q Querier, scope OperatorScope, advanceID string) ([]AdvanceEvent, error) {
	if !scope.authority {
		return nil, nil
	}
	rows, err := q.Query(ctx, `
WITH ev AS (
  SELECT j.journal_id, j.event_type, j.posted_at,
         COALESCE(SUM(je.debit_minor - je.credit_minor)
                  FILTER (WHERE je.account_code = 'SUBSCRIBER_RECEIVABLE'), 0) AS recv_move
  FROM journals j
  JOIN journal_entries je ON je.journal_id = j.journal_id
  WHERE j.advance_id = $1
  GROUP BY j.journal_id, j.event_type, j.posted_at
)
SELECT event_type, posted_at, recv_move,
       SUM(recv_move) OVER (ORDER BY posted_at, journal_id
                            ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW) AS running
FROM ev
ORDER BY posted_at, journal_id`, advanceID)
	if err != nil {
		return nil, fmt.Errorf("advance ledger history: %w", err)
	}
	defer rows.Close()
	var out []AdvanceEvent
	for rows.Next() {
		var e AdvanceEvent
		if err := rows.Scan(&e.EventType, &e.PostedAt, &e.ReceivableMovement, &e.RunningOutstanding); err != nil {
			return nil, fmt.Errorf("scan advance event: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
