package ratelimit

import (
	"sync"
	"testing"
	"time"
)

// A bucket allows up to burst, then refuses until it refills.
func TestBurstThenRefuse(t *testing.T) {
	l := New(map[string]Limit{"login": {RatePerMinute: 60, Burst: 5}})
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return base }

	for i := 0; i < 5; i++ {
		if !l.Allow("login", "1.2.3.4") {
			t.Fatalf("request %d within burst must be allowed", i)
		}
	}
	if l.Allow("login", "1.2.3.4") {
		t.Fatal("the 6th request past a burst of 5 must be refused")
	}
}

// Refill returns tokens over time (60/min = 1/sec).
func TestRefillOverTime(t *testing.T) {
	l := New(map[string]Limit{"login": {RatePerMinute: 60, Burst: 2}})
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := base
	l.now = func() time.Time { return now }

	a1, a2 := l.Allow("login", "k"), l.Allow("login", "k")
	if !a1 || !a2 {
		t.Fatal("burst of 2 must allow two")
	}
	if l.Allow("login", "k") {
		t.Fatal("third must be refused")
	}
	now = base.Add(2 * time.Second) // +2 tokens at 60/min
	b1, b2 := l.Allow("login", "k"), l.Allow("login", "k")
	if !b1 || !b2 {
		t.Fatal("after 2s the bucket must have refilled two tokens")
	}
	if l.Allow("login", "k") {
		t.Fatal("only two tokens refilled")
	}
}

// Keys are isolated: one client's exhaustion does not affect another.
func TestPerKeyIsolation(t *testing.T) {
	l := New(map[string]Limit{"channel": {RatePerMinute: 60, Burst: 1}})
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return base }
	if !l.Allow("channel", "telcoA") {
		t.Fatal("A first must pass")
	}
	if l.Allow("channel", "telcoA") {
		t.Fatal("A second must be refused")
	}
	if !l.Allow("channel", "telcoB") {
		t.Fatal("B must have its own budget")
	}
}

// Unknown scope is fail-closed (denied), never unlimited.
func TestUnknownScope_Denied(t *testing.T) {
	l := New(map[string]Limit{"login": {RatePerMinute: 60, Burst: 5}})
	if l.Allow("channel", "k") {
		t.Fatal("a scope with no configured limit must be denied (fail-closed)")
	}
}

// Concurrent Allow is race-free and never over-admits past the burst.
func TestConcurrentAllow_NoOverAdmit(t *testing.T) {
	l := New(map[string]Limit{"login": {RatePerMinute: 0.0001, Burst: 100}})
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return base } // effectively no refill

	var mu sync.Mutex
	allowed := 0
	var wg sync.WaitGroup
	for i := 0; i < 500; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.Allow("login", "same") {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if allowed != 100 {
		t.Fatalf("exactly burst=100 must be admitted with no refill, got %d", allowed)
	}
}
