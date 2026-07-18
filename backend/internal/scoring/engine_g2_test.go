package scoring

// G2 fold pack: F1 missing_policy is IMPLEMENTED (both values are real
// behaviors), F2 the spike consequence is an explicit engine decision, and
// F3's engine-side overflow guard refuses absurd values outright.

import (
	"strings"
	"testing"
)

// G2-F1: MISSING_FIELDS routes through the configured policy — REJECT makes
// the subject ineligible with the reason recorded; STARTER floors them.
func TestG2F1_MissingPolicy_BothValuesImplemented(t *testing.T) {
	reject := baseInput(steadyWeeks(50_000)) // merit TIER_04 if data were whole
	reject.Quality.Flags = []string{"MISSING_FIELDS"}
	reject.Policy.MissingPolicy = "REJECT"

	d, err := reject.Score()
	if err != nil {
		t.Fatal(err)
	}
	if d.Eligible || !hasReason(d, "MISSING_DATA_REJECTED") {
		t.Fatalf("REJECT policy must make missing-data subjects ineligible with reason: %+v", d)
	}

	starter := baseInput(steadyWeeks(50_000))
	starter.Quality.Flags = []string{"MISSING_FIELDS"}
	starter.Policy.MissingPolicy = "STARTER"

	d2, err := starter.Score()
	if err != nil {
		t.Fatal(err)
	}
	if !d2.Eligible || d2.TierCode != "TIER_01" || !hasReason(d2, "MISSING_DATA_STARTER") {
		t.Fatalf("STARTER policy must floor missing-data subjects at the starter tier with reason: %+v", d2)
	}

	// An unvalidated policy value reaching the engine is a refusal, not a
	// default (the validator forbids it; the engine does not trust that).
	bad := baseInput(steadyWeeks(50_000))
	bad.Quality.Flags = []string{"MISSING_FIELDS"}
	bad.Policy.MissingPolicy = "IMPUTE_ZERO"
	if _, err := bad.Score(); err == nil || !strings.Contains(err.Error(), "missing_policy") {
		t.Fatalf("unimplementable missing_policy must refuse, got %v", err)
	}
}

// G2-F2: the spike consequence is an explicit, recorded policy decision.
func TestG2F2_SpikeAction_ExplicitConsequence(t *testing.T) {
	// CAP_TO_STARTER: a spiky pattern is floored at the starter tier.
	capped := baseInput(steadyWeeks(10_000))
	capped.Features.WeeklyRechargeMinor[0] = 400_000
	capped.Policy.AntiGaming.SpikeAction = "CAP_TO_STARTER"

	d, err := capped.Score()
	if err != nil {
		t.Fatal(err)
	}
	if d.TierCode != "TIER_01" || !hasReason(d, "SPIKE_PATTERN_DETECTED") || !hasReason(d, "SPIKE_CAPPED_TO_STARTER") {
		t.Fatalf("CAP_TO_STARTER must floor the spiky subscriber with both facts recorded: %+v", d)
	}

	// FLAG_ONLY: winsorisation is the discount; the flag records the fact —
	// and no reason code ever asserts an action that did not happen.
	flagged := baseInput(steadyWeeks(10_000))
	flagged.Features.WeeklyRechargeMinor[0] = 400_000
	d2, err := flagged.Score()
	if err != nil {
		t.Fatal(err)
	}
	if !hasReason(d2, "SPIKE_PATTERN_DETECTED") || hasReason(d2, "SPIKE_CAPPED_TO_STARTER") {
		t.Fatalf("FLAG_ONLY must record detection and nothing else: %+v", d2)
	}
	for _, r := range d2.ReasonCodes {
		if strings.Contains(r, "DISCOUNT_APPLIED") {
			t.Fatalf("no reason code may assert an unapplied action: %v", d2.ReasonCodes)
		}
	}

	// An unvalidated action is a refusal, not a default.
	bad := baseInput(steadyWeeks(10_000))
	bad.Features.WeeklyRechargeMinor[0] = 400_000
	bad.Policy.AntiGaming.SpikeAction = "SHADOW_BAN"
	if _, err := bad.Score(); err == nil || !strings.Contains(err.Error(), "spike_action") {
		t.Fatalf("unimplementable spike_action must refuse, got %v", err)
	}
}

// G2-F3 (engine layer): a value that would overflow the bps arithmetic is
// refused outright — the ingest ceiling is the primary guard, this is the
// structural backstop.
func TestG2F3_OverflowShapedValue_RefusedNotMisscored(t *testing.T) {
	in := baseInput(steadyWeeks(1_000))
	in.Features.WeeklyRechargeMinor[0] = int64(1)<<62 + 12345

	if _, err := in.Score(); err == nil || !strings.Contains(err.Error(), "engine-safe range") {
		t.Fatalf("overflow-shaped value must be refused, got %v", err)
	}
}
