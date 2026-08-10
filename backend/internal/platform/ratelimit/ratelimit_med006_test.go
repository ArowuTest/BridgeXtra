package ratelimit

// BX-MED-006: the per-key table must be HARD-capped. The old code sweept idle buckets when the
// table reached maxKeys but then inserted the new key unconditionally — so a flood of distinct keys
// arriving faster than they go idle (all fresh) grew the map without bound: a memory-exhaustion DoS
// through the very control meant to bound abuse. The fix denies a new key when the table is full
// and nothing is reclaimable, holding memory at maxKeys.

import (
	"testing"
	"time"
)

func TestBXMED006_HardKeyCeiling_DeniesWhenFull(t *testing.T) {
	l := New(map[string]Limit{"login": {RatePerMinute: 60, Burst: 5}})
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return base } // fixed clock: nothing ever goes idle
	l.maxKeys = 3

	// Fill the table with 3 distinct, fresh keys — each first hit is admitted and creates a bucket.
	for _, k := range []string{"ip1", "ip2", "ip3"} {
		if !l.Allow("login", k) {
			t.Fatalf("first hit for %s must be admitted", k)
		}
	}
	if got := len(l.buckets); got != 3 {
		t.Fatalf("table should hold 3 buckets, got %d", got)
	}

	// A 4th distinct key while the table is full and NOTHING is idle. Load-bearing (BX-MED-006):
	// it must be DENIED and must NOT grow the map. Mutation proof: remove the `return false` in
	// Allow and this key is admitted and len(buckets) becomes 4 — RED on both assertions.
	if l.Allow("login", "ip4_flood") {
		t.Fatal("BX-MED-006: a new key must be DENIED when the table is full and nothing is idle")
	}
	if got := len(l.buckets); got != 3 {
		t.Fatalf("BX-MED-006: the key table must stay hard-capped at maxKeys=3, grew to %d", got)
	}
}

// The hard cap is not a permanent lockout: once buckets age out, a sweep reclaims space and new
// keys are admitted again — so a transient churn burst degrades gracefully rather than wedging.
func TestBXMED006_ReclaimsIdleThenAdmits(t *testing.T) {
	l := New(map[string]Limit{"login": {RatePerMinute: 60, Burst: 5}})
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := base
	l.now = func() time.Time { return now }
	l.maxKeys = 2

	if !l.Allow("login", "ip1") || !l.Allow("login", "ip2") {
		t.Fatal("first hits must be admitted")
	}
	if l.Allow("login", "ip3") {
		t.Fatal("full table, nothing idle -> a new key must be denied")
	}

	// Age past the idle window (2m) and the sweep interval; ip1/ip2 are now reclaimable.
	now = base.Add(3 * time.Minute)
	if !l.Allow("login", "ip3") {
		t.Fatal("after idle buckets age out, a sweep must reclaim space and admit a new key")
	}
	if got := len(l.buckets); got > l.maxKeys {
		t.Fatalf("table must stay within maxKeys after reclaim, got %d", got)
	}
}
