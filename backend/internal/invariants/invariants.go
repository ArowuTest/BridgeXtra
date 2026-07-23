// Package invariants is the BC-3 standing invariant checker: the ledger and
// exposure invariants as set-based SQL sweeps, runnable (a) in CI as part of
// the property pack, (b) on demand by an operator (worker -invariants), and
// (c) by the daily control cycle (V3-BOP-006). "We can prove the ledger
// balances at any instant" is the sales claim this package backs.
//
// Every check returns VIOLATIONS (empty = healthy) — the checker never
// repairs. Read-only by construction: it runs on the worker pool (BYPASSRLS,
// SELECT-only paths) and touches nothing.
package invariants

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Violation is one broken invariant instance.
type Violation struct {
	Invariant string // e.g. "INV-004"
	Subject   string // offending id
	Detail    string
}

func (v Violation) String() string {
	return fmt.Sprintf("%s [%s]: %s", v.Invariant, v.Subject, v.Detail)
}

// Checker runs the invariant sweeps.
type Checker struct{ Pool *pgxpool.Pool }

// checkSpec: each invariant is ONE set-based query returning offenders.
// Adding an invariant = adding a row here; the property test and the
// operator job pick it up automatically.
var checks = []struct {
	invariant string
	detail    string
	query     string
}{
	{
		"INV-004", "journal entries do not sum to zero per currency (V2-LED-001)",
		`SELECT journal_id FROM journal_entries
		 GROUP BY journal_id, currency
		 HAVING SUM(debit_minor) <> SUM(credit_minor)`,
	},
	{
		"INV-006", "advance outstanding below zero (V2-COL-003)",
		`SELECT advance_id FROM advances WHERE outstanding_minor < 0`,
	},
	{
		"INV-001", "money-bearing advance without a CONFIRMED fulfilment attempt",
		`SELECT a.advance_id FROM advances a
		 WHERE a.state IN ('ACTIVE','PARTIALLY_RECOVERED','CLOSED')
		   AND NOT EXISTS (
		     SELECT 1 FROM fulfilment_attempts fa
		     WHERE fa.advance_id = a.advance_id AND fa.state = 'CONFIRMED')`,
	},
	{
		"INV-001b", "money-bearing advance without its ADVANCE_ISSUED journal (V2-LED-006 converse)",
		`SELECT a.advance_id FROM advances a
		 WHERE a.state IN ('ACTIVE','PARTIALLY_RECOVERED','CLOSED')
		   AND NOT EXISTS (
		     SELECT 1 FROM journals j
		     WHERE j.advance_id = a.advance_id AND j.event_type = 'ADVANCE_ISSUED')`,
	},
	{
		"INV-013", "journal posted for an advance that never confirmed (V2-LED-006)",
		`SELECT j.advance_id FROM journals j
		 JOIN advances a ON a.advance_id = j.advance_id
		 WHERE j.event_type = 'ADVANCE_ISSUED'
		   AND a.state IN ('REQUESTED','VALIDATED','EXPOSURE_RESERVED',
		                   'PENDING_FULFILMENT','FULFILMENT_UNKNOWN',
		                   'FULFILMENT_FAILED','DECLINED')`,
	},
	{
		"INV-014", "advance arithmetic broken: outstanding != repayment - recovered",
		`SELECT a.advance_id FROM advances a
		 LEFT JOIN LATERAL (
		   SELECT COALESCE(SUM(amount_minor),0) AS recovered
		   FROM recovery_allocations ra WHERE ra.advance_id = a.advance_id) r ON true
		 WHERE a.state IN ('ACTIVE','PARTIALLY_RECOVERED','CLOSED')
		   AND a.outstanding_minor <> (
		     CASE WHEN EXISTS (SELECT 1 FROM offers o WHERE o.offer_id = a.offer_id
		                        AND o.fee_model = 'ADDED_TO_REPAYMENT')
		          THEN a.face_value_minor + a.fee_minor
		          ELSE a.face_value_minor END) - r.recovered`,
	},
	{
		"INV-002", "funding pool over-allocated (reserved+utilised > committed)",
		`SELECT pool_id FROM funding_pools
		 WHERE reserved_minor + utilised_minor > committed_minor
		    OR reserved_minor < 0 OR utilised_minor < 0`,
	},
	{
		"INV-015", "pool utilisation != sum of outstanding on its open money-bearing advances",
		`SELECT fp.pool_id FROM funding_pools fp
		 LEFT JOIN LATERAL (
		   SELECT COALESCE(SUM(a.outstanding_minor),0) AS open_outstanding
		   FROM advances a
		   WHERE a.funding_pool_id = fp.pool_id
		     AND a.state IN ('ACTIVE','PARTIALLY_RECOVERED')) x ON true
		 WHERE fp.utilised_minor <> x.open_outstanding`,
	},
	{
		"INV-016", "SUBSCRIBER_RECEIVABLE ledger balance != total outstanding (V2-LED-008 cross-check)",
		`SELECT 'ledger-vs-book' WHERE
		   (SELECT COALESCE(SUM(debit_minor - credit_minor),0)
		    FROM journal_entries WHERE account_code = 'SUBSCRIBER_RECEIVABLE')
		   <>
		   (SELECT COALESCE(SUM(outstanding_minor),0)
		    FROM advances WHERE state IN ('ACTIVE','PARTIALLY_RECOVERED'))`,
	},
	{
		"INV-017", "recovery event over-allocated (allocations exceed event amount)",
		`SELECT re.recovery_event_id FROM recovery_events re
		 JOIN (SELECT recovery_event_id, SUM(amount_minor) AS allocated
		       FROM recovery_allocations GROUP BY recovery_event_id) ra
		   ON ra.recovery_event_id = re.recovery_event_id
		 WHERE ra.allocated > re.amount_minor`,
	},
	{
		"INV-003", "duplicate journal for one business event",
		`SELECT business_event_key FROM journals
		 GROUP BY business_event_key, event_type HAVING count(*) > 1`,
	},
	{
		"INV-018", "UNEARNED_FEE has a net-debit (negative liability) balance — deferred fee over-reversed",
		`SELECT currency FROM journal_entries
		 WHERE account_code = 'UNEARNED_FEE'
		 GROUP BY currency HAVING SUM(debit_minor - credit_minor) > 0`,
	},
	{
		"INV-019", "UNEARNED_FEE ledger balance != booked-remaining unearned fee over live DEFERRED advances (deferred-fee cross-check)",
		// Deferred fee credited to UNEARNED_FEE at origination is drawn down as
		// the waterfall allocates the fee-portion; the liability's live balance
		// (credit-normal) must equal fee_minus-recovered-fee summed over advances
		// still live and pinned DEFERRED. CLOSED advances net to 0 (fee fully
		// recognised); WRITTEN_OFF advances are reversed to 0 by write-off and are
		// excluded from the book side — both sides contribute 0. Single-currency
		// book assumption, same as INV-016.
		`SELECT 'UNEARNED_FEE' WHERE
		   (SELECT COALESCE(SUM(credit_minor - debit_minor),0)
		      FROM journal_entries WHERE account_code = 'UNEARNED_FEE')
		   <>
		   (SELECT COALESCE(SUM(a.fee_minor - COALESCE(fa.net_fee,0)),0)
		      FROM advances a
		      LEFT JOIN (SELECT advance_id, SUM(amount_minor) AS net_fee
		                   FROM recovery_allocations WHERE component = 'FEE'
		                   GROUP BY advance_id) fa ON fa.advance_id = a.advance_id
		     WHERE a.fee_recognition = 'DEFERRED'
		       AND a.state IN ('ACTIVE','PARTIALLY_RECOVERED'))`,
	},
}

// Check runs every invariant sweep and returns all violations found.
func (c *Checker) Check(ctx context.Context) ([]Violation, error) {
	var out []Violation
	for _, chk := range checks {
		rows, err := c.Pool.Query(ctx, chk.query)
		if err != nil {
			return nil, fmt.Errorf("invariant %s query: %w", chk.invariant, err)
		}
		for rows.Next() {
			var subject string
			if err := rows.Scan(&subject); err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, Violation{Invariant: chk.invariant, Subject: subject, Detail: chk.detail})
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}
