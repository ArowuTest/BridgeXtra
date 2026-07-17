package scoring

// M2c pack: EDG-013 anti-gaming scenarios, tier-movement constraints
// (V2-SCR-007/008), cold start (SCR-009), staleness (SCR-016/EDG-014), and
// the determinism property BC-4 replay rests on.

import (
	"bytes"
	"math/rand"
	"testing"
	"time"
)

func testPolicy() Policy {
	var p Policy
	p.Gates.MinTenureDays = 90
	p.Gates.BlockedStatuses = []string{"BARRED", "SELF_EXCLUDED", "CLOSED"}
	p.Staleness.AcceptHours = 48
	p.Staleness.DegradeHours = 168
	p.Staleness.DegradeTierCap = "TIER_01"
	p.MissingPolicy = "REJECT"
	p.AntiGaming.WindowDays = 90
	p.AntiGaming.WinsorUpperBps = 9_200
	p.AntiGaming.SpikeRatioMaxBps = 30_000
	p.AntiGaming.MinActiveDays = 10
	p.Tiers = []Tier{
		{Code: "TIER_01", MaxFaceMinor: 5_000, MinRecharge90dMinor: 30_000},
		{Code: "TIER_02", MaxFaceMinor: 10_000, MinRecharge90dMinor: 90_000},
		{Code: "TIER_03", MaxFaceMinor: 20_000, MinRecharge90dMinor: 200_000},
		{Code: "TIER_04", MaxFaceMinor: 50_000, MinRecharge90dMinor: 500_000},
	}
	p.StarterTier = "TIER_01"
	p.OneTierUpMax = 1
	p.DecisionValidHours = 168
	return p
}

func steadyWeeks(perWeek int64) []int64 {
	w := make([]int64, 13)
	for i := range w {
		w[i] = perWeek
	}
	return w
}

func baseInput(weekly []int64) Input {
	asOf := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)
	return Input{
		Features: Features{
			TenureDays: 400, ActivityDays30d: 25, ActiveDays90d: 80,
			WeeklyRechargeMinor: weekly, Currency: "NGN",
		},
		FeatureContentHash: "hash-test",
		Policy:             testPolicy(),
		PolicyVersionID:    "cfg_test_v1",
		SubscriberStatus:   "ACTIVE",
		FeatureAsOf:        asOf,
		ScoredAt:           asOf.Add(2 * time.Hour),
	}
}

func hasReason(d Decision, code string) bool {
	for _, r := range d.ReasonCodes {
		if r == code {
			return true
		}
	}
	return false
}

// EDG-013: a steady ₦100/week history with one enormous spike week must NOT
// buy a higher tier — the spike is winsorised away and flagged.
func TestEDG013_SpikeWeek_CannotBuyATier(t *testing.T) {
	steady := baseInput(steadyWeeks(10_000)) // 130,000 over 90d -> TIER_02
	spiked := baseInput(steadyWeeks(10_000))
	spiked.Features.WeeklyRechargeMinor = steadyWeeks(10_000)
	spiked.Features.WeeklyRechargeMinor[0] = 400_000 // one giant week

	dSteady, err := steady.Score()
	if err != nil {
		t.Fatal(err)
	}
	dSpiked, err := spiked.Score()
	if err != nil {
		t.Fatal(err)
	}
	if dSteady.TierCode != "TIER_02" {
		t.Fatalf("steady subscriber should be TIER_02, got %s (%v)", dSteady.TierCode, dSteady.ReasonCodes)
	}
	if dSpiked.TierCode != dSteady.TierCode {
		t.Fatalf("EDG-013: spike week moved tier %s -> %s — gaming works", dSteady.TierCode, dSpiked.TierCode)
	}
	if !hasReason(dSpiked, "SPIKE_DISCOUNT_APPLIED") {
		t.Fatalf("spike must be flagged, got %v", dSpiked.ReasonCodes)
	}
}

// EDG-013 variant: recharges concentrated into a single week of an otherwise
// empty window (wash pattern) yields the starter tier at most.
func TestEDG013_SingleWeekWash_StaysLow(t *testing.T) {
	in := baseInput(steadyWeeks(0))
	in.Features.WeeklyRechargeMinor[0] = 600_000 // would be TIER_04 unwinsorised

	d, err := in.Score()
	if err != nil {
		t.Fatal(err)
	}
	if !hasReason(d, "SPIKE_DISCOUNT_APPLIED") {
		t.Fatalf("wash pattern must be spike-flagged: %v", d.ReasonCodes)
	}
	if d.TierCode == "TIER_03" || d.TierCode == "TIER_04" {
		t.Fatalf("wash pattern bought %s — anti-gaming failed", d.TierCode)
	}
}

// V2-SCR-007: a subscriber whose history justifies TIER_04 but whose prior
// decision was TIER_01 moves up exactly one tier per cycle.
func TestSCR007_OneTierUpPerCycle(t *testing.T) {
	in := baseInput(steadyWeeks(50_000)) // 650k -> TIER_04 on merit
	in.PriorTierCode = "TIER_01"

	d, err := in.Score()
	if err != nil {
		t.Fatal(err)
	}
	if d.TierCode != "TIER_02" {
		t.Fatalf("one-tier-up: want TIER_02, got %s (%v)", d.TierCode, d.ReasonCodes)
	}
	if !hasReason(d, "TIER_MOVEMENT_CAPPED") {
		t.Fatalf("cap must be a recorded reason: %v", d.ReasonCodes)
	}
}

// V2-SCR-008: downward movement is immediate — no cap.
func TestSCR008_DownwardImmediate(t *testing.T) {
	in := baseInput(steadyWeeks(3_000)) // 39k -> TIER_01 on merit
	in.PriorTierCode = "TIER_04"

	d, err := in.Score()
	if err != nil {
		t.Fatal(err)
	}
	if d.TierCode != "TIER_01" {
		t.Fatalf("downward must be immediate: want TIER_01, got %s", d.TierCode)
	}
	if !hasReason(d, "TIER_DOWNGRADED") {
		t.Fatalf("downgrade must be a recorded reason: %v", d.ReasonCodes)
	}
}

// V2-SCR-009: short tenure and thin files land on the starter tier, eligible.
func TestSCR009_ColdStartPaths(t *testing.T) {
	tenure := baseInput(steadyWeeks(50_000))
	tenure.Features.TenureDays = 30

	thin := baseInput(steadyWeeks(50_000))
	thin.Features.ActiveDays90d = 3

	flagged := baseInput(steadyWeeks(50_000))
	flagged.Quality.Flags = []string{"SHORT_HISTORY"}

	for name, in := range map[string]Input{"tenure": tenure, "thin": thin, "flagged": flagged} {
		d, err := in.Score()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !d.Eligible || d.TierCode != "TIER_01" {
			t.Fatalf("%s cold start: want eligible TIER_01, got eligible=%v tier=%s (%v)",
				name, d.Eligible, d.TierCode, d.ReasonCodes)
		}
	}
}

// EDG-014 / V2-SCR-016: stale features follow explicit policy — degraded
// window caps the tier and says so; beyond the degrade window is a rejection.
func TestEDG014_StalenessPolicy(t *testing.T) {
	degraded := baseInput(steadyWeeks(50_000)) // merit TIER_04
	degraded.ScoredAt = degraded.FeatureAsOf.Add(72 * time.Hour)

	d, err := degraded.Score()
	if err != nil {
		t.Fatal(err)
	}
	if d.StalenessOutcome != "DEGRADED" || d.TierCode != "TIER_01" || !hasReason(d, "STALE_TIER_CAPPED") {
		t.Fatalf("degraded window must cap tier with reasons: %+v", d)
	}

	rejected := baseInput(steadyWeeks(50_000))
	rejected.ScoredAt = rejected.FeatureAsOf.Add(200 * time.Hour)
	d2, err := rejected.Score()
	if err != nil {
		t.Fatal(err)
	}
	if d2.Eligible || d2.StalenessOutcome != "REJECTED" {
		t.Fatalf("beyond degrade window must reject: %+v", d2)
	}
}

// Blocked statuses gate before anything else.
func TestGates_BlockedStatus(t *testing.T) {
	in := baseInput(steadyWeeks(50_000))
	in.SubscriberStatus = "SELF_EXCLUDED"
	d, err := in.Score()
	if err != nil {
		t.Fatal(err)
	}
	if d.Eligible || !hasReason(d, "BLOCKED_STATUS_SELF_EXCLUDED") {
		t.Fatalf("blocked status must be ineligible with reason: %+v", d)
	}
}

// BC-4 property: identical inputs -> identical canonical bytes, and every
// randomized decision is internally consistent (tier exists, face > 0 when
// eligible, reasons non-empty). Seeded and reproducible.
func TestDeterminism_CanonicalBytesStable(t *testing.T) {
	rng := rand.New(rand.NewSource(20260718))
	for i := 0; i < 5_000; i++ {
		weekly := make([]int64, 13)
		for w := range weekly {
			weekly[w] = rng.Int63n(200_000)
		}
		in := baseInput(weekly)
		in.Features.TenureDays = int(rng.Int31n(2000))
		in.Features.ActiveDays90d = int(rng.Int31n(91))
		if rng.Intn(5) == 0 {
			in.PriorTierCode = testPolicy().Tiers[rng.Intn(4)].Code
		}

		d1, err := in.Score()
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		d2, _ := in.Score()
		b1, err := d1.CanonicalJSON()
		if err != nil {
			t.Fatal(err)
		}
		b2, _ := d2.CanonicalJSON()
		if !bytes.Equal(b1, b2) {
			t.Fatalf("case %d: same input, different canonical bytes:\n%s\n%s", i, b1, b2)
		}
		if len(d1.ReasonCodes) == 0 {
			t.Fatalf("case %d: every decision carries reason codes (V2-SCR-010)", i)
		}
		if d1.Eligible {
			if tierIndex(in.Policy.Tiers, d1.TierCode) < 0 || d1.MaxFaceMinor <= 0 {
				t.Fatalf("case %d: eligible decision inconsistent: %+v", i, d1)
			}
		}
	}
}
