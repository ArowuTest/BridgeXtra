// Package scoring is the deterministic credit-decision engine (M2c,
// V2-SCR-003..010). It is PURE: no database, no clock, no randomness — every
// input arrives as an argument, so the same inputs always produce the same
// canonical decision bytes. That purity is what makes BC-4 bit-exact replay
// (V1-CRD-010 / V2-SCR-011) a hash comparison instead of a forensic exercise.
//
// All arithmetic is integer (minor units, day counts, basis points). Floats
// are banned here exactly as in the money path (BC-1) — the CI grep guards
// this package too.
package scoring

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"time"
)

// EngineVersion is part of every canonical decision. Any change to the
// algorithm below OR to the canonical document shape MUST bump this — replay
// compares like-for-like engines and a silent change would otherwise
// masquerade as data corruption.
//
// 1.1.0: canonical doc echoes ALL engine inputs (subscriber_status,
// prior_tier_code) so replay is self-contained — mutable external state
// (a status change after scoring) can never make a replay diverge.
//
// 1.2.0 (G2 folds): missing_policy is IMPLEMENTED (G2-F1 — MISSING_FIELDS
// quality flag routes through REJECT or STARTER per config; previously the
// validated knob was read by nothing); spike reason renamed to the factual
// SPIKE_PATTERN_DETECTED and the spike consequence is an explicit policy
// decision, anti_gaming.spike_action = FLAG_ONLY | CAP_TO_STARTER (G2-F2);
// weekly values are overflow-guarded at the engine boundary (G2-F3 defense
// in depth behind the ingest plausibility ceiling).
const EngineVersion = "bx-score-1.2.0"

// Features is the parsed canonical feature row (integer quantities only).
type Features struct {
	TenureDays          int     `json:"tenure_days"`
	ActivityDays30d     int     `json:"activity_days_30d"`
	ActiveDays90d       int     `json:"active_days_90d"`
	WeeklyRechargeMinor []int64 `json:"weekly_recharge_minor"`
	Currency            string  `json:"currency"`
}

// Quality carries the snapshot's data-quality flags (V2-SCR-002).
type Quality struct {
	Flags []string `json:"flags"`
}

// Policy is the parsed scoring.policy config (validated at approval by
// configsvc/validators_m2.go — the engine may trust its shape, not its
// presence: a missing policy is an error, never a default).
type Policy struct {
	Gates struct {
		MinTenureDays       int      `json:"min_tenure_days"`
		BlockedStatuses     []string `json:"blocked_statuses"`
		RequireActivityDays int      `json:"require_activity_days"`
	} `json:"gates"`
	Staleness struct {
		AcceptHours    int    `json:"accept_hours"`
		DegradeHours   int    `json:"degrade_hours"`
		DegradeTierCap string `json:"degrade_tier_cap"`
	} `json:"staleness"`
	MissingPolicy string `json:"missing_policy"`
	AntiGaming    struct {
		WindowDays       int    `json:"window_days"`
		WinsorUpperBps   int64  `json:"winsor_upper_bps"`
		SpikeRatioMaxBps int64  `json:"spike_ratio_max_bps"`
		MinActiveDays    int    `json:"min_active_days"`
		SpikeAction      string `json:"spike_action"` // FLAG_ONLY | CAP_TO_STARTER
	} `json:"anti_gaming"`
	Tiers              []Tier `json:"tiers"`
	StarterTier        string `json:"starter_tier"`
	OneTierUpMax       int    `json:"one_tier_up_max"`
	DecisionValidHours int    `json:"decision_valid_hours"`
}

type Tier struct {
	Code                string `json:"code"`
	MaxFaceMinor        int64  `json:"max_face_minor"`
	MinRecharge90dMinor int64  `json:"min_recharge_90d_minor"`
}

// Input is everything one decision depends on. PolicyVersionID and the
// feature snapshot hash ride into the canonical decision so the hash pins
// provenance, not just outcome.
type Input struct {
	Features           Features
	Quality            Quality
	FeatureContentHash string
	Policy             Policy
	PolicyVersionID    string
	SubscriberStatus   string // subscriber_accounts.status
	PriorTierCode      string // "" when no prior scored decision
	FeatureAsOf        time.Time
	ScoredAt           time.Time // caller's clock — engine takes no clock
}

// Decision is the §11.2 canonical result.
type Decision struct {
	Eligible         bool     `json:"eligible"`
	TierCode         string   `json:"tier_code,omitempty"`
	MaxFaceMinor     int64    `json:"maximum_face_value_minor"`
	Currency         string   `json:"currency"`
	ReasonCodes      []string `json:"reason_codes"`
	EngineVersion    string   `json:"engine_version"`
	PolicyVersionID  string   `json:"policy_version_id"`
	FeatureHash      string   `json:"feature_snapshot_hash"`
	FeatureAsOf      string   `json:"feature_as_of"`
	ScoredAt         string   `json:"scored_at"`
	ValidUntil       string   `json:"valid_until"`
	StalenessOutcome string   `json:"staleness_outcome"` // FRESH | DEGRADED | REJECTED
	// Input echoes: every engine input a replay cannot re-derive from
	// immutable stores rides in the doc itself.
	SubscriberStatus string `json:"subscriber_status"`
	PriorTierCode    string `json:"prior_tier_code"`
}

// Score computes one decision. Errors are contract violations (malformed
// inputs the store should have made impossible) — a scoring RUN treats them
// as skips with reasons, never guesses.
func (in Input) Score() (Decision, error) {
	f, p := in.Features, in.Policy
	if len(f.WeeklyRechargeMinor) != 13 {
		return Decision{}, fmt.Errorf("features must carry exactly 13 weekly totals, got %d", len(f.WeeklyRechargeMinor))
	}
	if len(p.Tiers) == 0 || in.PolicyVersionID == "" {
		return Decision{}, fmt.Errorf("policy with tiers and a version id is required")
	}
	if in.ScoredAt.IsZero() || in.FeatureAsOf.IsZero() {
		return Decision{}, fmt.Errorf("scored_at and feature_as_of are required")
	}
	// G2-F3 defense in depth: the ingest plausibility ceiling is the primary
	// guard; the engine still refuses values that would overflow its own
	// bps arithmetic rather than silently mis-scoring them.
	for i, w := range f.WeeklyRechargeMinor {
		if w < 0 || w > maxSafeWeeklyMinor {
			return Decision{}, fmt.Errorf("weekly_recharge_minor[%d]=%d outside engine-safe range [0,%d]", i, w, int64(maxSafeWeeklyMinor))
		}
	}

	d := Decision{
		Currency:         f.Currency,
		EngineVersion:    EngineVersion,
		PolicyVersionID:  in.PolicyVersionID,
		FeatureHash:      in.FeatureContentHash,
		FeatureAsOf:      in.FeatureAsOf.UTC().Format(time.RFC3339),
		ScoredAt:         in.ScoredAt.UTC().Format(time.RFC3339),
		ValidUntil:       in.ScoredAt.UTC().Add(time.Duration(p.DecisionValidHours) * time.Hour).Format(time.RFC3339),
		SubscriberStatus: in.SubscriberStatus,
		PriorTierCode:    in.PriorTierCode,
	}
	reason := func(codes ...string) {
		d.ReasonCodes = append(d.ReasonCodes, codes...)
	}

	// --- staleness (EDG-014 / V2-SCR-016): explicit policy, never silent ---
	age := in.ScoredAt.Sub(in.FeatureAsOf)
	switch {
	case age > time.Duration(p.Staleness.DegradeHours)*time.Hour:
		reason("FEATURES_STALE_REJECTED")
		d.StalenessOutcome = "REJECTED"
		return d, nil // ineligible: data too old to lend on
	case age > time.Duration(p.Staleness.AcceptHours)*time.Hour:
		d.StalenessOutcome = "DEGRADED"
		reason("FEATURES_STALE_DEGRADED")
	default:
		d.StalenessOutcome = "FRESH"
	}

	// --- eligibility gates (V2-SCR-001/006) ---
	for _, blocked := range p.Gates.BlockedStatuses {
		if in.SubscriberStatus == blocked {
			reason("BLOCKED_STATUS_" + blocked)
			return d, nil
		}
	}
	// --- missing data (G2-F1, V2-SCR-017): the telco's MISSING_FIELDS
	// quality flag routes through EXPLICIT policy — REJECT (ineligible) or
	// STARTER (progressive-trust floor). Never silent, never imputed.
	if hasFlag(in.Quality.Flags, "MISSING_FIELDS") {
		switch p.MissingPolicy {
		case "REJECT":
			reason("MISSING_DATA_REJECTED")
			return d, nil
		case "STARTER":
			return in.starter(d, "MISSING_DATA_STARTER"), nil
		default:
			// Validator forbids anything else; an unvalidated policy reaching
			// the engine is a contract violation, not a default.
			return Decision{}, fmt.Errorf("missing_policy %q is not implementable (must be REJECT or STARTER)", p.MissingPolicy)
		}
	}

	if f.TenureDays < p.Gates.MinTenureDays {
		// Not a rejection: the cold-start path (V2-SCR-009).
		return in.starter(d, "COLD_START_TENURE"), nil
	}
	reason("TENURE_OK")

	// --- thin-file / cold start (V2-SCR-009) ---
	if hasFlag(in.Quality.Flags, "SHORT_HISTORY") || f.ActiveDays90d < p.AntiGaming.MinActiveDays {
		return in.starter(d, "COLD_START_THIN_FILE"), nil
	}

	// --- anti-gaming (V2-SCR-003/004): winsorise + spike detection --------
	weekly := make([]int64, len(f.WeeklyRechargeMinor))
	copy(weekly, f.WeeklyRechargeMinor)
	capValue := percentileValue(weekly, p.AntiGaming.WinsorUpperBps)
	med := medianValue(weekly)
	spiky := false
	if med > 0 {
		maxW := weekly[0]
		for _, w := range weekly {
			if w > maxW {
				maxW = w
			}
		}
		// integer bps ratio: max/median
		if maxW*10_000/med > p.AntiGaming.SpikeRatioMaxBps {
			spiky = true
		}
	} else if maxOf(weekly) > 0 {
		// All-but-one weeks empty: the purest gaming shape.
		spiky = true
	}
	var total90 int64
	for _, w := range weekly {
		if w > capValue {
			w = capValue // winsorised: one big week cannot buy a tier
		}
		total90 += w
	}
	// G2-F2: the reason code states the FACT (a spike pattern was detected);
	// the CONSEQUENCE is an explicit, recorded policy decision. FLAG_ONLY
	// relies on winsorisation alone; CAP_TO_STARTER additionally floors the
	// spiky subscriber at the starter tier for this cycle.
	if spiky {
		reason("SPIKE_PATTERN_DETECTED")
		switch p.AntiGaming.SpikeAction {
		case "FLAG_ONLY":
			// winsorisation is the only discount — recorded by the flag alone
		case "CAP_TO_STARTER":
			return in.starter(d, "SPIKE_CAPPED_TO_STARTER"), nil
		default:
			return Decision{}, fmt.Errorf("anti_gaming.spike_action %q is not implementable (must be FLAG_ONLY or CAP_TO_STARTER)", p.AntiGaming.SpikeAction)
		}
	}

	// --- tier assignment: highest tier whose threshold the winsorised
	// window clears (V2-SCR-006 limit waterfall starts at the tier cap) ---
	target := -1
	for i, t := range p.Tiers {
		if total90 >= t.MinRecharge90dMinor {
			target = i
		}
	}
	if target < 0 {
		return in.starter(d, "BELOW_LOWEST_TIER"), nil
	}

	// --- movement constraint (V2-SCR-007/008): up at most one_tier_up_max
	// per cycle; down is immediate ---
	prior := tierIndex(p.Tiers, in.PriorTierCode)
	if prior >= 0 && target > prior+p.OneTierUpMax {
		target = prior + p.OneTierUpMax
		reason("TIER_MOVEMENT_CAPPED")
	}
	if prior >= 0 && target < prior {
		reason("TIER_DOWNGRADED")
	}

	// --- staleness degradation cap (explicit, recorded) ---
	if d.StalenessOutcome == "DEGRADED" && p.Staleness.DegradeTierCap != "" {
		if capIdx := tierIndex(p.Tiers, p.Staleness.DegradeTierCap); capIdx >= 0 && target > capIdx {
			target = capIdx
			reason("STALE_TIER_CAPPED")
		}
	}

	d.Eligible = true
	d.TierCode = p.Tiers[target].Code
	d.MaxFaceMinor = p.Tiers[target].MaxFaceMinor
	reason("RECHARGE_TIER_" + d.TierCode)
	return d, nil
}

// starter returns the configured starter-tier decision (V2-SCR-009).
func (in Input) starter(d Decision, code string) Decision {
	idx := tierIndex(in.Policy.Tiers, in.Policy.StarterTier)
	d.Eligible = true
	d.TierCode = in.Policy.Tiers[idx].Code
	d.MaxFaceMinor = in.Policy.Tiers[idx].MaxFaceMinor
	d.ReasonCodes = append(d.ReasonCodes, code, "STARTER_POLICY_"+d.TierCode)
	return d
}

// CanonicalJSON renders the decision as canonical bytes: fixed field order
// (struct order), no HTML escaping, no indentation. These are the bytes the
// decision_hash pins — replay equality is byte equality.
func (d Decision) CanonicalJSON() ([]byte, error) {
	if d.ReasonCodes == nil {
		d.ReasonCodes = []string{}
	}
	return json.Marshal(d)
}

// maxSafeWeeklyMinor bounds a single weekly value so the bps spike ratio
// (value * 10_000) can never overflow int64: MaxInt64/10_000 ≈ 922 trillion
// kobo — far above any plausible recharge, exactly at the arithmetic cliff.
const maxSafeWeeklyMinor = math.MaxInt64 / 10_000

// --- integer helpers (deterministic, allocation-light) ---

func hasFlag(flags []string, want string) bool {
	for _, f := range flags {
		if f == want {
			return true
		}
	}
	return false
}

func tierIndex(tiers []Tier, code string) int {
	for i, t := range tiers {
		if t.Code == code {
			return i
		}
	}
	return -1
}

func maxOf(v []int64) int64 {
	m := v[0]
	for _, x := range v {
		if x > m {
			m = x
		}
	}
	return m
}

// percentileValue returns the value at the given basis-point percentile of
// the sorted slice (nearest-rank, deterministic).
func percentileValue(v []int64, bps int64) int64 {
	s := make([]int64, len(v))
	copy(s, v)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	rank := (bps*int64(len(s)) + 9_999) / 10_000 // ceil
	if rank < 1 {
		rank = 1
	}
	if rank > int64(len(s)) {
		rank = int64(len(s))
	}
	return s[rank-1]
}

// medianValue is the lower median of the slice (deterministic integer).
func medianValue(v []int64) int64 {
	s := make([]int64, len(v))
	copy(s, v)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[(len(s)-1)/2]
}
