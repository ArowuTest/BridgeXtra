package repo

// Wave B.3 — collections / delinquency queue read-model. A severity-ranked work-view of
// the advances that are behind: who is delinquent, how far, how much, plus the two
// compliance signals (self-exclusion, open complaint) that gate the write-off/bar actions
// in Phase B. Read-only, runs on the operatorRead RLS tx (tcp_operator) — rows AND the
// severity ordering are telco-scoped by the DB; fail-closed on no-authority.
//
// The bucket ladder is NOT hardcoded: the handler resolves the governed delinquency.buckets
// ladder and passes the ordered codes + ranks (and the delinquent subset) in. The severity
// sort is worst-bucket → live-DPD → outstanding, keyset-paginated by a composite cursor (the
// advance_id keyset the loan book uses can't paginate a severity sort). Live DPD is ADVISORY
// — the stamped delinquency_bucket (+ bucket_as_of) stays the classification-of-record.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// DelinquentRow is one queue row: the loan-book advance fields plus the drill key, the
// compliance signals, last-activity timestamps, the ADVISORY live DPD, and the governed
// bucket rank used for ordering.
type DelinquentRow struct {
	AdvanceRow          // base loan-book projection (money, bucket, bucket_as_of, …)
	SubscriberAccountID string
	SubStatus           string // ACTIVE / BARRED / SELF_EXCLUDED / CLOSED (self-exclusion gate)
	OpenComplaint       bool   // an OPEN/IN_REVIEW complaint exists (dispute gate)
	LastRecharge        *time.Time
	LastRecovery        *time.Time
	LiveDPD             int64 // advisory: GREATEST(0, days_since(activated_at) − grace); stamp is authoritative
	BucketRank          int64 // governed ladder rank (higher = worse)
}

// DelinquentFilter carries the governed ladder (ordered codes + parallel ranks), the
// delinquent code subset (the ANY filter — the ladder minus rung-0/CURRENT), the grace
// window, the composite cursor, and the page size. Telco is the optional admin narrowing.
type DelinquentFilter struct {
	Telco           string
	DelinquentCodes []string // classified-delinquent buckets (governed; excludes CURRENT)
	LadderCodes     []string // ordered ladder codes (parallel to LadderRanks)
	LadderRanks     []int32
	GraceDays       int
	Cursor          string // composite keyset (see collectionsCursor); "" = first page
	Limit           int
}

// collectionsCursor encodes the composite keyset for the severity sort.
type collectionsCursor struct {
	rank int64
	dpd  int64
	out  int64
	id   string
}

func encodeCollectionsCursor(r DelinquentRow) string {
	return fmt.Sprintf("%d|%d|%d|%s", r.BucketRank, r.LiveDPD, r.Outstanding.Amount(), r.AdvanceID)
}

func parseCollectionsCursor(s string) (collectionsCursor, bool) {
	if s == "" {
		return collectionsCursor{}, false
	}
	// SplitN on the LAST 3 delimiters would be safer, but advance_id is a ULID (no '|'),
	// so a 4-field split is unambiguous.
	parts := strings.SplitN(s, "|", 4)
	if len(parts) != 4 {
		return collectionsCursor{}, false
	}
	rank, e1 := strconv.ParseInt(parts[0], 10, 64)
	dpd, e2 := strconv.ParseInt(parts[1], 10, 64)
	out, e3 := strconv.ParseInt(parts[2], 10, 64)
	if e1 != nil || e2 != nil || e3 != nil || parts[3] == "" {
		return collectionsCursor{}, false
	}
	return collectionsCursor{rank: rank, dpd: dpd, out: out, id: parts[3]}, true
}

const delinquentListSQL = `
WITH ladder(code, rnk) AS (
  SELECT * FROM unnest($3::text[], $4::int[])
),
ranked AS (
  SELECT a.advance_id, a.telco_id, a.programme_id, a.state,
         COALESCE(a.delinquency_bucket, '') AS bucket, a.bucket_as_of,
         COALESCE(a.fee_recognition, 'UPFRONT') AS fee_recognition,
         sa.msisdn_token, a.subscriber_account_id, sa.status AS sub_status,
         a.face_value_minor, a.fee_minor, a.disbursed_minor, a.outstanding_minor,
         COALESCE(r.recovered_minor, 0) AS recovered_minor, a.currency,
         a.accepted_at, a.activated_at, a.closed_at,
         lr.last_recharge, lrec.last_recovery,
         EXISTS(SELECT 1 FROM complaints c
                WHERE c.subscriber_account_id = a.subscriber_account_id
                  AND c.state IN ('OPEN', 'IN_REVIEW')) AS open_complaint,
         GREATEST(0, (CURRENT_DATE - a.activated_at::date) - $5)::bigint AS live_dpd,
         COALESCE(l.rnk, 0)::bigint AS bucket_rank
  FROM advances a
  JOIN subscriber_accounts sa ON sa.subscriber_account_id = a.subscriber_account_id
  LEFT JOIN ladder l ON l.code = a.delinquency_bucket
  LEFT JOIN LATERAL (
    SELECT SUM(amount_minor) AS recovered_minor FROM recovery_allocations ra
    WHERE ra.advance_id = a.advance_id AND ra.component IN ('FEE', 'PRINCIPAL')
  ) r ON true
  LEFT JOIN LATERAL (
    SELECT MAX(re.occurred_at) AS last_recharge FROM recovery_events re
    WHERE re.subscriber_account_id = a.subscriber_account_id AND re.source_event_id LIKE 'wh:%'
  ) lr ON true
  LEFT JOIN LATERAL (
    SELECT MAX(ra2.created_at) AS last_recovery FROM recovery_allocations ra2
    WHERE ra2.advance_id = a.advance_id
  ) lrec ON true
  WHERE a.delinquency_bucket = ANY($2::text[])
    AND a.state IN ('ACTIVE', 'PARTIALLY_RECOVERED')
    AND ($1 = '' OR a.telco_id = $1)
)
SELECT advance_id, telco_id, programme_id, state, bucket, bucket_as_of, fee_recognition,
       msisdn_token, subscriber_account_id, sub_status,
       face_value_minor, fee_minor, disbursed_minor, outstanding_minor, recovered_minor, currency,
       accepted_at, activated_at, closed_at,
       last_recharge, last_recovery, open_complaint, live_dpd, bucket_rank
FROM ranked
WHERE ($6 = false OR (bucket_rank, live_dpd, outstanding_minor, advance_id) < ($7::bigint, $8::bigint, $9::bigint, $10::text))
ORDER BY bucket_rank DESC, live_dpd DESC, outstanding_minor DESC, advance_id DESC
LIMIT $11`

// ListDelinquent returns one severity-ranked page of the delinquency queue. Fail-closed on
// no-authority. The delinquent set + rank ladder + grace all come from governed config
// (passed in by the handler), never hardcoded.
func ListDelinquent(ctx context.Context, q Querier, scope OperatorScope, f DelinquentFilter) ([]DelinquentRow, error) {
	telco, ok := scope.TelcoLevelBound()
	if !ok {
		return nil, nil // fail-closed (also matches the loan-book/ListOpenBreaks discipline)
	}
	if len(f.DelinquentCodes) == 0 || len(f.LadderCodes) == 0 {
		return nil, nil // no governed ladder resolved => nothing to rank (handler degrades)
	}
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	cur, hasCur := parseCollectionsCursor(f.Cursor)
	rows, err := q.Query(ctx, delinquentListSQL,
		telco, f.DelinquentCodes, f.LadderCodes, f.LadderRanks, f.GraceDays,
		hasCur, cur.rank, cur.dpd, cur.out, cur.id, limit)
	if err != nil {
		return nil, fmt.Errorf("list delinquent: %w", err)
	}
	defer rows.Close()
	var out []DelinquentRow
	for rows.Next() {
		var d DelinquentRow
		var faceMinor, feeMinor, disbMinor, outMinor, recMinor int64
		var currency string
		if err := rows.Scan(
			&d.AdvanceID, &d.TelcoID, &d.ProgrammeID, &d.State,
			&d.DelinquencyBucket, &d.BucketAsOf, &d.FeeRecognition,
			&d.MSISDNToken, &d.SubscriberAccountID, &d.SubStatus,
			&faceMinor, &feeMinor, &disbMinor, &outMinor, &recMinor, &currency,
			&d.AcceptedAt, &d.ActivatedAt, &d.ClosedAt,
			&d.LastRecharge, &d.LastRecovery, &d.OpenComplaint, &d.LiveDPD, &d.BucketRank,
		); err != nil {
			return nil, fmt.Errorf("scan delinquent: %w", err)
		}
		var e error
		if d.FaceValue, e = scanMoney(faceMinor, currency); e != nil {
			return nil, e
		}
		if d.Fee, e = scanMoney(feeMinor, currency); e != nil {
			return nil, e
		}
		if d.Disbursed, e = scanMoney(disbMinor, currency); e != nil {
			return nil, e
		}
		if d.Outstanding, e = scanMoney(outMinor, currency); e != nil {
			return nil, e
		}
		if d.Recovered, e = scanMoney(recMinor, currency); e != nil {
			return nil, e
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// NextDelinquentCursor returns the composite cursor for the page's last row (for "load
// more"), or "" when the page was not full (no more rows).
func NextDelinquentCursor(rows []DelinquentRow, limit int) string {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if len(rows) < limit {
		return ""
	}
	return encodeCollectionsCursor(rows[len(rows)-1])
}

// WriteOffCandidateCount counts open advances whose STAMPED bucket is at/over the governed
// write-off floor (writeoff.policy.min_bucket, resolved by the handler to the set of
// candidate bucket codes). Fail-closed on no-authority. Uses the stamped bucket — the
// classification-of-record — not a live-DPD re-derivation.
func WriteOffCandidateCount(ctx context.Context, q Querier, scope OperatorScope, telcoFilter string, candidateCodes []string) (int64, error) {
	telco, ok := scope.TelcoLevelBound()
	if !ok {
		return 0, nil
	}
	if len(candidateCodes) == 0 {
		return 0, nil
	}
	var n int64
	if err := q.QueryRow(ctx, `
SELECT COUNT(*) FROM advances
WHERE state IN ('ACTIVE', 'PARTIALLY_RECOVERED')
  AND delinquency_bucket = ANY($1::text[])
  AND ($2 = '' OR telco_id = $2)`, candidateCodes, telco).Scan(&n); err != nil {
		return 0, fmt.Errorf("writeoff candidate count: %w", err)
	}
	return n, nil
}
