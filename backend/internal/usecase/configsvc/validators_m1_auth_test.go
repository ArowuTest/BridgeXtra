package configsvc_test

// Phase 1 S1 — telco.adapter outbound-auth block validation. Secret-safe and
// fail-closed: raw secrets are forbidden (env-var names only), mtls is refused
// until implemented, unknown schemes/fields are rejected, and each scheme's
// required fields are enforced. The pre-auth config (no auth block) still passes
// (backward compatible — the simulator needs no auth).

import (
	"context"
	"fmt"
	"testing"
)

// adapterContent wraps a valid telco.adapter base around an optional auth suffix
// (e.g. `,"auth":{...}`) so only the auth block is under test.
func adapterContent(authSuffix string) string {
	return fmt.Sprintf(`{"fulfilment_url":"https://mno.example/api","request_timeout_ms":2000,"retry_budget":0,"circuit_error_threshold_pct":50,"circuit_min_requests":20,"circuit_cooldown_seconds":30,"max_weekly_recharge_minor":100000000%s}`, authSuffix)
}

func TestS1_TelcoAdapterAuthValidator(t *testing.T) {
	svc, _ := newSvc(t, "cfg_s1_auth")
	scope := "telco:SIM_NG"

	for label, suffix := range map[string]string{
		"raw secret forbidden":         `,"auth":{"scheme":"apikey","header":"X-Api-Key","secret":"leaked"}`,
		"raw client_secret forbidden":  `,"auth":{"scheme":"oauth2","token_url":"https://x/t","client_id":"a","client_secret":"leaked"}`,
		"mtls not yet supported":       `,"auth":{"scheme":"mtls"}`,
		"unknown scheme":               `,"auth":{"scheme":"basic"}`,
		"scheme missing":               `,"auth":{"header":"X-Api-Key","secret_env":"E"}`,
		"apikey missing header":        `,"auth":{"scheme":"apikey","secret_env":"E"}`,
		"apikey missing secret_env":    `,"auth":{"scheme":"apikey","header":"X-Api-Key"}`,
		"oauth2 missing token_url":     `,"auth":{"scheme":"oauth2","client_id":"a","client_secret_env":"E"}`,
		"oauth2 bad token_url":         `,"auth":{"scheme":"oauth2","token_url":"notaurl","client_id":"a","client_secret_env":"E"}`,
		"oauth2 missing client_id":     `,"auth":{"scheme":"oauth2","token_url":"https://x/t","client_secret_env":"E"}`,
		"oauth2 missing client_secret": `,"auth":{"scheme":"oauth2","token_url":"https://x/t","client_id":"a"}`,
		"unknown auth field":           `,"auth":{"scheme":"apikey","header":"X-Api-Key","secret_env":"E","extra":1}`,
	} {
		mustReject(t, svc, "telco.adapter", scope, label, adapterContent(suffix))
	}

	// Valid shapes activate cleanly (validator runs at Approve).
	ctx := context.Background()
	for label, suffix := range map[string]string{
		"no auth block (backward compatible)": ``,
		"scheme none":                         `,"auth":{"scheme":"none"}`,
		"apikey valid":                        `,"auth":{"scheme":"apikey","header":"X-Api-Key","secret_env":"TCP_MNO_KEY"}`,
		"oauth2 valid":                        `,"auth":{"scheme":"oauth2","token_url":"https://mno.example/token","client_id":"bx","client_secret_env":"TCP_MNO_CS","scope":"fulfil"}`,
	} {
		c, err := svc.CreateDraft(ctx, "telco.adapter", scope, "alice", "auth "+label, []byte(adapterContent(suffix)))
		if err != nil {
			t.Fatalf("%s draft: %v", label, err)
		}
		if err := svc.Submit(ctx, c.ConfigVersionID, "alice"); err != nil {
			t.Fatalf("%s submit: %v", label, err)
		}
		if err := svc.Approve(ctx, c.ConfigVersionID, "bob"); err != nil {
			t.Fatalf("%s must approve: %v", label, err)
		}
	}
}
