package handler_test

import (
	"context"
	"sync"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform/ratelimit"
)

// testLimiter is a deliberately-generous limiter so functional tests never hit
// 429; the dedicated rate-limit tests build tight limiters of their own.
func testLimiter() *ratelimit.Limiter {
	return ratelimit.New(map[string]ratelimit.Limit{
		"login":      {RatePerMinute: 1e9, Burst: 1e9},
		"channel":    {RatePerMinute: 1e9, Burst: 1e9},
		"channel_ip": {RatePerMinute: 1e9, Burst: 1e9},
	})
}

// grantAllStore is a permissive aggregate store for functional tests.
//
// IT IS ALSO THE REASON Mount's panics ARE NOT THE PROOF OF WIRING. Once every handler test must
// supply a store to mount at all, "Store != nil" becomes a condition that a completely inert store
// satisfies just as well as the real one — so a production process wired with a broken aggregate
// would pass exactly the same check. The load-bearing evidence is behavioural and lives in
// aggregate_ratelimit_med006_test.go: a request through the MOUNTED route must decrement the actual
// shared row.
type grantAllStore struct{ calls atomic64 }

func (s *grantAllStore) Take(context.Context, string, string, int64, int64, time.Time) (bool, error) {
	s.calls.add(1)
	return true, nil
}

type atomic64 struct {
	mu sync.Mutex
	n  int64
}

func (a *atomic64) add(d int64) { a.mu.Lock(); a.n += d; a.mu.Unlock() }
func (a *atomic64) load() int64 { a.mu.Lock(); defer a.mu.Unlock(); return a.n }

// testAgg is the aggregate config functional tests mount with — generous, matching testLimiter.
func testAgg() map[string]ratelimit.AggLimit {
	return map[string]ratelimit.AggLimit{
		"channel": {BurstMilli: 1e12, RefillMilliPerSec: 1e9, QuotaFrom: time.Unix(1, 0).UTC()},
	}
}

// testGuard is the generous Guard functional tests mount with.
func testGuard() *ratelimit.Guard {
	return ratelimit.NewGuard(testLimiter(), &grantAllStore{}, testAgg(), 0, nil)
}
