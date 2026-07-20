package mno

// R-P0-8b: a per-telco circuit breaker for the fulfilment adapter (TEN-004).
// The telco.adapter config already carried circuit_error_threshold_pct and
// circuit_min_requests, but nothing read them — armed-but-dead. This activates
// them (plus a governed cooldown) so a DOWN telco is circuit-broken instead of
// hammered: once failures cross the threshold over a minimum sample, the
// circuit OPENS and calls short-circuit to Unknown (INV-009 — the resolver
// enquires; money is never guessed) until a cooldown lets one probe through.
//
// "Failure" here means the telco did not RESPOND (transport error / 5xx) — a
// telco that answers, even with a business FAILED, is healthy and must not
// trip the breaker.

import (
	"sync"
	"time"
)

type breakerState int

const (
	breakerClosed breakerState = iota
	breakerOpen
	breakerHalfOpen
)

// breakerCfg is the governed policy for one telco's breaker.
type breakerCfg struct {
	thresholdPct int
	minRequests  int
	cooldown     time.Duration
}

type breaker struct {
	cfg breakerCfg

	mu        sync.Mutex
	state     breakerState
	failures  int
	total     int
	openUntil time.Time
	now       func() time.Time
}

func newBreaker(cfg breakerCfg) *breaker {
	return &breaker{cfg: cfg, state: breakerClosed, now: time.Now}
}

// allow reports whether a call may proceed. An OPEN circuit refuses until its
// cooldown elapses, then admits exactly one HALF-OPEN probe.
func (b *breaker) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case breakerOpen:
		if b.now().Before(b.openUntil) {
			return false
		}
		// Cooldown elapsed: admit one probe.
		b.state = breakerHalfOpen
		return true
	case breakerHalfOpen:
		// A probe is already in flight; hold the rest closed.
		return false
	default: // closed
		return true
	}
}

// record feeds one call outcome back. success=false means the telco did not
// respond (transport error / 5xx).
func (b *breaker) record(success bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.state == breakerHalfOpen {
		if success {
			b.reset() // recovered
		} else {
			b.trip() // still down — re-open
		}
		return
	}
	if success {
		// Healthy response resets the window (fresh evaluation each burst).
		b.failures, b.total = 0, 0
		return
	}
	b.failures++
	b.total++
	if b.total >= b.cfg.minRequests && b.failures*100 >= b.cfg.thresholdPct*b.total {
		b.trip()
	}
}

func (b *breaker) trip() {
	b.state = breakerOpen
	b.openUntil = b.now().Add(b.cfg.cooldown)
	b.failures, b.total = 0, 0
}

func (b *breaker) reset() {
	b.state = breakerClosed
	b.failures, b.total = 0, 0
}
