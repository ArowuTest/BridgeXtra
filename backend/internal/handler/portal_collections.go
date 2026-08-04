package handler

// Wave B.3 — collections / delinquency queue (Phase A, read-only work-view). GET
// /ops/collections: a severity-ranked list of advances that are behind, the two compliance
// signals (self-exclusion, open complaint) that will gate the Phase-B write-off/bar actions,
// and a rollup strip. NOT a chase/dunning tool — recovery is opportunistic and no
// outbound-contact capability exists; the only doors are write-off (Phase B) and bar.
//
// The bucket ladder, grace window, and write-off floor are all GOVERNED config resolved
// here (delinquency.buckets, writeoff.policy) and passed to the repo — no hardcoded rung
// order or day count. Live DPD is ADVISORY; the stamped delinquency_bucket (+ bucket_as_of)
// is the classification-of-record, surfaced prominently.

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
)

// resolvedLadder is the governed delinquency ladder flattened for the query: all rung codes
// ordered by days-past-due with parallel ranks, the delinquent subset (rungs past rung-0),
// and the grace window.
type resolvedLadder struct {
	codes           []string
	ranks           []int32
	delinquentCodes []string
	rankOf          map[string]int
	grace           int
}

type collectionsData struct {
	rows      []repo.DelinquentRow
	summary   repo.LoanBookSummaryResult
	candidate int64
}

func (p *Portal) opsCollections(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	scope := sess.OperatorScope()
	telcoFilter := r.URL.Query().Get("telco")
	cursor := r.URL.Query().Get("cursor")
	limit := clampInt(r.URL.Query().Get("limit"), 50, 200)
	now := time.Now().UTC()

	// Pass 1: the programmes in scope, to resolve the governed ladder from a representative one.
	progs, err := operatorRead(r.Context(), p, scope, func(ctx context.Context, tx pgx.Tx) ([]repo.ProgrammeHealthRow, error) {
		return repo.ProgrammesInScope(ctx, tx, scope)
	})
	if err != nil {
		p.Log.Error("ops collections programmes", "err", err)
		writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
		return
	}
	if telcoFilter != "" {
		progs = filterProgsByTelco(progs, telcoFilter)
	}

	// Governed ladder + write-off floor (config service's own pool). v1 resolves a single
	// canonical ladder from a representative in-scope programme; per-programme ladders are a
	// multi-programme follow-up (same class as the feed-health F4/F5 deferrals).
	ladder, ladderOK := p.resolveLadder(r.Context(), progs, now)
	candidateCodes := p.resolveWriteoffCandidates(r.Context(), progs, ladder, now)

	// Pass 2: the queue + rollup, all on one scoped tx.
	data, err := operatorRead(r.Context(), p, scope, func(ctx context.Context, tx pgx.Tx) (collectionsData, error) {
		var d collectionsData
		var e error
		if ladderOK {
			if d.rows, e = repo.ListDelinquent(ctx, tx, scope, repo.DelinquentFilter{
				Telco: telcoFilter, DelinquentCodes: ladder.delinquentCodes,
				LadderCodes: ladder.codes, LadderRanks: ladder.ranks, GraceDays: ladder.grace,
				Cursor: cursor, Limit: limit,
			}); e != nil {
				return d, e
			}
		}
		if d.summary, e = repo.LoanBookSummary(ctx, tx, scope, repo.AdvanceFilter{Telco: telcoFilter}); e != nil {
			return d, e
		}
		d.candidate, e = repo.WriteOffCandidateCount(ctx, tx, scope, telcoFilter, candidateCodes)
		return d, e
	})
	if err != nil {
		p.Log.Error("ops collections", "err", err)
		writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
		return
	}

	queue := make([]map[string]any, 0, len(data.rows))
	for _, d := range data.rows {
		row := advanceRowJSON(d.AdvanceRow) // base loan-book projection (incl. bucket + bucket_as_of)
		row["subscriber_account_id"] = d.SubscriberAccountID
		row["subscriber_status"] = d.SubStatus
		row["self_excluded"] = d.SubStatus == "SELF_EXCLUDED"
		row["open_complaint"] = d.OpenComplaint
		row["live_dpd_advisory"] = d.LiveDPD // ADVISORY — the client labels it; stamp is authoritative
		row["bucket_rank"] = d.BucketRank
		if d.LastRecharge != nil {
			row["last_recharge_at"] = *d.LastRecharge
		}
		if d.LastRecovery != nil {
			row["last_recovery_at"] = *d.LastRecovery
		}
		queue = append(queue, row)
	}

	resp := map[string]any{
		"queue":       queue,
		"next_cursor": repo.NextDelinquentCursor(data.rows, limit),
		// Ladder transparency: the governed grace + delinquent rungs the queue is built from.
		"ladder": map[string]any{
			"grace_days":         ladder.grace,
			"delinquent_buckets": ladder.delinquentCodes, // worst-last (ascending rank)
			"resolved":           ladderOK,
		},
		"rollup": map[string]any{
			"by_status":           data.summary.ByStatus,
			"by_bucket":           data.summary.ByBucket,
			"by_bucket_value":     byBucketValueJSON(data.summary.ByBucketValue),
			"writeoff_candidates": data.candidate,
		},
	}
	writeJSON(w, http.StatusOK, resp)
}

// resolveLadder reads the governed delinquency.buckets ladder from the first in-scope
// programme that resolves it. ok=false when none resolve (the queue then degrades to empty
// rather than inventing a rung order).
func (p *Portal) resolveLadder(ctx context.Context, progs []repo.ProgrammeHealthRow, now time.Time) (resolvedLadder, bool) {
	for _, pr := range progs {
		cv, err := p.Config.ActiveAt(ctx, "delinquency.buckets", "programme:"+pr.ProgrammeID, now)
		if err != nil {
			continue
		}
		var c struct {
			Buckets []struct {
				Code      string `json:"code"`
				MinDaysPD int    `json:"min_days_past_due"`
			} `json:"buckets"`
			GraceDays int `json:"grace_days"`
		}
		if json.Unmarshal(cv.Content, &c) != nil || len(c.Buckets) == 0 {
			continue
		}
		// The validator enforces strictly-ascending min_days_past_due; sort defensively so the
		// rank is by days-past-due regardless of array order.
		sort.SliceStable(c.Buckets, func(i, j int) bool { return c.Buckets[i].MinDaysPD < c.Buckets[j].MinDaysPD })
		l := resolvedLadder{grace: c.GraceDays, rankOf: map[string]int{}}
		for i, b := range c.Buckets {
			l.codes = append(l.codes, b.Code)
			l.ranks = append(l.ranks, int32(i))
			l.rankOf[b.Code] = i
			if b.MinDaysPD > 0 { // delinquent = past rung-0 (CURRENT)
				l.delinquentCodes = append(l.delinquentCodes, b.Code)
			}
		}
		if len(l.delinquentCodes) == 0 {
			continue
		}
		return l, true
	}
	return resolvedLadder{}, false
}

// resolveWriteoffCandidates returns the bucket codes at/over the governed write-off floor
// (writeoff.policy.min_bucket), mapped through the ladder rank. Empty when unresolved.
func (p *Portal) resolveWriteoffCandidates(ctx context.Context, progs []repo.ProgrammeHealthRow, ladder resolvedLadder, now time.Time) []string {
	if ladder.rankOf == nil {
		return nil
	}
	for _, pr := range progs {
		cv, err := p.Config.ActiveAt(ctx, "writeoff.policy", "programme:"+pr.ProgrammeID, now)
		if err != nil {
			continue
		}
		var c struct {
			MinBucket string `json:"min_bucket"`
		}
		if json.Unmarshal(cv.Content, &c) != nil || c.MinBucket == "" {
			continue
		}
		floor, ok := ladder.rankOf[c.MinBucket]
		if !ok {
			continue
		}
		var out []string
		for code, rank := range ladder.rankOf {
			if rank >= floor {
				out = append(out, code)
			}
		}
		return out
	}
	return nil
}

func filterProgsByTelco(progs []repo.ProgrammeHealthRow, telco string) []repo.ProgrammeHealthRow {
	out := progs[:0]
	for _, pr := range progs {
		if pr.TelcoID == telco {
			out = append(out, pr)
		}
	}
	return out
}
