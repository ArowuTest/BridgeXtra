// Package ratelimit is a small in-process token-bucket limiter for the
// inbound edges (R-P0-8): the portal /login (credential-stuffing / brute
// force) and the telco-facing channel API (a hammering or looping telco,
// with no backstop otherwise). Limits are GOVERNED config, not hardcoded, and
// an unknown scope is DENIED — a surface wired to a scope with no configured
// limit must never run unlimited (reachability invariant).
//
// Scope: per-process. It stops a single client saturating one instance, which
// is the pre-pilot concern; cross-instance coordination (a shared store or an
// edge gateway) is a scale hardening tracked for production, noted honestly.
package ratelimit

import (
	"math"
	"sync"
	"time"
)

// Limit is the per-key allowance for one scope.
type Limit struct {
	RatePerMinute float64 // sustained refill rate per key
	Burst         float64 // bucket capacity (max instantaneous)
}

type bucket struct {
	tokens float64
	last   time.Time
}

// Limiter is a keyed token-bucket limiter. Safe for concurrent use.
type Limiter struct {
	mu      sync.Mutex
	limits  map[string]Limit
	buckets map[string]*bucket
	now     func() time.Time
	maxKeys int
}

// New builds a limiter from a scope→limit map (loaded from governed config).
func New(limits map[string]Limit) *Limiter {
	cp := make(map[string]Limit, len(limits))
	for k, v := range limits {
		cp[k] = v
	}
	return &Limiter{limits: cp, buckets: map[string]*bucket{}, now: time.Now, maxKeys: 50_000}
}

// Allow reports whether one request under (scope, key) is permitted, consuming
// a token if so. Unknown or non-positive-limit scope → DENIED (fail-closed).
func (l *Limiter) Allow(scope, key string) bool {
	lim, ok := l.limits[scope]
	if !ok || lim.RatePerMinute <= 0 || lim.Burst <= 0 {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	k := scope + "|" + key
	b := l.buckets[k]
	if b == nil {
		if len(l.buckets) >= l.maxKeys {
			l.sweep(now)
		}
		b = &bucket{tokens: lim.Burst, last: now}
		l.buckets[k] = b
	}
	// Refill by elapsed time, capped at burst.
	elapsed := now.Sub(b.last).Minutes()
	if elapsed > 0 {
		b.tokens = math.Min(lim.Burst, b.tokens+elapsed*lim.RatePerMinute)
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// sweep drops buckets that have fully refilled and been idle a while — they
// carry no state a fresh bucket wouldn't reproduce. Called under lock when the
// key space grows large, bounding memory against an IP-churn flood.
func (l *Limiter) sweep(now time.Time) {
	for k, b := range l.buckets {
		if now.Sub(b.last) > 2*time.Minute {
			delete(l.buckets, k)
		}
	}
}
