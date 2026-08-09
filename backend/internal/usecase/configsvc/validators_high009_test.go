package configsvc_test

// BX-HIGH-009: a REAL (non-synthetic) telco's adapter config may not arm over plaintext
// http or with no outbound auth — only the synthetic simulator may. The rule reads
// telcos.is_synthetic (a privileged marker config cannot flip), scoped by the config's
// telco (telco:<id>). The content validator accepts http/none (the simulator needs
// them); this scope-aware half refuses them for a real telco.

import (
	"context"
	"testing"
	"time"
)

func TestBXHIGH009_RealTelcoAdapterRequiresHTTPSAndAuth(t *testing.T) {
	svc, db := newSvc(t, "cfg_h009")
	ctx := context.Background()

	// A real (non-synthetic) telco: is_synthetic defaults false.
	if _, err := db.Admin.Exec(ctx, `INSERT INTO telcos (telco_id, name, country) VALUES ('MTN_NG', 'MTN Nigeria', 'NG')`); err != nil {
		t.Fatal(err)
	}
	base := func(url, authSuffix string) string {
		return `{"fulfilment_url":"` + url + `","request_timeout_ms":2000,"retry_budget":0,"circuit_error_threshold_pct":50,"circuit_min_requests":20,"circuit_cooldown_seconds":30,"max_weekly_recharge_minor":100000000` + authSuffix + `}`
	}

	// A real telco cannot arm over http, nor with auth none / no auth, nor an http
	// oauth token endpoint — each rejected at approval by the scope-aware validator.
	mustReject(t, svc, "telco.adapter", "telco:MTN_NG", "real telco http fulfilment",
		base("http://mno.mtn/api", `,"auth":{"scheme":"apikey","header":"X-Api-Key","secret_env":"E"}`))
	mustReject(t, svc, "telco.adapter", "telco:MTN_NG", "real telco auth none",
		base("https://mno.mtn/api", `,"auth":{"scheme":"none"}`))
	mustReject(t, svc, "telco.adapter", "telco:MTN_NG", "real telco no auth block",
		base("https://mno.mtn/api", ``))
	mustReject(t, svc, "telco.adapter", "telco:MTN_NG", "real telco oauth http token_url",
		base("https://mno.mtn/api", `,"auth":{"scheme":"oauth2","token_url":"http://mno.mtn/token","client_id":"a","client_secret_env":"E"}`))

	// A real telco WITH https + a real auth scheme activates cleanly (Approve AND Activate).
	c, err := svc.CreateDraft(ctx, "telco.adapter", "telco:MTN_NG", "alice", "real telco secure",
		[]byte(base("https://mno.mtn/api", `,"auth":{"scheme":"apikey","header":"X-Api-Key","secret_env":"TCP_MTN_KEY"}`)))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Submit(ctx, c.ConfigVersionID, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Approve(ctx, c.ConfigVersionID, "bob"); err != nil {
		t.Fatalf("a real telco with https + apikey must be accepted: %v", err)
	}
	if err := svc.Activate(ctx, c.ConfigVersionID, "bob", time.Now().UTC()); err != nil {
		t.Fatalf("activation must also pass the scope-aware validator: %v", err)
	}

	// Control: the SYNTHETIC simulator (SIM_NG, is_synthetic=true) may still use http/none.
	sc, err := svc.CreateDraft(ctx, "telco.adapter", "telco:SIM_NG", "alice", "sim http none",
		[]byte(base("http://127.0.0.1:9/api", `,"auth":{"scheme":"none"}`)))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Submit(ctx, sc.ConfigVersionID, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Approve(ctx, sc.ConfigVersionID, "bob"); err != nil {
		t.Fatalf("the synthetic simulator may use http/none: %v", err)
	}
}
