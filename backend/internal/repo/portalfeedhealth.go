package repo

// Feed-Health monitor read-model (Wave B.4). An ops view over the MNO feeds — the
// recharge webhook (drives scoring + recovery) and the EOD recovery/deduction feed —
// answering: is data arriving · is it clean · is anything stuck. Every function runs
// on the operatorRead RLS tx (tcp_operator), so rows AND aggregates are DB-scoped by
// telco; telco-grained resources additionally guard with scope.TelcoLevelBound() (a
// programme/global operator reads nothing, matching ListOpenBreaks). Money is
// server-computed; the handler formats via toMoneyView. The `wh:%` filter on
// recovery_events is the ONLY discriminator between the recharge feed and the EOD
// feed in that table — omit it and every recharge count double-counts both feeds.

import (
	"context"
	"fmt"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
)

// ── (a) Is data arriving? ────────────────────────────────────────────────────

// RechargeFreshnessRow is one telco's recharge-feed liveness: the last webhook
// recharge received and how many landed today. (A1)
type RechargeFreshnessRow struct {
	TelcoID      string
	LastReceived *time.Time // nil only if the telco has no recharge events at all
	EventsToday  int64
}

// RechargeFreshness reports last-received + events-today per telco, over the recharge
// feed only (source_event_id LIKE 'wh:%'). GROUP BY telco_id gives the '*' estate; a
// telco operator sees one row. (A1)
func RechargeFreshness(ctx context.Context, q Querier, scope OperatorScope) ([]RechargeFreshnessRow, error) {
	telco, ok := scope.TelcoLevelBound()
	if !ok {
		return nil, nil
	}
	rows, err := q.Query(ctx, `
SELECT telco_id, MAX(received_at),
       COUNT(*) FILTER (WHERE received_at >= date_trunc('day', now())) AS events_today
FROM recovery_events
WHERE source_event_id LIKE 'wh:%' AND ($1 = '' OR telco_id = $1)
GROUP BY telco_id
ORDER BY telco_id`, telco)
	if err != nil {
		return nil, fmt.Errorf("recharge freshness: %w", err)
	}
	defer rows.Close()
	var out []RechargeFreshnessRow
	for rows.Next() {
		var r RechargeFreshnessRow
		if err := rows.Scan(&r.TelcoID, &r.LastReceived, &r.EventsToday); err != nil {
			return nil, fmt.Errorf("scan recharge freshness: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// TelcosInScope lists the telcos the operator may see, for per-telco control lookups
// (A2 recovery-layer liveness runs on the tcp_app control pool, outside this tx). A
// telco-scoped operator returns exactly its own telco WITHOUT querying `telcos` (which
// has no RLS); only the '*' admin enumerates the estate. Fail-closed: a programme/global
// operator returns nothing.
func TelcosInScope(ctx context.Context, q Querier, scope OperatorScope) ([]string, error) {
	telco, ok := scope.TelcoLevelBound()
	if !ok {
		return nil, nil
	}
	if telco != "" {
		return []string{telco}, nil // telco-scoped: never touch the un-RLS'd telcos table
	}
	rows, err := q.Query(ctx, `SELECT telco_id FROM telcos ORDER BY telco_id`) // '*' admin only
	if err != nil {
		return nil, fmt.Errorf("telcos in scope: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan telco: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ── (b) Is it clean? ─────────────────────────────────────────────────────────

// RechargeStateMix counts recharge events by allocation state (PENDING/ALLOCATED/
// QUARANTINED/UNMATCHED), recharge feed only. QUARANTINED = over-recovery suspense;
// UNMATCHED = no advance to apply to. (B3)
func RechargeStateMix(ctx context.Context, q Querier, scope OperatorScope) (map[string]int64, error) {
	out := map[string]int64{}
	telco, ok := scope.TelcoLevelBound()
	if !ok {
		return out, nil
	}
	rows, err := q.Query(ctx, `
SELECT state, COUNT(*)
FROM recovery_events
WHERE source_event_id LIKE 'wh:%' AND ($1 = '' OR telco_id = $1)
GROUP BY state`, telco)
	if err != nil {
		return out, fmt.Errorf("recharge state mix: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var n int64
		if err := rows.Scan(&state, &n); err != nil {
			return out, fmt.Errorf("scan state mix: %w", err)
		}
		out[state] = n
	}
	return out, rows.Err()
}

// HeldCountRow is one (status, reason) bucket of the held/clamped recharge queue. (B2)
type HeldCountRow struct {
	Status string // HELD / RELEASED / REJECTED
	Reason string // PER_EVENT_CLAMP / DAILY_CEILING
	Count  int64
}

// HeldCountsByStatusReason groups the held-recharge queue by status+reason so the
// monitor sees the OPEN (HELD) backlog AND the RELEASED/REJECTED history and which
// clamp fired. ListOpen only shows status='HELD'; this is the fuller picture. (B1/B2)
func HeldCountsByStatusReason(ctx context.Context, q Querier, scope OperatorScope) ([]HeldCountRow, error) {
	telco, ok := scope.TelcoLevelBound()
	if !ok {
		return nil, nil
	}
	rows, err := q.Query(ctx, `
SELECT status, reason, COUNT(*)
FROM held_recharge_events
WHERE ($1 = '' OR telco_id = $1)
GROUP BY status, reason
ORDER BY status, reason`, telco)
	if err != nil {
		return nil, fmt.Errorf("held counts: %w", err)
	}
	defer rows.Close()
	var out []HeldCountRow
	for rows.Next() {
		var r HeldCountRow
		if err := rows.Scan(&r.Status, &r.Reason, &r.Count); err != nil {
			return nil, fmt.Errorf("scan held count: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DuplicateHoldRow is a (msisdn, amount, occurred_at) tuple that arrived under more
// than one distinct source_event_id — the fingerprint of an unstable telco txn id
// (MTN contract item #2), an early read on the stable-txn-id requirement. (C3)
type DuplicateHoldRow struct {
	TelcoID          string
	MSISDNToken      string // masked at egress
	AmountMinor      int64
	OccurredAt       time.Time
	DistinctEventIDs int64
}

// DuplicateHolds finds held recharges that share (msisdn, amount, occurred_at) but
// carry different source_event_ids — i.e. the same real recharge ingested twice under
// different ids. (C3)
func DuplicateHolds(ctx context.Context, q Querier, scope OperatorScope) ([]DuplicateHoldRow, error) {
	telco, ok := scope.TelcoLevelBound()
	if !ok {
		return nil, nil
	}
	rows, err := q.Query(ctx, `
SELECT telco_id, msisdn_token, amount_minor, occurred_at, COUNT(DISTINCT source_event_id) AS n
FROM held_recharge_events
WHERE ($1 = '' OR telco_id = $1)
GROUP BY telco_id, msisdn_token, amount_minor, occurred_at
HAVING COUNT(DISTINCT source_event_id) > 1
ORDER BY n DESC, occurred_at DESC
LIMIT 100`, telco)
	if err != nil {
		return nil, fmt.Errorf("duplicate holds: %w", err)
	}
	defer rows.Close()
	var out []DuplicateHoldRow
	for rows.Next() {
		var r DuplicateHoldRow
		if err := rows.Scan(&r.TelcoID, &r.MSISDNToken, &r.AmountMinor, &r.OccurredAt, &r.DistinctEventIDs); err != nil {
			return nil, fmt.Errorf("scan duplicate hold: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ── (c) Is anything stuck? ───────────────────────────────────────────────────

// RecoveryBreakBacklog is the open RECOVERY recon-break backlog and the age of the
// oldest one (for the governed break-aging alert). (C1)
type RecoveryBreakBacklog struct {
	OpenCount       int64
	OldestCreatedAt *time.Time // nil when the backlog is empty
}

// RecoveryBreakBacklogFor counts unresolved RECOVERY breaks (recon_items). ListOpenBreaks
// mixes FULFILMENT + RECOVERY; the item_type='RECOVERY' filter is what makes this the
// recharge/recovery feed's health rather than the wrong feed's. (C1)
func RecoveryBreakBacklogFor(ctx context.Context, q Querier, scope OperatorScope) (RecoveryBreakBacklog, error) {
	var out RecoveryBreakBacklog
	telco, ok := scope.TelcoLevelBound()
	if !ok {
		return out, nil
	}
	if err := q.QueryRow(ctx, `
SELECT COUNT(*), MIN(created_at)
FROM recon_items
WHERE status LIKE 'BREAK_%' AND resolved_at IS NULL AND item_type = 'RECOVERY'
  AND ($1 = '' OR telco_id = $1)`, telco).Scan(&out.OpenCount, &out.OldestCreatedAt); err != nil {
		return out, fmt.Errorf("recovery break backlog: %w", err)
	}
	return out, nil
}

// ── grant-backed tiles (migration 0068) ──────────────────────────────────────

// ReconRunHealthRow is the latest ACTIVE RECOVERY recon run for one telco. created_at
// is the last recon ATTEMPT (vs A2's IsLayerLive = last CONFIRMED), which distinguishes
// "silent feed" from "arrived but unconfirmed". (B4)
type ReconRunHealthRow struct {
	TelcoID      string
	SourceCount  int64
	MatchedCount int64
	BreakCount   int64
	CreatedAt    time.Time
}

// ReconRunHealth is the per-telco latest-ACTIVE runs plus the count of REJECTED runs (a
// REJECTED run = the feed arrived but failed the completeness floor — the "held-back
// feed" signal, which never supersedes the good ACTIVE run). (B4)
type ReconRunHealth struct {
	LatestActive  []ReconRunHealthRow
	RejectedCount int64
}

// RecoveryReconRunHealth reads recon_runs (granted 0068) for the RECOVERY layer. Requires
// TelcoLevelBound; RLS scopes rows to the telco. (B4)
func RecoveryReconRunHealth(ctx context.Context, q Querier, scope OperatorScope) (ReconRunHealth, error) {
	var out ReconRunHealth
	telco, ok := scope.TelcoLevelBound()
	if !ok {
		return out, nil
	}
	// Latest ACTIVE run per telco (RECOVERY). DISTINCT ON collapses to one row/telco.
	rows, err := q.Query(ctx, `
SELECT DISTINCT ON (telco_id) telco_id, source_record_count, matched_count, break_count, created_at
FROM recon_runs
WHERE layer = 'RECOVERY' AND state = 'ACTIVE' AND ($1 = '' OR telco_id = $1)
ORDER BY telco_id, created_at DESC`, telco)
	if err != nil {
		return out, fmt.Errorf("recon run health: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var r ReconRunHealthRow
		if err := rows.Scan(&r.TelcoID, &r.SourceCount, &r.MatchedCount, &r.BreakCount, &r.CreatedAt); err != nil {
			return out, fmt.Errorf("scan recon run: %w", err)
		}
		out.LatestActive = append(out.LatestActive, r)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	// REJECTED RECOVERY runs (feed arrived, failed the completeness floor).
	if err := q.QueryRow(ctx, `
SELECT COUNT(*) FROM recon_runs
WHERE layer = 'RECOVERY' AND state = 'REJECTED' AND ($1 = '' OR telco_id = $1)`, telco).Scan(&out.RejectedCount); err != nil {
		return out, fmt.Errorf("rejected run count: %w", err)
	}
	return out, nil
}

// SuspenseSummary is the open over-recovery suspense position — money quarantined
// because it could not be applied. (B5)
type SuspenseSummary struct {
	OpenCount int64
	OpenTotal entity.Money
}

// OpenSuspense sums suspense_items in state OPEN (granted 0068). (B5)
func OpenSuspense(ctx context.Context, q Querier, scope OperatorScope) (SuspenseSummary, error) {
	out := SuspenseSummary{OpenTotal: entity.MustMoney(0, entity.NGN)}
	telco, ok := scope.TelcoLevelBound()
	if !ok {
		return out, nil
	}
	var total int64
	if err := q.QueryRow(ctx, `
SELECT COUNT(*), COALESCE(SUM(amount_minor), 0)
FROM suspense_items
WHERE state = 'OPEN' AND ($1 = '' OR telco_id = $1)`, telco).Scan(&out.OpenCount, &total); err != nil {
		return out, fmt.Errorf("open suspense: %w", err)
	}
	m, err := scanMoney(total, "NGN")
	if err != nil {
		return out, err
	}
	out.OpenTotal = m
	return out, nil
}

// FeedDenialRow is a count of a rejection action recorded on the ingest path. (B6)
type FeedDenialRow struct {
	Action string // RECHARGE_FEED_DENIED / RECHARGE_EVENT_HELD / TENANT_CONTEXT_MISMATCH
	Count  int64
}

// FeedDenials counts today's feed-denial audit events by action (audit_events, granted
// 0068). These are written with telco_id NULL (platform scope), so a telco operator's
// tenant RLS matches none — this is a '*'-ADMIN ESTATE view only, and the handler labels
// it as such. The "recovery recon layer not live" denial is the key "arriving but
// structurally rejected" signal. (B6)
func FeedDenials(ctx context.Context, q Querier, scope OperatorScope) ([]FeedDenialRow, error) {
	if !scope.authority {
		return nil, nil
	}
	rows, err := q.Query(ctx, `
SELECT action, COUNT(*)
FROM audit_events
WHERE action IN ('RECHARGE_FEED_DENIED', 'RECHARGE_EVENT_HELD', 'TENANT_CONTEXT_MISMATCH')
  AND occurred_at >= date_trunc('day', now())
GROUP BY action
ORDER BY action`)
	if err != nil {
		return nil, fmt.Errorf("feed denials: %w", err)
	}
	defer rows.Close()
	var out []FeedDenialRow
	for rows.Next() {
		var r FeedDenialRow
		if err := rows.Scan(&r.Action, &r.Count); err != nil {
			return nil, fmt.Errorf("scan denial: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UnconfirmedCloses is the count and oldest-age of recovery closes still awaiting recon
// confirmation — a phantom close whose token didn't match the day's run leaves the
// borrower blocked from re-borrowing until it clears. (C2)
type UnconfirmedCloses struct {
	OpenCount       int64
	OldestCreatedAt *time.Time // nil when none are stuck
}

// StuckUnconfirmedCloses counts recovery_unconfirmed_closes with cleared_at IS NULL
// (granted 0068); aging reuses the governed break-aging threshold. (C2)
func StuckUnconfirmedCloses(ctx context.Context, q Querier, scope OperatorScope) (UnconfirmedCloses, error) {
	var out UnconfirmedCloses
	telco, ok := scope.TelcoLevelBound()
	if !ok {
		return out, nil
	}
	if err := q.QueryRow(ctx, `
SELECT COUNT(*), MIN(created_at)
FROM recovery_unconfirmed_closes
WHERE cleared_at IS NULL AND ($1 = '' OR telco_id = $1)`, telco).Scan(&out.OpenCount, &out.OldestCreatedAt); err != nil {
		return out, fmt.Errorf("stuck unconfirmed closes: %w", err)
	}
	return out, nil
}
