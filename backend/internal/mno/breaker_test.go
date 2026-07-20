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
	b, _ := newTestBreaker() // threshold 50%, minRequests 4
	// A telco erroring 1-in-4 (25%) stays under the 50% threshold: even a full
	// rolling window holds exactly one failure in every four, so the rate never
	// crosses the line. Successes now DILUTE the window instead of erasing it —
	// which is precisely what the old inert-threshold logic failed to do.
	for i := 0; i < 50; i++ {
		b.allow()
		b.record(i%4 != 0) // fails on i=0,4,8,… → 25% error
	}
	if !b.allow() {
		t.Fatal("a telco erroring below the threshold must not trip the breaker")
	}
}

// R-P0-8b-F1: the distinguishing test. The SAME window of outcomes (6 failures
// / 4 successes = 60% error over a 10-request sample) must OPEN the breaker at
// threshold 50% but leave it CLOSED at 90%. This is the property the earlier
// implementation lacked: because every success zeroed the window, the error
// rate at the trip check was always 100%, so pct=50 and pct=90 behaved
// identically and no test that varied only the percentage could tell them
// apart. If circuit_error_threshold_pct ever goes inert again, exactly one of
// these two assertions fails.
func TestBreaker_ThresholdGovernsTripPoint(t *testing.T) {
	// record(success): false = the telco did not respond. 6 failures / 4 ok.
	seq := []bool{false, true, false, true, false, true, false, true, false, false}

	mk := func(pct int) *breaker {
		now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		b := newBreaker(breakerCfg{thresholdPct: pct, minRequests: 10, cooldown: 30 * time.Second})
		b.now = func() time.Time { return now }
		for _, ok := range seq {
			b.allow()
			b.record(ok)
		}
		return b
	}

	if mk(50).allow() {
		t.Fatal("60% error over the sample must OPEN the breaker at threshold 50%")
	}
	if !mk(90).allow() {
		t.Fatal("60% error must stay CLOSED at threshold 90% — the percentage must govern the trip")
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
