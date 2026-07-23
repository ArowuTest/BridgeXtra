package configsvc_test

// Deferred fee recognition (M3f) config validators:
//  - fee_recognition: only UPFRONT|DEFERRED activate; absent/unknown refused so
//    the fail-closed origination read can never resolve a policy the posting
//    engine does not understand.
//  - R-SEED-VALIDATE: the ledger.templates v2 JSON shipped in migration 0049
//    (with the deferred-fee recognition legs) PASSES validateLedgerTemplates
//    against the ACTIVE v3 chart — the seed bypasses the validator at migration
//    time, so this proves in CI that the shipped templates would have passed.

import (
	"context"
	"testing"
)

func TestM3F_FeeRecognitionValidator(t *testing.T) {
	svc, _ := newSvc(t, "cfg_m3f_feerec")
	scope := "programme:prg_sim_airtime01"

	for label, content := range map[string]string{
		"policy absent":  `{}`,
		"policy null":    `{"policy":null}`,
		"unknown policy": `{"policy":"MONTHLY"}`,
		"empty policy":   `{"policy":""}`,
	} {
		mustReject(t, svc, "fee_recognition", scope, label, content)
	}

	// Both valid policies activate cleanly.
	ctx := context.Background()
	for _, policy := range []string{"UPFRONT", "DEFERRED"} {
		c, err := svc.CreateDraft(ctx, "fee_recognition", scope, "alice", "set "+policy,
			[]byte(`{"policy":"`+policy+`"}`))
		if err != nil {
			t.Fatalf("draft %s: %v", policy, err)
		}
		if err := svc.Submit(ctx, c.ConfigVersionID, "alice"); err != nil {
			t.Fatalf("submit %s: %v", policy, err)
		}
		if err := svc.Approve(ctx, c.ConfigVersionID, "bob"); err != nil {
			t.Fatalf("approve %s must succeed: %v", policy, err)
		}
	}
}

// The exact templates v2 JSON from migration 0049 must pass the CFG-012 balance
// validator against the ACTIVE chart (which migration 0049 advanced to v3 with
// UNEARNED_FEE). If any augmented template were unbalanced or named an off-chart
// account, this Approve would fail.
func TestM3F_SeededTemplatesV2_PassValidator(t *testing.T) {
	svc, _ := newSvc(t, "cfg_m3f_tplseed")
	ctx := context.Background()
	const templatesV2 = `{"templates":{"ADVANCE_ISSUED":{"lines":[{"account":"SUBSCRIBER_RECEIVABLE","side":"DEBIT","amount":"OUTSTANDING"},{"account":"AIRTIME_FUNDING_CLEARING","side":"CREDIT","amount":"DISBURSED"},{"account":"FEE_INCOME","side":"CREDIT","amount":"FEE","omit_when_zero":true},{"account":"FEE_INCOME","side":"DEBIT","amount":"FEE_DEFER_ADJ","omit_when_zero":true},{"account":"UNEARNED_FEE","side":"CREDIT","amount":"FEE_DEFER_ADJ","omit_when_zero":true}]},"RECOVERY_APPLIED":{"lines":[{"account":"TELCO_SETTLEMENT_RECEIVABLE","side":"DEBIT","amount":"AMOUNT"},{"account":"SUBSCRIBER_RECEIVABLE","side":"CREDIT","amount":"AMOUNT"},{"account":"UNEARNED_FEE","side":"DEBIT","amount":"FEE_RECOGNIZED","omit_when_zero":true},{"account":"FEE_INCOME","side":"CREDIT","amount":"FEE_RECOGNIZED","omit_when_zero":true}]},"RECOVERY_SUSPENSE":{"lines":[{"account":"TELCO_SETTLEMENT_RECEIVABLE","side":"DEBIT","amount":"AMOUNT"},{"account":"RECOVERY_SUSPENSE","side":"CREDIT","amount":"AMOUNT"}]},"RECOVERY_REVERSED":{"lines":[{"account":"SUBSCRIBER_RECEIVABLE","side":"DEBIT","amount":"AMOUNT"},{"account":"TELCO_SETTLEMENT_RECEIVABLE","side":"CREDIT","amount":"AMOUNT"},{"account":"FEE_INCOME","side":"DEBIT","amount":"FEE_RECOGNIZED","omit_when_zero":true},{"account":"UNEARNED_FEE","side":"CREDIT","amount":"FEE_RECOGNIZED","omit_when_zero":true}]},"RECOVERY_QUARANTINED":{"lines":[{"account":"TELCO_SETTLEMENT_RECEIVABLE","side":"DEBIT","amount":"AMOUNT"},{"account":"RECOVERY_SUSPENSE","side":"CREDIT","amount":"AMOUNT"}]},"WRITEOFF_RECOVERY_INC":{"lines":[{"account":"TELCO_SETTLEMENT_RECEIVABLE","side":"DEBIT","amount":"AMOUNT"},{"account":"WRITEOFF_RECOVERY_INCOME","side":"CREDIT","amount":"AMOUNT"}]},"WRITE_OFF":{"lines":[{"account":"WRITE_OFF_EXPENSE","side":"DEBIT","amount":"AMOUNT"},{"account":"SUBSCRIBER_RECEIVABLE","side":"CREDIT","amount":"AMOUNT"},{"account":"UNEARNED_FEE","side":"DEBIT","amount":"FEE_UNEARNED_REVERSED","omit_when_zero":true},{"account":"WRITE_OFF_EXPENSE","side":"CREDIT","amount":"FEE_UNEARNED_REVERSED","omit_when_zero":true}]}}}`

	c, err := svc.CreateDraft(ctx, "ledger.templates", "global", "alice", "re-validate v2", []byte(templatesV2))
	if err != nil {
		t.Fatalf("draft: %v", err)
	}
	if err := svc.Submit(ctx, c.ConfigVersionID, "alice"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if err := svc.Approve(ctx, c.ConfigVersionID, "bob"); err != nil {
		t.Fatalf("seeded templates v2 must pass validateLedgerTemplates against the active v3 chart: %v", err)
	}
}
