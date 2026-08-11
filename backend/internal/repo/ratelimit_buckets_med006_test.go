package repo_test

// BX-MED-006: the cross-replica aggregate rate limit.
//
// The audit bar is: two replicas sharing one backing store must TOGETHER grant at most ONE
// configured quota, with the mutation being per-process buckets under which each replica grants the
// full quota. Everything here asserts EXACT counts rather than upper bounds — `granted <= N` is
// satisfied by granted == 0, so it stays green for every broken fixture, which is the single easiest
// way to certify a control that is not running.

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform/ratelimit"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
)

const med006Scope = "channel"

// qf is a quota effective-from stamp. Fixed and old, so a test that does not care about quota
// adoption never accidentally triggers it.
var qf = time.Unix(1_700_000_000, 0).UTC()

func bucketState(t *testing.T, db *testutil.DB, telco string) (tokens, burst int64, lastRefill time.Time) {
	t.Helper()
	err := db.Admin.QueryRow(context.Background(),
		`SELECT tokens_milli, burst_milli, last_refill_at FROM rate_limit_buckets WHERE scope=$1 AND telco_id=$2`,
		med006Scope, telco).Scan(&tokens, &burst, &lastRefill)
	if err != nil {
		t.Fatalf("read bucket: %v", err)
	}
	return
}

// ageBucket drives the bucket's clock BACKWARD by d. This is how every refill test here advances
// time: sleeping would make the assertions a race against wall-clock and against Postgres's own
// now(), and the tolerance needed to stop it flaking is exactly the tolerance that hides the
// defects being tested.
func ageBucket(t *testing.T, db *testutil.DB, telco string, d time.Duration) {
	t.Helper()
	if _, err := db.Admin.Exec(context.Background(),
		`UPDATE rate_limit_buckets SET last_refill_at = last_refill_at - $3::interval
		  WHERE scope=$1 AND telco_id=$2`,
		med006Scope, telco, d.String()); err != nil {
		t.Fatalf("age bucket: %v", err)
	}
}

// TestBXMED006_TwoReplicasShareOneQuota is the audit's acceptance test (clause d).
//
// Two Guards, each with its OWN in-process limiter — which is what a second replica actually is,
// since the per-process limiter is the only per-process state — sharing ONE Postgres bucket. Their
// local limits are generous, so the ONLY thing that can refuse a request is the shared quota.
//
// The assertion is EXACT: together they must grant precisely the burst, not "at most". Under the
// pre-fix behaviour each replica keeps its own bucket and grants the full quota, so the total is 2x
// and this fails; if the fixture is broken and nothing is granted at all, it also fails.
func TestBXMED006_TwoReplicasShareOneQuota(t *testing.T) {
	db := testutil.MustSetup(t, "med006_two_replicas")
	db.SeedTelco(t, "TELCO_A", "")
	ctx := context.Background()

	const burstTokens = 5
	store := repo.RateLimitBuckets{Pool: db.App}
	// Refill 1 milli/s: over the sub-second life of this test that is at most a couple of
	// milli-tokens, far below the 1000 needed for one request, so the count cannot drift.
	agg := map[string]ratelimit.AggLimit{
		med006Scope: {BurstMilli: burstTokens * 1000, RefillMilliPerSec: 1, QuotaFrom: qf},
	}
	generous := func() *ratelimit.Limiter {
		return ratelimit.New(map[string]ratelimit.Limit{med006Scope: {RatePerMinute: 1e9, Burst: 1e9}})
	}
	replicaA := ratelimit.NewGuard(generous(), store, agg, 0, slog.Default())
	replicaB := ratelimit.NewGuard(generous(), store, agg, 0, slog.Default())

	granted := 0
	for i := 0; i < burstTokens*2; i++ {
		g := replicaA
		if i%2 == 1 {
			g = replicaB
		}
		if ok, _ := g.AllowTelco(ctx, med006Scope, "TELCO_A"); ok {
			granted++
		}
	}
	if granted != burstTokens {
		t.Fatalf("two replicas together granted %d, want exactly %d (the ONE configured quota); "+
			"more means per-process buckets, fewer means the fixture never reached the limiter", granted, burstTokens)
	}
}

// TestBXMED006_ConcurrentNeverOverGrants proves the spend is atomic. Sequential alternation cannot:
// a read-then-write implementation produces the same count sequentially as an atomic one, so only
// genuine concurrency can distinguish them.
func TestBXMED006_ConcurrentNeverOverGrants(t *testing.T) {
	db := testutil.MustSetup(t, "med006_concurrent")
	db.SeedTelco(t, "TELCO_A", "")
	ctx := context.Background()

	const burstTokens = 8
	const attempts = 40
	store := repo.RateLimitBuckets{Pool: db.App}

	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make([]bool, attempts)
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // release everyone at once, so the spends genuinely overlap
			ok, err := store.Take(ctx, med006Scope, "TELCO_A", burstTokens*1000, 1, qf)
			if err != nil {
				t.Errorf("take: %v", err)
				return
			}
			results[i] = ok
		}(i)
	}
	close(start)
	wg.Wait()

	granted := 0
	for _, ok := range results {
		if ok {
			granted++
		}
	}
	if granted != burstTokens {
		t.Fatalf("concurrent spend granted %d, want exactly %d — a non-atomic read-then-write "+
			"over-grants by the concurrency factor", granted, burstTokens)
	}
	tokens, _, _ := bucketState(t, db, "TELCO_A")
	if tokens != 0 {
		t.Fatalf("bucket left at %d milli, want 0 — grants and the stored balance disagree", tokens)
	}
}

// TestBXMED006_SubResolutionRefillCarriesRemainder is the starvation proof.
//
// When the elapsed time floors to LESS THAN one milli-token, the refill is zero — and the timestamp
// must be left ALONE so the remainder accumulates. Advancing it unconditionally discards the
// fraction, and at any governed rate where each request's elapsed floors to zero under sustained
// load the bucket then never refills again and the partner is locked out permanently.
func TestBXMED006_SubResolutionRefillCarriesRemainder(t *testing.T) {
	db := testutil.MustSetup(t, "med006_remainder")
	db.SeedTelco(t, "TELCO_A", "")
	ctx := context.Background()
	store := repo.RateLimitBuckets{Pool: db.App}

	// 1 milli-token per second. Drain the single-token bucket.
	if ok, err := store.Take(ctx, med006Scope, "TELCO_A", 1000, 1, qf); err != nil || !ok {
		t.Fatalf("first take: ok=%v err=%v", ok, err)
	}
	// Park the clock 500ms in the past: FLOOR(0.5s * 1 milli/s) == 0.
	if _, err := db.Admin.Exec(ctx,
		`UPDATE rate_limit_buckets SET last_refill_at = clock_timestamp() - interval '500 milliseconds'
		  WHERE scope=$1 AND telco_id=$2`, med006Scope, "TELCO_A"); err != nil {
		t.Fatal(err)
	}
	_, _, before := bucketState(t, db, "TELCO_A")

	if ok, err := store.Take(ctx, med006Scope, "TELCO_A", 1000, 1, qf); err != nil || ok {
		t.Fatalf("sub-resolution take: ok=%v err=%v, want refused (nothing has refilled yet)", ok, err)
	}
	_, _, after := bucketState(t, db, "TELCO_A")
	if !after.Equal(before) {
		t.Fatalf("last_refill_at moved from %s to %s on a zero refill — the fractional remainder was "+
			"discarded, which starves the bucket forever under sustained load", before, after)
	}

	// Now let a whole milli-token accumulate: the bucket must recover.
	ageBucket(t, db, "TELCO_A", 1000*time.Second)
	if ok, err := store.Take(ctx, med006Scope, "TELCO_A", 1000, 1, qf); err != nil || !ok {
		t.Fatalf("after 1000s of refill: ok=%v err=%v, want granted", ok, err)
	}
}

// TestBXMED006_IdleTimeIsNotBanked is the UPPER bound — the opposite failure to starvation, and the
// dangerous one.
//
// If the timestamp advanced only by the refill actually ADDED (i.e. after the burst clamp), a full
// bucket would add nothing, leave the clock untouched, and then refund a token per request for as
// long as the banked idle time lasted. The effective burst becomes burst + idle_seconds x rate with
// no ceiling at all — an hour idle turns a 5-token bucket into 3605 tokens deliverable instantly.
func TestBXMED006_IdleTimeIsNotBanked(t *testing.T) {
	db := testutil.MustSetup(t, "med006_no_banking")
	db.SeedTelco(t, "TELCO_A", "")
	ctx := context.Background()
	store := repo.RateLimitBuckets{Pool: db.App}

	const burstTokens = 5
	const refillPerSec = 1000 // 1 token/s
	if ok, err := store.Take(ctx, med006Scope, "TELCO_A", burstTokens*1000, refillPerSec, qf); err != nil || !ok {
		t.Fatalf("seed take: ok=%v err=%v", ok, err)
	}
	// Refill it to full and then some: one hour of idle at 1 token/s is 3600 tokens' worth of
	// elapsed time against a 5-token ceiling.
	ageBucket(t, db, "TELCO_A", time.Hour)

	granted := 0
	for i := 0; i < 50; i++ {
		ok, err := store.Take(ctx, med006Scope, "TELCO_A", burstTokens*1000, refillPerSec, qf)
		if err != nil {
			t.Fatalf("take %d: %v", i, err)
		}
		if ok {
			granted++
		}
	}
	// The hour buys exactly one full bucket, never more. A couple of tokens of genuine wall-clock
	// refill during the loop is possible at 1 token/s, so allow the burst plus a small margin — but
	// nothing remotely like the 3600 the banking defect would deliver.
	if granted < burstTokens || granted > burstTokens+2 {
		t.Fatalf("after 1h idle, granted %d of 50, want ~%d (the burst) — a much larger number means "+
			"idle time was banked and the burst ceiling is not binding", granted, burstTokens)
	}
}

// TestBXMED006_QuotaTighteningBindsImmediately covers the adopt path and its token clamp.
//
// A newer governed quota must take effect at once. Without LEAST(tokens, new_burst) the tightening
// would not bind until the pre-existing surplus drained, so a bucket holding 20 tokens keeps serving
// 20 instant requests under a new burst of 2.
func TestBXMED006_QuotaTighteningBindsImmediately(t *testing.T) {
	db := testutil.MustSetup(t, "med006_tighten")
	db.SeedTelco(t, "TELCO_A", "")
	ctx := context.Background()
	store := repo.RateLimitBuckets{Pool: db.App}

	// Establish a wide bucket: burst 20 tokens.
	if ok, err := store.Take(ctx, med006Scope, "TELCO_A", 20_000, 1, qf); err != nil || !ok {
		t.Fatalf("seed take: ok=%v err=%v", ok, err)
	}
	if tokens, burst, _ := bucketState(t, db, "TELCO_A"); burst != 20_000 || tokens != 19_000 {
		t.Fatalf("seed state tokens=%d burst=%d, want 19000/20000", tokens, burst)
	}

	// A STRICTLY newer config tightens the burst to 2 tokens.
	newer := qf.Add(time.Hour)
	if ok, err := store.Take(ctx, med006Scope, "TELCO_A", 2_000, 1, newer); err != nil || !ok {
		t.Fatalf("tightening take: ok=%v err=%v", ok, err)
	}
	tokens, burst, _ := bucketState(t, db, "TELCO_A")
	if burst != 2_000 {
		t.Fatalf("burst = %d, want 2000 — the newer quota was not adopted", burst)
	}
	if tokens != 1_000 {
		t.Fatalf("tokens = %d, want 1000 (clamped to the new burst of 2000, then one spent) — an "+
			"unclamped balance lets the old, wider allowance keep being served", tokens)
	}
}

// TestBXMED006_StaleReplicaCannotReRaiseTheCeiling is the rolling-deploy case.
//
// During a deploy, replicas run different config versions against the SAME row. A replica still
// carrying the older, wider quota must not be able to restore it — otherwise the shared ceiling is
// whatever the slowest-restarting process believes, and a tightening never actually lands.
func TestBXMED006_StaleReplicaCannotReRaiseTheCeiling(t *testing.T) {
	db := testutil.MustSetup(t, "med006_stale_quota")
	db.SeedTelco(t, "TELCO_A", "")
	ctx := context.Background()
	store := repo.RateLimitBuckets{Pool: db.App}

	newer := qf.Add(time.Hour)
	// The upgraded replica lands the tight quota first.
	if ok, err := store.Take(ctx, med006Scope, "TELCO_A", 2_000, 1, newer); err != nil || !ok {
		t.Fatalf("new-config take: ok=%v err=%v", ok, err)
	}
	// The not-yet-restarted replica now spends with its OLD, WIDER quota.
	if _, err := store.Take(ctx, med006Scope, "TELCO_A", 20_000, 1, qf); err != nil {
		t.Fatalf("stale-config take: %v", err)
	}
	_, burst, _ := bucketState(t, db, "TELCO_A")
	if burst != 2_000 {
		t.Fatalf("burst = %d after a stale replica spent, want 2000 — the older config re-raised the "+
			"shared ceiling, so a governed tightening does not survive a rolling deploy", burst)
	}
}

// TestBXMED006_ExhaustionIsNotAnError is small and load-bearing.
//
// Writing the deny as a conditional `DO UPDATE ... WHERE tokens >= 1000` makes an over-limit request
// return zero rows, which pgx reports as an error indistinguishable from the store being broken. The
// Guard treats store errors as unavailability and falls back to local-only limiting — so ordinary,
// expected 429 traffic would DISABLE the control that is refusing it. Exhaustion must be a plain
// (false, nil).
func TestBXMED006_ExhaustionIsNotAnError(t *testing.T) {
	db := testutil.MustSetup(t, "med006_exhaustion")
	db.SeedTelco(t, "TELCO_A", "")
	ctx := context.Background()
	store := repo.RateLimitBuckets{Pool: db.App}

	if ok, err := store.Take(ctx, med006Scope, "TELCO_A", 1000, 1, qf); err != nil || !ok {
		t.Fatalf("first take: ok=%v err=%v", ok, err)
	}
	ok, err := store.Take(ctx, med006Scope, "TELCO_A", 1000, 1, qf)
	if err != nil {
		t.Fatalf("exhausted take returned an ERROR (%v); it must return (false, nil), or normal "+
			"over-limit traffic is misread as store failure and trips the fallback", err)
	}
	if ok {
		t.Fatal("exhausted bucket granted a request")
	}
}

// TestBXMED006_GuardDeniesUnconfiguredScope holds this package's documented floor: an unknown scope
// is DENIED. The aggregate path must not introduce a permissive default that the local limiter
// deliberately does not have.
func TestBXMED006_GuardDeniesUnconfiguredScope(t *testing.T) {
	db := testutil.MustSetup(t, "med006_unconfigured")
	db.SeedTelco(t, "TELCO_A", "")
	g := ratelimit.NewGuard(
		ratelimit.New(map[string]ratelimit.Limit{med006Scope: {RatePerMinute: 1e9, Burst: 1e9}}),
		repo.RateLimitBuckets{Pool: db.App},
		map[string]ratelimit.AggLimit{}, // no aggregate configured for any scope
		0, slog.Default())
	if ok, reason := g.AllowTelco(context.Background(), med006Scope, "TELCO_A"); ok {
		t.Fatalf("unconfigured aggregate scope was ALLOWED (reason %q); empty config must never mean "+
			"allow on a money door", reason)
	}
}
