package configsvc_test

// R-P0-7: the disclosure.policy validator. Every rejection is a disclosure an
// admin could plausibly write that would NOT disclose — the exact conduct gap
// R-P0-7 closes. A body or total-cost template that omits the repayment total,
// an empty channel or locale set, or a default locale the policy does not
// support, must all be refused at approval; the seeded shape approves.

import (
	"context"
	"testing"
)

func TestRP07_DisclosurePolicyValidator(t *testing.T) {
	svc, _ := newSvc(t, "cfg_rp07disc")
	scope := "programme:prg_sim_airtime01"

	const valid = `{"template_id":"T","template_version":"v1","default_locale":"en-NG","supported_locales":["en-NG"],"allowed_channels":["USSD","APP"],"body_template":"You repay {{repayment}}.","total_cost_template":"Total {{repayment}}."}`

	cases := map[string]string{
		"missing template_id":             `{"template_version":"v1","default_locale":"en-NG","supported_locales":["en-NG"],"allowed_channels":["USSD"],"body_template":"repay {{repayment}}","total_cost_template":"total {{repayment}}"}`,
		"missing template_version":        `{"template_id":"T","default_locale":"en-NG","supported_locales":["en-NG"],"allowed_channels":["USSD"],"body_template":"repay {{repayment}}","total_cost_template":"total {{repayment}}"}`,
		"empty supported_locales":         `{"template_id":"T","template_version":"v1","default_locale":"en-NG","supported_locales":[],"allowed_channels":["USSD"],"body_template":"repay {{repayment}}","total_cost_template":"total {{repayment}}"}`,
		"default locale not supported":    `{"template_id":"T","template_version":"v1","default_locale":"fr-FR","supported_locales":["en-NG"],"allowed_channels":["USSD"],"body_template":"repay {{repayment}}","total_cost_template":"total {{repayment}}"}`,
		"empty allowed_channels":          `{"template_id":"T","template_version":"v1","default_locale":"en-NG","supported_locales":["en-NG"],"allowed_channels":[],"body_template":"repay {{repayment}}","total_cost_template":"total {{repayment}}"}`,
		"body omits repayment (disarmed)": `{"template_id":"T","template_version":"v1","default_locale":"en-NG","supported_locales":["en-NG"],"allowed_channels":["USSD"],"body_template":"You are borrowing {{face}}.","total_cost_template":"total {{repayment}}"}`,
		"total cost omits repayment":      `{"template_id":"T","template_version":"v1","default_locale":"en-NG","supported_locales":["en-NG"],"allowed_channels":["USSD"],"body_template":"repay {{repayment}}","total_cost_template":"cost is {{fee}}"}`,
	}
	for label, content := range cases {
		mustReject(t, svc, "disclosure.policy", scope, label, content)
	}

	// The seeded-shape policy approves.
	ctx := context.Background()
	good, err := svc.CreateDraft(ctx, "disclosure.policy", scope, "alice", "valid", []byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Submit(ctx, good.ConfigVersionID, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Approve(ctx, good.ConfigVersionID, "bob"); err != nil {
		t.Fatalf("valid disclosure policy must be approvable: %v", err)
	}
}
