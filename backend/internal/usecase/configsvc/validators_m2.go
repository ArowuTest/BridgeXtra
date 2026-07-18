package configsvc

// M2 domain validators (BUILD_PLAN §7b). Same contract as validators_m1.go:
// a value the engine cannot honor is REJECTED at approval, never
// stored-and-ignored (SF-2 armed-but-dead prevention), and safety-relevant
// lists have zero-config floors — an empty blocking list is a rejection, not
// an allow-everything.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
)

func init() {
	validators["scoring.policy"] = validateScoringPolicy
	validators["overlays.policy"] = validateOverlaysPolicy
	validators["notify.templates"] = validateNotifyTemplates
}

// knownFlags MUST stay in sync with the subscriber_flags CHECK in 0007 —
// approving a flag the schema cannot store would be armed-but-dead.
var knownFlags = map[string]bool{
	"SIM_SWAP": true, "BARRED": true, "SELF_EXCLUDED": true,
	"FRAUD_SUSPECT": true, "DECEASED": true,
}

func validateScoringPolicy(ctx context.Context, tx pgx.Tx, content json.RawMessage) error {
	var v struct {
		Gates *struct {
			MinTenureDays       *int     `json:"min_tenure_days"`
			BlockedStatuses     []string `json:"blocked_statuses"`
			RequireActivityDays *int     `json:"require_activity_days"`
		} `json:"gates"`
		Staleness *struct {
			AcceptHours    *int    `json:"accept_hours"`
			DegradeHours   *int    `json:"degrade_hours"`
			DegradeTierCap *string `json:"degrade_tier_cap"`
		} `json:"staleness"`
		MissingPolicy *string `json:"missing_policy"`
		AntiGaming    *struct {
			WindowDays       *int    `json:"window_days"`
			WinsorUpperBps   *int64  `json:"winsor_upper_bps"`
			SpikeRatioMaxBps *int64  `json:"spike_ratio_max_bps"`
			MinActiveDays    *int    `json:"min_active_days"`
			SpikeAction      *string `json:"spike_action"`
		} `json:"anti_gaming"`
		Tiers []struct {
			Code                *string `json:"code"`
			MaxFaceMinor        *int64  `json:"max_face_minor"`
			MinRecharge90dMinor *int64  `json:"min_recharge_90d_minor"`
		} `json:"tiers"`
		StarterTier        *string `json:"starter_tier"`
		OneTierUpMax       *int    `json:"one_tier_up_max"`
		DecisionValidHours *int    `json:"decision_valid_hours"`
	}
	if err := json.Unmarshal(content, &v); err != nil {
		return fmt.Errorf("parse: %w", err)
	}

	if v.Gates == nil || v.Gates.MinTenureDays == nil || *v.Gates.MinTenureDays < 0 {
		return fmt.Errorf("gates.min_tenure_days is required and must be >= 0 (V2-SCR-001)")
	}

	// Staleness policy is a SAFETY control (EDG-014 / V2-SCR-016): both
	// windows required, degrade window must not precede accept window.
	if v.Staleness == nil || v.Staleness.AcceptHours == nil || v.Staleness.DegradeHours == nil {
		return fmt.Errorf("staleness.accept_hours and staleness.degrade_hours are required (V2-SCR-016)")
	}
	if *v.Staleness.AcceptHours <= 0 || *v.Staleness.DegradeHours < *v.Staleness.AcceptHours {
		return fmt.Errorf("staleness windows must satisfy 0 < accept_hours <= degrade_hours")
	}

	if v.MissingPolicy == nil || (*v.MissingPolicy != "REJECT" && *v.MissingPolicy != "STARTER") {
		return fmt.Errorf("missing_policy must be REJECT or STARTER — silent imputation is forbidden (V2-SCR-017)")
	}

	if v.AntiGaming == nil {
		return fmt.Errorf("anti_gaming is required (V2-SCR-003: single-period totals must not set limits unchecked)")
	}
	ag := v.AntiGaming
	if ag.WindowDays == nil || *ag.WindowDays <= 0 {
		return fmt.Errorf("anti_gaming.window_days must be > 0")
	}
	// The canonical feature window is 13 weeks. Nearest-rank percentile above
	// 12/13 (9230 bps) selects the MAXIMUM itself — winsorisation would cap
	// nothing and the anti-gaming control would be armed-but-dead. Found live:
	// the EDG-013 wash-pattern test bought TIER_04 under a 9500 bps cap.
	if ag.WinsorUpperBps == nil || *ag.WinsorUpperBps <= 0 || *ag.WinsorUpperBps > 9_230 {
		return fmt.Errorf("anti_gaming.winsor_upper_bps must be in (0,9230] — above 12/13 the cap equals the max and winsorisation is disarmed (13-week canonical window)")
	}
	if ag.SpikeRatioMaxBps == nil || *ag.SpikeRatioMaxBps < 10_000 {
		return fmt.Errorf("anti_gaming.spike_ratio_max_bps must be >= 10000 (a cap below 1x rejects everything)")
	}
	if ag.MinActiveDays == nil || *ag.MinActiveDays < 0 || *ag.MinActiveDays > *ag.WindowDays {
		return fmt.Errorf("anti_gaming.min_active_days must be within [0, window_days]")
	}
	// G2-F2: the spike consequence is an explicit policy decision the engine
	// implements — both values are real behaviors, so the knob can never be
	// armed-but-dead.
	if ag.SpikeAction == nil || (*ag.SpikeAction != "FLAG_ONLY" && *ag.SpikeAction != "CAP_TO_STARTER") {
		return fmt.Errorf("anti_gaming.spike_action must be FLAG_ONLY or CAP_TO_STARTER (G2-F2: the spike consequence is an explicit recorded decision)")
	}

	if len(v.Tiers) == 0 {
		return fmt.Errorf("tiers must be non-empty")
	}
	seen := map[string]bool{}
	var prevFace, prevRecharge int64
	for i, t := range v.Tiers {
		if t.Code == nil || *t.Code == "" {
			return fmt.Errorf("tiers[%d]: code is required", i)
		}
		if seen[*t.Code] {
			return fmt.Errorf("tiers[%d]: duplicate code %q", i, *t.Code)
		}
		seen[*t.Code] = true
		if t.MaxFaceMinor == nil || *t.MaxFaceMinor <= 0 {
			return fmt.Errorf("tiers[%d] (%s): max_face_minor must be > 0 (no negative limits)", i, *t.Code)
		}
		if t.MinRecharge90dMinor == nil || *t.MinRecharge90dMinor < 0 {
			return fmt.Errorf("tiers[%d] (%s): min_recharge_90d_minor must be >= 0", i, *t.Code)
		}
		// Monotonic ladder: a higher tier must demand more and permit more —
		// an inverted ladder silently caps everyone at the wrong tier.
		if i > 0 && (*t.MaxFaceMinor <= prevFace || *t.MinRecharge90dMinor <= prevRecharge) {
			return fmt.Errorf("tiers[%d] (%s): ladder must be strictly ascending in both max_face_minor and min_recharge_90d_minor", i, *t.Code)
		}
		prevFace, prevRecharge = *t.MaxFaceMinor, *t.MinRecharge90dMinor
	}
	if v.StarterTier == nil || !seen[*v.StarterTier] {
		return fmt.Errorf("starter_tier is required and must name an existing tier (V2-SCR-009 cold start)")
	}
	if v.Staleness.DegradeTierCap != nil && !seen[*v.Staleness.DegradeTierCap] {
		return fmt.Errorf("staleness.degrade_tier_cap must name an existing tier")
	}
	if v.OneTierUpMax == nil || *v.OneTierUpMax < 1 {
		return fmt.Errorf("one_tier_up_max is required and must be >= 1 (V2-SCR-007; 1 is the default policy)")
	}
	if v.DecisionValidHours == nil || *v.DecisionValidHours <= 0 {
		return fmt.Errorf("decision_valid_hours must be > 0 — decisions must expire (V2-SCR-015)")
	}
	return nil
}

func validateOverlaysPolicy(ctx context.Context, tx pgx.Tx, content json.RawMessage) error {
	var v struct {
		BlockingFlags       []string `json:"blocking_flags"`
		SimSwapCooloffHours *int     `json:"sim_swap_cooloff_hours"`
		CheckAt             []string `json:"check_at"`
	}
	if err := json.Unmarshal(content, &v); err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	// Zero-config floor: an empty blocking list would silently disable every
	// risk overlay — that is a rejection, not a configuration.
	if len(v.BlockingFlags) == 0 {
		return fmt.Errorf("blocking_flags must be non-empty (empty = overlays disarmed; reference_safety_control_zero_config_floor)")
	}
	for i, f := range v.BlockingFlags {
		if !knownFlags[f] {
			return fmt.Errorf("blocking_flags[%d]: unknown flag %q — schema cannot store it (armed-but-dead)", i, f)
		}
	}
	if v.SimSwapCooloffHours == nil || *v.SimSwapCooloffHours < 0 {
		return fmt.Errorf("sim_swap_cooloff_hours is required and must be >= 0")
	}
	if len(v.CheckAt) == 0 {
		return fmt.Errorf("check_at must be non-empty")
	}
	points := map[string]bool{"OFFER": true, "CONFIRM": true}
	sawConfirm := false
	for i, c := range v.CheckAt {
		if !points[c] {
			return fmt.Errorf("check_at[%d]: must be OFFER or CONFIRM", i)
		}
		if c == "CONFIRM" {
			sawConfirm = true
		}
	}
	// CONFIRM is the money-moving moment: overlays may not be configured off
	// there (V2-SCR-015 real-time evaluation is a Must).
	if !sawConfirm {
		return fmt.Errorf("check_at must include CONFIRM — overlays cannot be disabled at the money-moving boundary")
	}
	return nil
}

func validateNotifyTemplates(ctx context.Context, tx pgx.Tx, content json.RawMessage) error {
	var v struct {
		SenderID   *string `json:"sender_id"`
		QuietHours *struct {
			Start *string `json:"start"`
			End   *string `json:"end"`
		} `json:"quiet_hours"`
		Templates map[string]struct {
			Version *string `json:"version"`
			Body    *string `json:"body"`
		} `json:"templates"`
	}
	if err := json.Unmarshal(content, &v); err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	if v.SenderID == nil || *v.SenderID == "" {
		return fmt.Errorf("sender_id is required")
	}
	if len(v.Templates) == 0 {
		return fmt.Errorf("templates must be non-empty")
	}
	for kind, t := range v.Templates {
		if t.Version == nil || *t.Version == "" {
			return fmt.Errorf("templates[%s]: version is required (evidence pins the template version)", kind)
		}
		if t.Body == nil || *t.Body == "" {
			return fmt.Errorf("templates[%s]: body is required", kind)
		}
	}
	if v.QuietHours != nil {
		for name, s := range map[string]*string{"start": v.QuietHours.Start, "end": v.QuietHours.End} {
			if s == nil || len(*s) != 5 || (*s)[2] != ':' {
				return fmt.Errorf("quiet_hours.%s must be HH:MM", name)
			}
		}
	}
	return nil
}
