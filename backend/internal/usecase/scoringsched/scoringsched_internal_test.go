package scoringsched

// Pure-logic tests for the two decisions that don't need a database: the cadence
// clamp (property 2: cadence honored + never disarm) and the terminal-status
// classifier (property 3: decisions fresh, frozen-feed loud not silent).

import (
	"testing"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
)

func TestEffectiveCadence(t *testing.T) {
	cases := []struct {
		cadence, valid, headroom int
		wantEff                  int
		wantClamp, wantFloor     bool
	}{
		{24, 168, 1, 24, false, false}, // 24 <= 168/2 -> no clamp
		{100, 168, 1, 84, true, false}, // clamp to valid/2
		{6, 168, 3, 6, false, false},   // 6 <= 168/4=42
		{50, 168, 3, 42, true, false},  // clamp to valid/4
		{24, 2, 1, 1, true, false},     // maxEff=1 -> clamp to 1 (not floored)
		{24, 1, 1, 1, true, true},      // maxEff=0 -> floored to 1
	}
	for _, c := range cases {
		eff, clamp, floor := effectiveCadence(c.cadence, c.valid, c.headroom)
		if eff != c.wantEff || clamp != c.wantClamp || floor != c.wantFloor {
			t.Errorf("effectiveCadence(%d,%d,%d) = (%d,%v,%v), want (%d,%v,%v)",
				c.cadence, c.valid, c.headroom, eff, clamp, floor, c.wantEff, c.wantClamp, c.wantFloor)
		}
		if eff < 1 {
			t.Errorf("effective cadence must never drop below 1h, got %d", eff)
		}
		// The safety invariant: effective cadence must leave freshness headroom, i.e.
		// eff*(headroom+1) <= valid (unless floored, where valid is simply too small).
		if !floor && eff*(c.headroom+1) > c.valid {
			t.Errorf("effectiveCadence(%d,%d,%d)=%d leaves no headroom: %d*%d > %d",
				c.cadence, c.valid, c.headroom, eff, eff, c.headroom+1, c.valid)
		}
	}
}

func TestClassifyTerminal(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	eff := 1
	cases := []struct {
		name    string
		scored  int
		freshVU time.Time
		have    bool
		want    string
	}{
		{"new decisions -> succeeded", 5, time.Time{}, false, entity.CycleSucceeded},
		{"replay, decisions comfortably fresh -> succeeded", 0, now.Add(2 * time.Hour), true, entity.CycleSucceeded},
		{"replay, decisions near expiry -> stale", 0, now.Add(30 * time.Minute), true, entity.CycleStaleNoRefresh},
		{"replay, decisions exactly at threshold -> stale (conservative)", 0, now.Add(time.Duration(eff) * time.Hour), true, entity.CycleStaleNoRefresh},
		{"replay, no decisions at all -> stale", 0, time.Time{}, false, entity.CycleStaleNoRefresh},
	}
	for _, c := range cases {
		if got := classifyTerminal(c.scored, c.freshVU, c.have, now, eff); got != c.want {
			t.Errorf("%s: classifyTerminal = %q, want %q", c.name, got, c.want)
		}
	}
}
