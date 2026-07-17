package configsvc_test

// M2 validator pack (BUILD_PLAN §7b): every rejection here is an
// armed-but-dead or safety-floor case — a config an admin could plausibly
// write that the engine cannot honor, or that silently disarms a control.

import (
	"context"
	"testing"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
)

const scoringScope = "programme:prg_sim_airtime01"

// validScoringPolicy is the seeded shape with one field mutated per case.
const validScoringPolicy = `{
  "gates":{"min_tenure_days":90,"blocked_statuses":["BARRED"],"require_activity_days":30},
  "staleness":{"accept_hours":48,"degrade_hours":168,"degrade_tier_cap":"TIER_01"},
  "missing_policy":"REJECT",
  "anti_gaming":{"window_days":90,"winsor_upper_bps":9500,"spike_ratio_max_bps":30000,"min_active_days":10},
  "tiers":[{"code":"TIER_01","max_face_minor":5000,"min_recharge_90d_minor":30000},
           {"code":"TIER_02","max_face_minor":10000,"min_recharge_90d_minor":90000}],
  "starter_tier":"TIER_01","one_tier_up_max":1,"decision_valid_hours":168}`

func mustReject(t *testing.T, svc *configsvc.Service, domain, scope, label, content string) {
	t.Helper()
	ctx := context.Background()
	c, err := svc.CreateDraft(ctx, domain, scope, "alice", label, []byte(content))
	if err != nil {
		t.Fatalf("%s: draft: %v", label, err)
	}
	if err := svc.Submit(ctx, c.ConfigVersionID, "alice"); err != nil {
		t.Fatalf("%s: submit: %v", label, err)
	}
	if err := svc.Approve(ctx, c.ConfigVersionID, "bob"); err == nil {
		t.Errorf("%s: must be rejected at approval", label)
	}
}

func TestM2_ScoringPolicyValidator_RejectsArmedButDead(t *testing.T) {
	svc, _ := newSvc(t, "cfg_m2scoring")

	cases := map[string]string{
		"inverted tier ladder":                  `{"gates":{"min_tenure_days":90},"staleness":{"accept_hours":48,"degrade_hours":168},"missing_policy":"REJECT","anti_gaming":{"window_days":90,"winsor_upper_bps":9500,"spike_ratio_max_bps":30000,"min_active_days":10},"tiers":[{"code":"T1","max_face_minor":10000,"min_recharge_90d_minor":90000},{"code":"T2","max_face_minor":5000,"min_recharge_90d_minor":30000}],"starter_tier":"T1","one_tier_up_max":1,"decision_valid_hours":168}`,
		"starter tier missing from ladder":      `{"gates":{"min_tenure_days":90},"staleness":{"accept_hours":48,"degrade_hours":168},"missing_policy":"REJECT","anti_gaming":{"window_days":90,"winsor_upper_bps":9500,"spike_ratio_max_bps":30000,"min_active_days":10},"tiers":[{"code":"T1","max_face_minor":5000,"min_recharge_90d_minor":30000}],"starter_tier":"T9","one_tier_up_max":1,"decision_valid_hours":168}`,
		"degrade window before accept window":   `{"gates":{"min_tenure_days":90},"staleness":{"accept_hours":168,"degrade_hours":48},"missing_policy":"REJECT","anti_gaming":{"window_days":90,"winsor_upper_bps":9500,"spike_ratio_max_bps":30000,"min_active_days":10},"tiers":[{"code":"T1","max_face_minor":5000,"min_recharge_90d_minor":30000}],"starter_tier":"T1","one_tier_up_max":1,"decision_valid_hours":168}`,
		"silent imputation policy":              `{"gates":{"min_tenure_days":90},"staleness":{"accept_hours":48,"degrade_hours":168},"missing_policy":"IMPUTE_ZERO","anti_gaming":{"window_days":90,"winsor_upper_bps":9500,"spike_ratio_max_bps":30000,"min_active_days":10},"tiers":[{"code":"T1","max_face_minor":5000,"min_recharge_90d_minor":30000}],"starter_tier":"T1","one_tier_up_max":1,"decision_valid_hours":168}`,
		"missing anti-gaming block entirely":    `{"gates":{"min_tenure_days":90},"staleness":{"accept_hours":48,"degrade_hours":168},"missing_policy":"REJECT","tiers":[{"code":"T1","max_face_minor":5000,"min_recharge_90d_minor":30000}],"starter_tier":"T1","one_tier_up_max":1,"decision_valid_hours":168}`,
		"spike cap below 1x rejects everything": `{"gates":{"min_tenure_days":90},"staleness":{"accept_hours":48,"degrade_hours":168},"missing_policy":"REJECT","anti_gaming":{"window_days":90,"winsor_upper_bps":9500,"spike_ratio_max_bps":5000,"min_active_days":10},"tiers":[{"code":"T1","max_face_minor":5000,"min_recharge_90d_minor":30000}],"starter_tier":"T1","one_tier_up_max":1,"decision_valid_hours":168}`,
		"non-expiring decisions":                `{"gates":{"min_tenure_days":90},"staleness":{"accept_hours":48,"degrade_hours":168},"missing_policy":"REJECT","anti_gaming":{"window_days":90,"winsor_upper_bps":9500,"spike_ratio_max_bps":30000,"min_active_days":10},"tiers":[{"code":"T1","max_face_minor":5000,"min_recharge_90d_minor":30000}],"starter_tier":"T1","one_tier_up_max":1,"decision_valid_hours":0}`,
		"negative limit":                        `{"gates":{"min_tenure_days":90},"staleness":{"accept_hours":48,"degrade_hours":168},"missing_policy":"REJECT","anti_gaming":{"window_days":90,"winsor_upper_bps":9500,"spike_ratio_max_bps":30000,"min_active_days":10},"tiers":[{"code":"T1","max_face_minor":-5000,"min_recharge_90d_minor":30000}],"starter_tier":"T1","one_tier_up_max":1,"decision_valid_hours":168}`,
	}
	for label, content := range cases {
		mustReject(t, svc, "scoring.policy", scoringScope, label, content)
	}

	// The seeded-shape policy approves.
	ctx := context.Background()
	good, err := svc.CreateDraft(ctx, "scoring.policy", scoringScope, "alice", "valid", []byte(validScoringPolicy))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Submit(ctx, good.ConfigVersionID, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Approve(ctx, good.ConfigVersionID, "bob"); err != nil {
		t.Fatalf("valid scoring policy must be approvable: %v", err)
	}
}

func TestM2_OverlaysValidator_ZeroConfigFloor(t *testing.T) {
	svc, _ := newSvc(t, "cfg_m2overlays")
	scope := "telco:SIM_NG"

	cases := map[string]string{
		"empty blocking list disarms overlays": `{"blocking_flags":[],"sim_swap_cooloff_hours":72,"check_at":["OFFER","CONFIRM"]}`,
		"unknown flag schema cannot store":     `{"blocking_flags":["SIM_SWAP","MAGIC_FLAG"],"sim_swap_cooloff_hours":72,"check_at":["CONFIRM"]}`,
		"overlays off at the money boundary":   `{"blocking_flags":["SIM_SWAP"],"sim_swap_cooloff_hours":72,"check_at":["OFFER"]}`,
		"missing cooloff":                      `{"blocking_flags":["SIM_SWAP"],"check_at":["CONFIRM"]}`,
	}
	for label, content := range cases {
		mustReject(t, svc, "overlays.policy", scope, label, content)
	}
}

func TestM2_NotifyTemplatesValidator(t *testing.T) {
	svc, _ := newSvc(t, "cfg_m2notify")
	scope := "telco:SIM_NG"

	cases := map[string]string{
		"no templates":        `{"sender_id":"BX","templates":{}}`,
		"template no version": `{"sender_id":"BX","templates":{"ADVANCE_CONFIRMED":{"body":"hi"}}}`,
		"template no body":    `{"sender_id":"BX","templates":{"ADVANCE_CONFIRMED":{"version":"v1"}}}`,
		"no sender":           `{"templates":{"ADVANCE_CONFIRMED":{"version":"v1","body":"hi"}}}`,
		"bad quiet hours":     `{"sender_id":"BX","quiet_hours":{"start":"9pm","end":"07:00"},"templates":{"ADVANCE_CONFIRMED":{"version":"v1","body":"hi"}}}`,
	}
	for label, content := range cases {
		mustReject(t, svc, "notify.templates", scope, label, content)
	}
}

func TestM2_SeededDomainsActive(t *testing.T) {
	svc, _ := newSvc(t, "cfg_m2seeds")
	ctx := context.Background()
	for domain, scope := range map[string]string{
		"scoring.policy":   scoringScope,
		"overlays.policy":  "telco:SIM_NG",
		"notify.templates": "telco:SIM_NG",
	} {
		if _, err := svc.ActiveAt(ctx, domain, scope, time.Now().UTC()); err != nil {
			t.Errorf("missing seeded ACTIVE default for %s (%s): %v", domain, scope, err)
		}
	}
}
