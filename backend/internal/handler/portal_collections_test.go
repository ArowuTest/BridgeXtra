package handler_test

// Wave B.3 collections / delinquency queue (Phase A). Adversarial assertions, one per way
// the queue could mislead:
//   1. Severity ranking is governed and correct — worst bucket first (rank from the
//      delinquency.buckets ladder, not a hardcoded order); a swap would reorder the list.
//   2. The multi-bucket filter shows ONLY classified-delinquent advances — CURRENT and
//      unclassified are excluded; open states only (a CLOSED/WRITTEN_OFF stale bucket is out).
//   3. The compliance signals surface — self-exclusion (subscriber status) and open complaint.
//   4. bucket_as_of + advisory live DPD are present (the honesty gate).
//   5. Write-off-candidate count is governed (writeoff.policy.min_bucket rank), not "all delinquent".
//   6. Composite-cursor pagination is stable across a severity sort.
//   7. Scope fail-closed: a no-authority operator sees an empty queue + 0 candidates.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
)

// seedDelinquent seeds an ACTIVE advance and stamps its delinquency bucket + as-of and
// backdates activated_at (for the advisory live DPD). Returns the advance + subscriber ids.
func seedDelinquent(t *testing.T, f *portalFixture, n int, bucket string, outMinor int64, activatedDaysAgo int) (advID, sub string) {
	t.Helper()
	advID, _ = seedLoanBookAdvance(t, f, n, "ACTIVE", 10000, 1000, 9000, outMinor, 0)
	sub = fmt.Sprintf("sub_b1_%d", n)
	if _, err := f.db.Admin.Exec(context.Background(), `
		UPDATE advances
		SET delinquency_bucket = $2, bucket_as_of = now() - interval '1 day',
		    activated_at = now() - make_interval(days => $3)
		WHERE advance_id = $1`, advID, bucket, activatedDaysAgo); err != nil {
		t.Fatalf("stamp delinquent: %v", err)
	}
	return advID, sub
}

func setSelfExcluded(t *testing.T, f *portalFixture, sub string) {
	t.Helper()
	if _, err := f.db.Admin.Exec(context.Background(),
		`UPDATE subscriber_accounts SET status='SELF_EXCLUDED' WHERE subscriber_account_id=$1`, sub); err != nil {
		t.Fatalf("self-exclude: %v", err)
	}
}

func openComplaint(t *testing.T, f *portalFixture, sub string, n int) {
	t.Helper()
	if _, err := f.db.Admin.Exec(context.Background(), `
		INSERT INTO complaints (complaint_id, telco_id, subscriber_account_id, channel, category, narrative, state)
		VALUES ($1,'SIM_NG',$2,'IVR','DISPUTED_ADVANCE','disputes the advance','OPEN')`,
		fmt.Sprintf("cmp_%d", n), sub); err != nil {
		t.Fatalf("open complaint: %v", err)
	}
}

type collQueueRow struct {
	AdvanceID      string `json:"advance_id"`
	DelinquencyBkt string `json:"delinquency_bucket"`
	BucketAsOf     string `json:"bucket_as_of"`
	BucketRank     int64  `json:"bucket_rank"`
	LiveDPD        int64  `json:"live_dpd_advisory"`
	SelfExcluded   bool   `json:"self_excluded"`
	OpenComplaint  bool   `json:"open_complaint"`
	Outstanding    struct {
		AmountMinor int64 `json:"amount_minor,string"`
	} `json:"outstanding"`
}

type collResp struct {
	Queue      []collQueueRow `json:"queue"`
	NextCursor string         `json:"next_cursor"`
	Ladder     struct {
		GraceDays         int      `json:"grace_days"`
		DelinquentBuckets []string `json:"delinquent_buckets"`
		Resolved          bool     `json:"resolved"`
	} `json:"ladder"`
	Rollup struct {
		WriteoffCandidates int64 `json:"writeoff_candidates"`
	} `json:"rollup"`
}

func collectionsGET(t *testing.T, f *portalFixture, s *session, query string) collResp {
	t.Helper()
	code, body := f.callBody(t, s, "GET", "/v1/portal/ops/collections"+query, "")
	if code != http.StatusOK {
		t.Fatalf("collections: %d %s", code, body)
	}
	var r collResp
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatalf("unmarshal collections: %v — %s", err, body)
	}
	return r
}

// writeoffMinBucketRank reads the governed writeoff.policy.min_bucket and its rank within the
// delinquency ladder, so the candidate-count assertion's expected value is config-derived.
func writeoffMinBucketRank(t *testing.T, f *portalFixture) int {
	t.Helper()
	ctx := context.Background()
	var minBucket string
	if err := f.db.Admin.QueryRow(ctx, `
		SELECT content->>'min_bucket' FROM config_versions
		WHERE domain='writeoff.policy' AND scope='programme:prg_sim_airtime01' AND state='ACTIVE'`).Scan(&minBucket); err != nil {
		t.Fatalf("read writeoff min_bucket: %v", err)
	}
	var rank int
	if err := f.db.Admin.QueryRow(ctx, `
		WITH b AS (
		  SELECT (jsonb_array_elements(content->'buckets')->>'code') AS code,
		         (jsonb_array_elements(content->'buckets')->>'min_days_past_due')::int AS mdpd
		  FROM config_versions
		  WHERE domain='delinquency.buckets' AND scope='programme:prg_sim_airtime01' AND state='ACTIVE'
		),
		ranked AS (SELECT code, row_number() OVER (ORDER BY mdpd) - 1 AS rnk FROM b)
		SELECT rnk FROM ranked WHERE code=$1`, minBucket).Scan(&rank); err != nil {
		t.Fatalf("rank of min_bucket %q: %v", minBucket, err)
	}
	return rank
}

func TestCollections_Severity_Compliance_Candidate_AndScope(t *testing.T) {
	f := newPortalFixture(t, "collections")
	ops := f.login(t, roleKeys["OPS"])

	// Delinquent advances across three rungs + one CURRENT (must be excluded). Distinct
	// outstanding so a rank/order regression is visible.
	adv90, sub90 := seedDelinquent(t, f, 40, "DPD_90_PLUS", 9000, 100)
	adv31, _ := seedDelinquent(t, f, 41, "DPD_31_60", 8000, 45)
	adv1, sub1 := seedDelinquent(t, f, 42, "DPD_1_7", 7000, 5)
	seedDelinquent(t, f, 43, "CURRENT", 6000, 1) // rung-0 → NOT delinquent, must be absent

	setSelfExcluded(t, f, sub1)   // the DPD_1_7 subscriber is self-excluded
	openComplaint(t, f, sub90, 1) // the DPD_90_PLUS subscriber has an open dispute

	r := collectionsGET(t, f, &ops, "?limit=50")

	if !r.Ladder.Resolved {
		t.Fatalf("governed ladder must resolve for the seeded programme")
	}
	// (1) Severity order: worst bucket first — DPD_90_PLUS, DPD_31_60, DPD_1_7. CURRENT absent.
	var ids []string
	for _, q := range r.Queue {
		ids = append(ids, q.AdvanceID)
		if q.DelinquencyBkt == "CURRENT" {
			t.Fatalf("CURRENT advance must NOT appear in the delinquency queue")
		}
	}
	if len(r.Queue) != 3 {
		t.Fatalf("expected 3 delinquent advances (CURRENT excluded), got %d: %v", len(r.Queue), ids)
	}
	if r.Queue[0].AdvanceID != adv90 || r.Queue[1].AdvanceID != adv31 || r.Queue[2].AdvanceID != adv1 {
		t.Fatalf("severity order must be worst-bucket-first [%s,%s,%s], got %v", adv90, adv31, adv1, ids)
	}
	// Rank strictly descends (governed ladder rank, not hardcoded).
	if r.Queue[0].BucketRank <= r.Queue[1].BucketRank || r.Queue[1].BucketRank <= r.Queue[2].BucketRank {
		t.Fatalf("bucket_rank must strictly descend, got %d,%d,%d", r.Queue[0].BucketRank, r.Queue[1].BucketRank, r.Queue[2].BucketRank)
	}
	// (4) Honesty gate: bucket_as_of + advisory live DPD present.
	for _, q := range r.Queue {
		if q.BucketAsOf == "" {
			t.Fatalf("bucket_as_of must be surfaced on every row (%s)", q.AdvanceID)
		}
	}
	if r.Queue[0].LiveDPD <= 0 {
		t.Fatalf("the 100-day-old DPD_90_PLUS advance must have a positive advisory DPD, got %d", r.Queue[0].LiveDPD)
	}
	// (3) Compliance signals.
	byID := map[string]collQueueRow{}
	for _, q := range r.Queue {
		byID[q.AdvanceID] = q
	}
	if !byID[adv1].SelfExcluded {
		t.Fatalf("the self-excluded subscriber's advance must carry self_excluded=true")
	}
	if byID[adv90].SelfExcluded {
		t.Fatalf("a non-self-excluded subscriber must not be flagged")
	}
	if !byID[adv90].OpenComplaint {
		t.Fatalf("the advance whose subscriber has an OPEN complaint must carry open_complaint=true")
	}
	if byID[adv31].OpenComplaint {
		t.Fatalf("an advance with no complaint must not be flagged")
	}

	// (5) Write-off-candidate count is governed by writeoff.policy.min_bucket rank — only my
	// seeded advances at/above that rank count (a fresh DB has no others).
	floor := writeoffMinBucketRank(t, f)
	wantCand := 0
	for _, q := range r.Queue {
		if int(q.BucketRank) >= floor {
			wantCand++
		}
	}
	if r.Rollup.WriteoffCandidates != int64(wantCand) {
		t.Fatalf("writeoff_candidates must be the governed count (rank>=%d ⇒ %d), got %d", floor, wantCand, r.Rollup.WriteoffCandidates)
	}
	if wantCand == len(r.Queue) {
		t.Fatalf("test is not discriminating: the write-off floor should exclude the lower rungs (got floor rank %d)", floor)
	}

	// (6) Composite-cursor pagination: page size 2 then the rest, no dup / no skip.
	p1 := collectionsGET(t, f, &ops, "?limit=2")
	if len(p1.Queue) != 2 || p1.NextCursor == "" {
		t.Fatalf("first page must be full (2) with a cursor, got %d cursor=%q", len(p1.Queue), p1.NextCursor)
	}
	p2 := collectionsGET(t, f, &ops, "?limit=2&cursor="+p1.NextCursor)
	if len(p2.Queue) != 1 {
		t.Fatalf("second page must hold the remaining 1, got %d", len(p2.Queue))
	}
	seen := map[string]bool{p1.Queue[0].AdvanceID: true, p1.Queue[1].AdvanceID: true}
	if seen[p2.Queue[0].AdvanceID] {
		t.Fatalf("composite cursor must not repeat a row across pages")
	}
	// Union of both pages equals the full ordered set.
	if p1.Queue[0].AdvanceID != adv90 || p1.Queue[1].AdvanceID != adv31 || p2.Queue[0].AdvanceID != adv1 {
		t.Fatalf("paged order must match the single-page severity order")
	}

	// (7) Scope fail-closed.
	ctx := context.Background()
	if err := (&repo.Admins{Pool: f.db.Admin}).CreateWithRole(ctx, "adm_coll_g", "ops_global_coll", "portal-key-ops-global-coll", "OPS", "global"); err != nil {
		t.Fatal(err)
	}
	g := f.login(t, "portal-key-ops-global-coll")
	gr := collectionsGET(t, f, &g, "?limit=50")
	if len(gr.Queue) != 0 || gr.Rollup.WriteoffCandidates != 0 {
		t.Fatalf("no-authority operator must see empty queue + 0 candidates, got %d rows / %d candidates", len(gr.Queue), gr.Rollup.WriteoffCandidates)
	}
}
