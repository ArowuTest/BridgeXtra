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

// CFG-012 (M3e): a template that could EVER post unbalanced cannot activate;
// the proof is symbolic, so it covers every binding and every
// omit_when_zero branch.
func TestM3E_TemplateValidator_SymbolicBalanceProof(t *testing.T) {
	svc, _ := newSvc(t, "cfg_m3etpl")
	cases := map[string]string{
		"plainly unbalanced": `{"templates":{"X":{"lines":[
			{"account":"SUBSCRIBER_RECEIVABLE","side":"DEBIT","amount":"AMOUNT"},
			{"account":"FEE_INCOME","side":"CREDIT","amount":"FEE"}]}}}`,
		"subtle: outstanding vs disbursed only": `{"templates":{"X":{"lines":[
			{"account":"SUBSCRIBER_RECEIVABLE","side":"DEBIT","amount":"OUTSTANDING"},
			{"account":"AIRTIME_FUNDING_CLEARING","side":"CREDIT","amount":"DISBURSED"}]}}}`,
		"unknown symbol": `{"templates":{"X":{"lines":[
			{"account":"SUBSCRIBER_RECEIVABLE","side":"DEBIT","amount":"MAGIC"},
			{"account":"FEE_INCOME","side":"CREDIT","amount":"MAGIC"}]}}}`,
		"account not on chart": `{"templates":{"X":{"lines":[
			{"account":"SLUSH_FUND","side":"DEBIT","amount":"AMOUNT"},
			{"account":"FEE_INCOME","side":"CREDIT","amount":"AMOUNT"}]}}}`,
		"single line": `{"templates":{"X":{"lines":[
			{"account":"FEE_INCOME","side":"DEBIT","amount":"AMOUNT"}]}}}`,
		"empty set halts posting": `{"templates":{}}`,
	}
	for label, content := range cases {
		mustReject(t, svc, "ledger.templates", "global", label, content)
	}

	// The seeded shape (OUTSTANDING = DISBURSED + FEE identity, optional fee
	// line) approves — the identity is what makes it balance.
	ctx := context.Background()
	good, err := svc.CreateDraft(ctx, "ledger.templates", "global", "alice", "valid", []byte(`{"templates":{"ADVANCE_ISSUED":{"lines":[
		{"account":"SUBSCRIBER_RECEIVABLE","side":"DEBIT","amount":"OUTSTANDING"},
		{"account":"AIRTIME_FUNDING_CLEARING","side":"CREDIT","amount":"DISBURSED"},
		{"account":"FEE_INCOME","side":"CREDIT","amount":"FEE","omit_when_zero":true}]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Submit(ctx, good.ConfigVersionID, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Approve(ctx, good.ConfigVersionID, "bob"); err != nil {
		t.Fatalf("the balanced identity template must approve: %v", err)
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
