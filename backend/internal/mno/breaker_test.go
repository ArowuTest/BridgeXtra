package mno

// R-P0-8b: the circuit breaker's own logic, driven by a deterministic clock.
// Closed → (threshold failures over min sample) → Open → (cooldown) →
// HalfOpen → success closes / failure re-opens. A responsive telco (success)
// never trips it.

import (
	"testing"
	"time"
)

func newTestBreaker() (*breaker, *time.Time) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	b := newBreaker(breakerCfg{thresholdPct: 50, minRequests: 4, cooldown: 30 * time.Second})
	b.now = func() time.Time { return now }
	return b, &now
}

func TestBreaker_TripsAtThresholdOverMinSample(t *testing.T) {
	b, _ := newTestBreaker()
	// 3 failures under the min sample of 4 must NOT trip.
	for i := 0; i < 3; i++ {
		if !b.allow() {
			t.Fatalf("call %d must be allowed while closed", i)
		}
		b.record(false)
	}
	if !b.allow() {
		t.Fatal("still closed under the min sample")
	}
	// The 4th failure reaches the min sample at 100% error > 50% → trips.
	b.record(false)
	if b.allow() {
		t.Fatal("circuit must be OPEN after crossing threshold over the min sample")
	}
}

func TestBreaker_ResponsiveTelcoNeverTrips(t *testing.T) {
	b, _ := newTestBreaker()
	// Alternate fail/success: failures never sustain 50% because success
	// resets the window.
	for i := 0; i < 50; i++ {
		b.allow()
		b.record(i%2 == 0) // half succeed
	}
	if !b.allow() {
		t.Fatal("a telco that keeps responding must not trip the breaker")
	}
}

func TestBreaker_CooldownThenHalfOpenRecovers(t *testing.T) {
	b, now := newTestBreaker()
	for i := 0; i < 4; i++ {
		b.allow()
		b.record(false)
	}
	if b.allow() {
		t.Fatal("must be open")
	}
	// Within cooldown: still open.
	*now = now.Add(20 * time.Second)
	if b.allow() {
		t.Fatal("still within cooldown → open")
	}
	// After cooldown: one half-open probe admitted, the rest held.
	*now = now.Add(20 * time.Second)
	if !b.allow() {
		t.Fatal("after cooldown a half-open probe must be admitted")
	}
	if b.allow() {
		t.Fatal("only ONE half-open probe; the rest stay closed until it resolves")
	}
	// The probe succeeds → circuit closes.
	b.record(true)
	if !b.allow() {
		t.Fatal("a successful probe must close the circuit")
	}
}

func TestBreaker_HalfOpenFailureReopens(t *testing.T) {
	b, now := newTestBreaker()
	for i := 0; i < 4; i++ {
		b.allow()
		b.record(false)
	}
	*now = now.Add(31 * time.Second)
	if !b.allow() {
		t.Fatal("half-open probe admitted after cooldown")
	}
	b.record(false) // probe fails
	if b.allow() {
		t.Fatal("a failed half-open probe must re-open the circuit")
	}
}
