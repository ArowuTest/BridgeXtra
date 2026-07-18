package configsvc_test

// M3 validator pack (BUILD_PLAN §7c): armed-but-dead and zero-config-floor
// rejections for the money-core domains, per the G3 gate criteria.

import (
	"context"
	"testing"
	"time"
)

const m3Scope = "programme:prg_sim_airtime01"

func TestM3_DelinquencyBucketsValidator(t *testing.T) {
	svc, _ := newSvc(t, "cfg_m3delinq")
	cases := map[string]string{
		"single bucket only":    `{"buckets":[{"code":"CURRENT","min_days_past_due":0}],"grace_days":0}`,
		"ladder not ascending":  `{"buckets":[{"code":"CURRENT","min_days_past_due":0},{"code":"DPD_30","min_days_past_due":31},{"code":"DPD_7","min_days_past_due":1}],"grace_days":0}`,
		"gap below the ladder":  `{"buckets":[{"code":"DPD_1","min_days_past_due":1},{"code":"DPD_30","min_days_past_due":31}],"grace_days":0}`,
		"duplicate bucket code": `{"buckets":[{"code":"CURRENT","min_days_past_due":0},{"code":"CURRENT","min_days_past_due":5}],"grace_days":0}`,
		"grace out of range":    `{"buckets":[{"code":"CURRENT","min_days_past_due":0},{"code":"DPD_1","min_days_past_due":1}],"grace_days":90}`,
	}
	for label, content := range cases {
		mustReject(t, svc, "delinquency.buckets", m3Scope, label, content)
	}
}

func TestM3_WriteoffPolicyValidator_MakerCheckerFloor(t *testing.T) {
	svc, _ := newSvc(t, "cfg_m3wo")
	cases := map[string]string{
		"maker-checker configured off": `{"min_bucket":"DPD_90_PLUS","require_distinct_approver":false,"post_writeoff_recovery":"RECOVERY_INCOME"}`,
		"maker-checker absent":         `{"min_bucket":"DPD_90_PLUS","post_writeoff_recovery":"RECOVERY_INCOME"}`,
		"unimplemented recovery mode":  `{"min_bucket":"DPD_90_PLUS","require_distinct_approver":true,"post_writeoff_recovery":"REOPEN_ADVANCE"}`,
		"no min bucket":                `{"require_distinct_approver":true,"post_writeoff_recovery":"RECOVERY_INCOME"}`,
	}
	for label, content := range cases {
		mustReject(t, svc, "writeoff.policy", m3Scope, label, content)
	}
}

func TestM3_TreasuryGuardrailsValidator_ZeroConfigFloors(t *testing.T) {
	svc, _ := newSvc(t, "cfg_m3tre")
	cases := map[string]string{
		"no daily cap":            `{"max_open_exposure_bps_of_committed":8000,"trip_action":"SUSPEND_PROGRAMME","rearm":"MAKER_CHECKER"}`,
		"zero daily cap":          `{"max_daily_disbursed_minor":0,"max_open_exposure_bps_of_committed":8000,"trip_action":"SUSPEND_PROGRAMME","rearm":"MAKER_CHECKER"}`,
		"trip does nothing":       `{"max_daily_disbursed_minor":50000000,"max_open_exposure_bps_of_committed":8000,"trip_action":"LOG_ONLY","rearm":"MAKER_CHECKER"}`,
		"rearm configured off":    `{"max_daily_disbursed_minor":50000000,"max_open_exposure_bps_of_committed":8000,"trip_action":"SUSPEND_PROGRAMME","rearm":"AUTO"}`,
		"exposure cap above 100%": `{"max_daily_disbursed_minor":50000000,"max_open_exposure_bps_of_committed":12000,"trip_action":"SUSPEND_PROGRAMME","rearm":"MAKER_CHECKER"}`,
	}
	for label, content := range cases {
		mustReject(t, svc, "treasury.guardrails", m3Scope, label, content)
	}
}

func TestM3_SettlementTermsValidator_SharesPartitionExactly(t *testing.T) {
	svc, _ := newSvc(t, "cfg_m3set")
	cases := map[string]string{
		"shares under 100%": `{"cycle":"MONTHLY","telco_share_bps":2500,"platform_share_bps":7000,"taxes":[],"tolerance_minor":0}`,
		"shares over 100%":  `{"cycle":"MONTHLY","telco_share_bps":5000,"platform_share_bps":6000,"taxes":[],"tolerance_minor":0}`,
		"bad cycle":         `{"cycle":"DAILY","telco_share_bps":2500,"platform_share_bps":7500,"taxes":[],"tolerance_minor":0}`,
		"duplicate tax":     `{"cycle":"MONTHLY","telco_share_bps":2500,"platform_share_bps":7500,"taxes":[{"code":"VAT","bps":750},{"code":"VAT","bps":750}],"tolerance_minor":0}`,
		"no tolerance":      `{"cycle":"MONTHLY","telco_share_bps":2500,"platform_share_bps":7500,"taxes":[]}`,
	}
	for label, content := range cases {
		mustReject(t, svc, "settlement.terms", m3Scope, label, content)
	}
}

func TestM3_SeededDomainsActive(t *testing.T) {
	svc, _ := newSvc(t, "cfg_m3seeds")
	ctx := context.Background()
	for _, domain := range []string{
		"delinquency.buckets", "writeoff.policy", "treasury.guardrails", "settlement.terms",
	} {
		if _, err := svc.ActiveAt(ctx, domain, m3Scope, time.Now().UTC()); err != nil {
			t.Errorf("missing seeded ACTIVE default for %s: %v", domain, err)
		}
	}
}
