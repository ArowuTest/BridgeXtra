package handler_test

import "github.com/ArowuTest/telco-credit-platform/backend/internal/platform/ratelimit"

// testLimiter is a deliberately-generous limiter so functional tests never hit
// 429; the dedicated rate-limit tests build tight limiters of their own.
func testLimiter() *ratelimit.Limiter {
	return ratelimit.New(map[string]ratelimit.Limit{
		"login":   {RatePerMinute: 1e9, Burst: 1e9},
		"channel": {RatePerMinute: 1e9, Burst: 1e9},
	})
}
