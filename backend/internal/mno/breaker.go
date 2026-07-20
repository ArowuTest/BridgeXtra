package mno

// R-P0-8b: a per-telco circuit breaker for the fulfilment adapter (TEN-004).
// The telco.adapter config already carried circuit_error_threshold_pct and
// circuit_min_requests, but nothing read them — armed-but-dead. This activates
// them (plus a governed cooldown) so a DOWN telco is circuit-broken instead of
// hammered: once the error RATE crosses the threshold over a minimum sample,
// the circuit OPENS and calls short-circuit to Unknown (INV-009 — the resolver
// enquires; money is never guessed) until a cooldown lets one probe through.
//
// "Failure" here means the telco did not RESPOND (transport error / 5xx) — a
// telco that answers, even with a business FAILED, is healthy and must not
// trip the breaker.
//
// R-P0-8b-F1: the error rate is measured over a genuine ROLLING window — a ring
// buffer of the last circuit_min_requests outcomes. An earlier version zeroed
// the sample on every success, so between resets the window held only failures;
// at the trip check failures==total, and failures*100 >= pct*total collapsed to
// 100 >= pct — true for any pct ≤ 100. That made circuit_error_threshold_pct
// inert (the breaker was really a consecutive-failure count) and pct=10 behaved
// identically to pct=90. With a rolling window each success now DILUTES the rate
// instead of erasing history, so the configured percentage governs the trip.

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

	mu    sync.Mutex
	state breakerState
	// window is a ring buffer of the last cfg.minRequests outcomes (true = a
	// failure). failures is the running count of failures currently held in it,
	// and filled is how many slots carry a real sample (0..len(window)). Once
	// the window is full the error rate is failures/filled — a true rolling rate
	// that circuit_error_threshold_pct gates against.
	window    []bool
	pos       int
	filled    int
	failures  int
	openUntil time.Time
	now       func() time.Time
}

func newBreaker(cfg breakerCfg) *breaker {
	size := cfg.minRequests
	if size < 1 {
		size = 1 // defensive; the validator already floors min_requests at 1
	}
	return &breaker{cfg: cfg, state: breakerClosed, window: make([]bool, size), now: time.Now}
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

	// Slide the rolling window by one: evict the oldest sample (adjusting the
	// failure count) before overwriting its slot, so successes dilute the rate
	// rather than erasing the window.
	if b.filled == len(b.window) {
		if b.window[b.pos] {
			b.failures--
		}
	} else {
		b.filled++
	}
	b.window[b.pos] = !success
	if !success {
		b.failures++
	}
	b.pos = (b.pos + 1) % len(b.window)

	// Only evaluate on a full sample, and trip on the error RATE over it.
	if b.filled >= b.cfg.minRequests && b.failures*100 >= b.cfg.thresholdPct*b.filled {
		b.trip()
	}
}

func (b *breaker) trip() {
	b.state = breakerOpen
	b.openUntil = b.now().Add(b.cfg.cooldown)
	b.clearWindow()
}

func (b *breaker) reset() {
	b.state = breakerClosed
	b.clearWindow()
}

func (b *breaker) clearWindow() {
	for i := range b.window {
		b.window[i] = false
	}
	b.pos, b.filled, b.failures = 0, 0, 0
}
