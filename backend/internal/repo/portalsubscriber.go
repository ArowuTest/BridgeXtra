package repo

// Wave B.2 — Subscriber-360 read-model. The MSISDN is the account number, so this
// assembles ONE subscriber's whole picture, keyed by MSISDN, from the SAME sources
// as the loan book — one truth: per-loan and total "still owed" use the identical
// outstanding basis (Σ outstanding over ACTIVE/PARTIALLY_RECOVERED) that cross-foots
// to SUBSCRIBER_RECEIVABLE (INV-016), so the 360 and the ledger can never disagree.
//
// Every function runs on the tx passed by OperatorReader.Read (the RLS-enforced
// tcp_operator pool), so BOTH the directory search AND the profile are telco-scoped
// by the DB — a telco operator sees only their subscribers, an admin the estate, a
// no-authority operator nothing. The MSISDN token is carried raw here; the handler
// masks it at egress and audits any full reveal.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
)

// escapeLike escapes the LIKE metacharacters so a query is matched literally under
// `LIKE ... ESCAPE '\'` — a user typing '%' or '_' means those digits, not a wildcard.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// SubscriberDirectoryRow is one row of the searchable MSISDN directory: identity +
// the per-subscriber money rollup, on the loan-book outstanding basis.
type SubscriberDirectoryRow struct {
	SubscriberAccountID string
	TelcoID             string
	MSISDNToken         string // handler masks
	Status              string
	OpenLoans           int64
	TotalOutstanding    entity.Money // Σ outstanding over ACTIVE/PARTIALLY_RECOVERED (loan-book basis)
	EverBorrowed        entity.Money // Σ disbursed
	EverRepaid          entity.Money // Σ recovery_allocations (FEE+PRINCIPAL)
	WorstBucket         string       // worst delinquency bucket across open loans ("" if none)
	LastRechargeAt      *time.Time
}

// SearchSubscribers returns live subscribers whose MSISDN matches the query — exact,
// or a trailing-digits suffix (the last-N the admin sees masked) — newest-active
// first, with the per-subscriber money rollup. An empty query lists the book (capped).
// Scope-bound by the tx RLS; a no-authority operator returns nothing.
func SearchSubscribers(ctx context.Context, q Querier, scope OperatorScope, query string, limit int) ([]SubscriberDirectoryRow, error) {
	if !scope.authority {
		return nil, nil
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	// A blank query lists everyone in scope. A non-blank query matches EITHER the full
	// MSISDN (exact) OR a trailing-digits suffix (the tail an admin sees masked) — these
	// are OR-ed, not AND-ed, so a suffix search isn't defeated by the exact clause. LIKE
	// metacharacters in the query are escaped ($2) so a '%'/'_' is matched literally, not
	// as a wildcard (no LIKE-injection, no accidental match-all).
	rows, err := q.Query(ctx, `
SELECT s.subscriber_account_id, s.telco_id, s.msisdn_token, s.status,
       COALESCE(COUNT(a.advance_id) FILTER (WHERE a.state IN ('ACTIVE','PARTIALLY_RECOVERED')), 0) AS open_loans,
       COALESCE(SUM(a.outstanding_minor) FILTER (WHERE a.state IN ('ACTIVE','PARTIALLY_RECOVERED')), 0) AS outstanding,
       COALESCE(SUM(a.disbursed_minor), 0) AS ever_borrowed,
       COALESCE(SUM(rp.repaid_minor), 0) AS ever_repaid,
       COALESCE(MAX(a.currency), 'NGN') AS currency,
       COALESCE(MAX(a.delinquency_bucket) FILTER (WHERE a.state IN ('ACTIVE','PARTIALLY_RECOVERED')), '') AS worst_bucket,
       (SELECT MAX(re.occurred_at) FROM recovery_events re
        WHERE re.subscriber_account_id = s.subscriber_account_id OR re.msisdn_token = s.msisdn_token) AS last_recharge_at
FROM subscriber_accounts s
LEFT JOIN advances a ON a.subscriber_account_id = s.subscriber_account_id
LEFT JOIN LATERAL (
  SELECT SUM(ra.amount_minor) AS repaid_minor
  FROM recovery_allocations ra
  WHERE ra.advance_id = a.advance_id AND ra.component IN ('FEE','PRINCIPAL')
) rp ON true
WHERE s.effective_to IS NULL
  AND ($1 = '' OR s.msisdn_token = $1 OR s.msisdn_token LIKE '%' || $2 ESCAPE '\')
GROUP BY s.subscriber_account_id, s.telco_id, s.msisdn_token, s.status
ORDER BY outstanding DESC, s.effective_from DESC
LIMIT $3`, query, escapeLike(query), limit)
	if err != nil {
		return nil, fmt.Errorf("subscriber search: %w", err)
	}
	defer rows.Close()
	var out []SubscriberDirectoryRow
	for rows.Next() {
		var r SubscriberDirectoryRow
		var outstanding, borrowed, repaid int64
		var cur string
		if err := rows.Scan(&r.SubscriberAccountID, &r.TelcoID, &r.MSISDNToken, &r.Status,
			&r.OpenLoans, &outstanding, &borrowed, &repaid, &cur, &r.WorstBucket, &r.LastRechargeAt); err != nil {
			return nil, fmt.Errorf("scan subscriber row: %w", err)
		}
		var e error
		if r.TotalOutstanding, e = scanMoney(outstanding, cur); e != nil {
			return nil, e
		}
		if r.EverBorrowed, e = scanMoney(borrowed, cur); e != nil {
			return nil, e
		}
		if r.EverRepaid, e = scanMoney(repaid, cur); e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// --- the full 360 profile ---------------------------------------------------

// SubscriberIdentity is the header of the 360.
type SubscriberIdentity struct {
	SubscriberAccountID string
	TelcoID             string
	MSISDNToken         string // handler masks (full only via audited reveal)
	Status              string
	EffectiveFrom       time.Time
}

// LimitPoint is one entry in the credit limit + tier history (decision_snapshots),
// newest first — how the subscriber's standing changed over time and why.
type LimitPoint struct {
	Limit      entity.Money
	TierCode   string
	PriorTier  string // "" = no prior at scoring time
	ConfigVer  string // pinned decision inputs (the "reason"/provenance)
	IsCurrent  bool
	ScoredAt   time.Time
	ValidUntil *time.Time
}

// SubscriberLoan reuses the loan-book row shape so per-loan outstanding is the exact
// same figure as the ledger (one truth). The handler formats money + masks nothing
// here (advance-level, no token).
type SubscriberLoan struct {
	AdvanceID         string
	ProgrammeID       string
	State             string
	DelinquencyBucket string
	Disbursed         entity.Money
	Outstanding       entity.Money
	Recovered         entity.Money
	AcceptedAt        time.Time
	ActivatedAt       *time.Time
	ClosedAt          *time.Time
}

// RechargeEvent is one recharge that came in for this subscriber (money in), with how
// much of it was applied to loans.
type RechargeEvent struct {
	Amount     entity.Money
	State      string       // ALLOCATED / QUARANTINED / UNMATCHED / PENDING
	Applied    entity.Money // Σ recovery_allocations sourced from this event
	OccurredAt time.Time
}

// RepaymentEvent is one repayment applied to one loan (money toward a debt).
type RepaymentEvent struct {
	AdvanceID string
	Component string // FEE | PRINCIPAL
	Amount    entity.Money
	AppliedAt time.Time
}

// SubscriberProfileResult is the whole 360.
type SubscriberProfileResult struct {
	Identity SubscriberIdentity
	Current  *LimitPoint // nil if never scored
	History  []LimitPoint
	Loans    []SubscriberLoan
	// TotalOutstanding is the per-subscriber "still owed", Σ outstanding over
	// ACTIVE/PARTIALLY_RECOVERED loans — the SAME basis as the loan book, so it
	// cross-foots to this subscriber's slice of SUBSCRIBER_RECEIVABLE (INV-016).
	TotalOutstanding entity.Money
	Recharges        []RechargeEvent
	Repayments       []RepaymentEvent
}

// GetSubscriberProfile assembles the 360 for one subscriber, keyed by the opaque
// subscriber_account_id (NOT the MSISDN — the directory returns the id, the token
// stays masked, and the full number is only ever exposed via the audited reveal).
// Unknown id / out-of-scope / no-authority all return ErrNotFound (no oracle). Loans
// use the loan-book outstanding basis; the caller reconciles the total to
// SUBSCRIBER_RECEIVABLE.
func GetSubscriberProfile(ctx context.Context, q Querier, scope OperatorScope, subscriberAccountID string) (SubscriberProfileResult, error) {
	var res SubscriberProfileResult
	if !scope.authority {
		return res, fmt.Errorf("subscriber %q: %w", subscriberAccountID, ErrNotFound)
	}

	// Identity (live row).
	id := &res.Identity
	err := q.QueryRow(ctx, `
SELECT subscriber_account_id, telco_id, msisdn_token, status, effective_from
FROM subscriber_accounts
WHERE subscriber_account_id = $1 AND effective_to IS NULL`, subscriberAccountID).
		Scan(&id.SubscriberAccountID, &id.TelcoID, &id.MSISDNToken, &id.Status, &id.EffectiveFrom)
	if errors.Is(err, pgx.ErrNoRows) {
		return res, fmt.Errorf("subscriber %q: %w", subscriberAccountID, ErrNotFound)
	}
	if err != nil {
		return res, fmt.Errorf("subscriber identity: %w", err)
	}
	sub := id.SubscriberAccountID

	// Limit + tier history (decision_snapshots), newest first.
	hrows, err := q.Query(ctx, `
SELECT max_face_value_minor, currency, tier_code, COALESCE(prior_tier_code,''),
       config_version_id, is_current, created_at, valid_until
FROM decision_snapshots WHERE subscriber_account_id = $1
ORDER BY created_at DESC`, sub)
	if err != nil {
		return res, fmt.Errorf("limit history: %w", err)
	}
	defer hrows.Close()
	for hrows.Next() {
		var p LimitPoint
		var minor int64
		var cur string
		if err := hrows.Scan(&minor, &cur, &p.TierCode, &p.PriorTier, &p.ConfigVer,
			&p.IsCurrent, &p.ScoredAt, &p.ValidUntil); err != nil {
			return res, fmt.Errorf("scan limit point: %w", err)
		}
		if p.Limit, err = scanMoney(minor, cur); err != nil {
			return res, err
		}
		res.History = append(res.History, p)
		if p.IsCurrent {
			cp := p
			res.Current = &cp
		}
	}
	if err := hrows.Err(); err != nil {
		return res, err
	}

	// All loans (advances) — loan-book basis, with recovered net of reversals.
	lrows, err := q.Query(ctx, `
SELECT a.advance_id, a.programme_id, a.state, COALESCE(a.delinquency_bucket,''),
       a.disbursed_minor, a.outstanding_minor, COALESCE(r.recovered_minor, 0), a.currency,
       a.accepted_at, a.activated_at, a.closed_at
FROM advances a
LEFT JOIN LATERAL (
  SELECT SUM(ra.amount_minor) AS recovered_minor
  FROM recovery_allocations ra
  WHERE ra.advance_id = a.advance_id AND ra.component IN ('FEE','PRINCIPAL')
) r ON true
WHERE a.subscriber_account_id = $1
ORDER BY a.accepted_at DESC`, sub)
	if err != nil {
		return res, fmt.Errorf("loans: %w", err)
	}
	defer lrows.Close()
	var owedMinor int64
	owedCur := "NGN"
	for lrows.Next() {
		var l SubscriberLoan
		var disb, out, rec int64
		var cur string
		if err := lrows.Scan(&l.AdvanceID, &l.ProgrammeID, &l.State, &l.DelinquencyBucket,
			&disb, &out, &rec, &cur, &l.AcceptedAt, &l.ActivatedAt, &l.ClosedAt); err != nil {
			return res, fmt.Errorf("scan loan: %w", err)
		}
		var e error
		if l.Disbursed, e = scanMoney(disb, cur); e != nil {
			return res, e
		}
		if l.Outstanding, e = scanMoney(out, cur); e != nil {
			return res, e
		}
		if l.Recovered, e = scanMoney(rec, cur); e != nil {
			return res, e
		}
		// "Still owed" on the loan-book basis — the figure that reconciles to the ledger.
		if l.State == "ACTIVE" || l.State == "PARTIALLY_RECOVERED" {
			owedMinor += out
			owedCur = cur
		}
		res.Loans = append(res.Loans, l)
	}
	if err := lrows.Err(); err != nil {
		return res, err
	}
	if res.TotalOutstanding, err = scanMoney(owedMinor, owedCur); err != nil {
		return res, err
	}

	// Recharges in (recovery_events), with how much of each was applied to loans. Match
	// by the resolved subscriber_account_id OR the msisdn_token (the account number), so
	// recharges that came in for this number but aren't yet resolver-linked still appear
	// — the account IS the phone number.
	rrows, err := q.Query(ctx, `
SELECT re.amount_minor, re.currency, re.state, re.occurred_at,
       COALESCE((SELECT SUM(ra.amount_minor) FROM recovery_allocations ra
                 WHERE ra.recovery_event_id = re.recovery_event_id), 0) AS applied
FROM recovery_events re
WHERE re.subscriber_account_id = $1 OR re.msisdn_token = $2
ORDER BY re.occurred_at DESC LIMIT 200`, sub, id.MSISDNToken)
	if err != nil {
		return res, fmt.Errorf("recharges: %w", err)
	}
	defer rrows.Close()
	for rrows.Next() {
		var rc RechargeEvent
		var amt, applied int64
		var cur string
		if err := rrows.Scan(&amt, &cur, &rc.State, &rc.OccurredAt, &applied); err != nil {
			return res, fmt.Errorf("scan recharge: %w", err)
		}
		var e error
		if rc.Amount, e = scanMoney(amt, cur); e != nil {
			return res, e
		}
		if rc.Applied, e = scanMoney(applied, cur); e != nil {
			return res, e
		}
		res.Recharges = append(res.Recharges, rc)
	}
	if err := rrows.Err(); err != nil {
		return res, err
	}

	// Repayments (recovery_allocations) applied to this subscriber's loans.
	prows, err := q.Query(ctx, `
SELECT ra.advance_id, ra.component, ra.amount_minor, ra.currency, ra.created_at
FROM recovery_allocations ra
JOIN advances a ON a.advance_id = ra.advance_id
WHERE a.subscriber_account_id = $1
ORDER BY ra.created_at DESC LIMIT 200`, sub)
	if err != nil {
		return res, fmt.Errorf("repayments: %w", err)
	}
	defer prows.Close()
	for prows.Next() {
		var rp RepaymentEvent
		var amt int64
		var cur string
		if err := prows.Scan(&rp.AdvanceID, &rp.Component, &amt, &cur, &rp.AppliedAt); err != nil {
			return res, fmt.Errorf("scan repayment: %w", err)
		}
		var e error
		if rp.Amount, e = scanMoney(amt, cur); e != nil {
			return res, e
		}
		res.Repayments = append(res.Repayments, rp)
	}
	return res, prows.Err()
}
