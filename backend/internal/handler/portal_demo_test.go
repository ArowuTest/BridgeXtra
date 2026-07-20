package handler_test

// M4e-3 pack: the fault demo end-to-end through the portal — the REAL
// pipeline (simulator feature file -> ingest -> scoring -> origination ->
// resolver), no stubs. Proves the design-gate demo promise: hard_fail shows
// a refused credit with the reservation released; timeout_unknown shows
// EDG-005 (the credit HAPPENED, the platform didn't hear, the resolver
// chases the truth to CONFIRMED); the artifact chain (advance, attempts,
// journals, notifications) reads live from the real tables by correlation.
// Also proves the guards: allowlist floor, C3 disabled floor, and C6 pool
// rotation past the one-active constraint.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/ledger"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/mno"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/featureingest"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/fulfilmentresolver"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/origination"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/scoringrun"
	"github.com/ArowuTest/telco-credit-platform/simulator/sim"
)

// demoFixture extends the portal fixture with the full M2 pipeline: a live
// simulator, an activated telco.adapter config pointing at it (short request
// timeout + a long sim hold so TIMEOUT tokens genuinely strand the caller),
// and the demo token pool ingested + scored.
func newDemoFixture(t *testing.T, suffix string) (*portalFixture, *fulfilmentresolver.Service) {
	t.Helper()
	f := newPortalFixture(t, suffix)
	ctx := context.Background()

	simulator := sim.New(slog.Default(), "demo-test", 5*time.Second)
	srv := httptest.NewServer(simulator.Handler())
	t.Cleanup(srv.Close)

	svcCfg := configsvc.New(f.db.Worker)
	content := fmt.Sprintf(`{"fulfilment_url":%q,"request_timeout_ms":500,"retry_budget":0,"circuit_error_threshold_pct":50,"circuit_min_requests":20,"circuit_cooldown_seconds":30,"max_weekly_recharge_minor":100000000}`, srv.URL)
	c, err := svcCfg.CreateDraft(ctx, "telco.adapter", "telco:SIM_NG", "alice", "demo sim", []byte(content))
	if err != nil {
		t.Fatal(err)
	}
	if err := svcCfg.Submit(ctx, c.ConfigVersionID, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := svcCfg.Approve(ctx, c.ConfigVersionID, "bob"); err != nil {
		t.Fatal(err)
	}
	if err := svcCfg.Activate(ctx, c.ConfigVersionID, "bob", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	appCfg := configsvc.New(f.db.App)
	file, err := featureingest.New(f.db.App, appCfg, slog.Default()).Run(ctx, "SIM_NG")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scoringrun.New(f.db.App, appCfg, slog.Default()).Run(ctx, "SIM_NG", "prg_sim_airtime01", file.FeatureFileID); err != nil {
		t.Fatal(err)
	}

	orig := origination.New(f.db.App, appCfg, ledger.New(appCfg), mno.NewHTTPAdapter(appCfg), slog.Default())
	resolver := fulfilmentresolver.New(f.db.App, appCfg, mno.NewHTTPAdapter(appCfg), orig, slog.Default())
	return f, resolver
}

func (f *portalFixture) demoRun(t *testing.T, s *session, scenario string) (code int, runID, token string) {
	t.Helper()
	c, body := f.callBody(t, s, "POST", "/v1/portal/ops/demo/run",
		fmt.Sprintf(`{"telco_id":"SIM_NG","scenario":%q}`, scenario))
	if c != http.StatusOK {
		return c, "", ""
	}
	var r struct {
		RunID       string `json:"run_id"`
		MSISDNToken string `json:"msisdn_token"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatal(err)
	}
	return c, r.RunID, r.MSISDNToken
}

func (f *portalFixture) demoChain(t *testing.T, s *session, runID string) map[string]any {
	t.Helper()
	code, body := f.callBody(t, s, "GET", "/v1/portal/ops/demo/runs/"+runID, "")
	if code != http.StatusOK {
		t.Fatalf("chain %s: %d %s", runID, code, body)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func chainStr(t *testing.T, chain map[string]any, path ...string) string {
	t.Helper()
	cur := any(chain)
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("chain path %v: not an object", path)
		}
		cur = m[p]
	}
	s, _ := cur.(string)
	return s
}

func TestM4E3_FaultDemo_EndToEnd(t *testing.T) {
	f, resolver := newDemoFixture(t, "m4e3_e2e")
	opsSess := f.login(t, roleKeys["OPS"])
	ctx := context.Background()

	// The governed catalogue lists for the operator's telco.
	code, body := f.callBody(t, &opsSess, "GET", "/v1/portal/ops/demo/scenarios?telco_id=SIM_NG", "")
	if code != http.StatusOK || !strings.Contains(string(body), "timeout_unknown") {
		t.Fatalf("scenarios: %d %s", code, body)
	}

	// hard_fail: the telco refuses; the attempt lands FAILED and the advance
	// FULFILMENT_FAILED — no exposure survives.
	code, runID, token := f.demoRun(t, &opsSess, "hard_fail")
	if code != http.StatusOK {
		t.Fatalf("hard_fail run: %d", code)
	}
	if !strings.Contains(token, "FAIL") {
		t.Fatalf("hard_fail must draw a FAIL-shaped token, got %s", token)
	}
	chain := f.demoChain(t, &opsSess, runID)
	if st := chainStr(t, chain, "advance", "state"); st != "FULFILMENT_FAILED" {
		t.Fatalf("hard_fail advance state = %s", st)
	}

	// timeout_unknown: EDG-005. The sim records the credit but holds the
	// response past the adapter timeout — the attempt is ambiguous, never
	// guessed FAILED.
	code, run1, tok1 := f.demoRun(t, &opsSess, "timeout_unknown")
	if code != http.StatusOK {
		t.Fatalf("timeout run: %d", code)
	}
	chain = f.demoChain(t, &opsSess, run1)
	if st := chainStr(t, chain, "advance", "state"); st != "FULFILMENT_UNKNOWN" {
		t.Fatalf("timeout advance state = %s (must be UNKNOWN, not guessed)", st)
	}

	// C6: a second run rotates to a DIFFERENT pool token — the first holds
	// an open advance.
	code, _, tok2 := f.demoRun(t, &opsSess, "timeout_unknown")
	if code != http.StatusOK {
		t.Fatalf("second timeout run: %d", code)
	}
	if tok2 == tok1 {
		t.Fatalf("C6: pool must rotate, both runs drew %s", tok1)
	}
	// Third consumes the pool; the fourth is an HONEST 409, not a wedge.
	if code, _, _ = f.demoRun(t, &opsSess, "timeout_unknown"); code != http.StatusOK {
		t.Fatalf("third timeout run: %d", code)
	}
	if code, _, _ = f.demoRun(t, &opsSess, "timeout_unknown"); code != http.StatusConflict {
		t.Fatalf("exhausted pool must 409 DEMO_POOL_BUSY, got %d", code)
	}

	// The M4e-1 queue sees the ambiguity; enquire-now + the resolver land
	// the truth: the credit HAD happened -> CONFIRMED, advance ACTIVE.
	advID := chainStr(t, chain, "run", "advance_id")
	var attemptID string
	if err := f.db.Admin.QueryRow(ctx,
		`SELECT attempt_id FROM fulfilment_attempts WHERE advance_id=$1`, advID).Scan(&attemptID); err != nil {
		t.Fatal(err)
	}
	if code := f.call(t, &opsSess, "POST", "/v1/portal/ops/fulfilments/"+attemptID+"/enquire-now", ""); code != http.StatusOK {
		t.Fatalf("enquire-now on demo attempt: %d", code)
	}
	if _, err := resolver.RunOnce(context.Background(), "SIM_NG", 10); err != nil {
		t.Fatal(err)
	}
	chain = f.demoChain(t, &opsSess, run1)
	if st := chainStr(t, chain, "advance", "state"); st != "ACTIVE" {
		t.Fatalf("post-resolver advance state = %s (EDG-005: the credit happened)", st)
	}

	// happy_path: straight through, with the money trail in the chain.
	code, runID, _ = f.demoRun(t, &opsSess, "happy_path")
	if code != http.StatusOK {
		t.Fatalf("happy run: %d", code)
	}
	chain = f.demoChain(t, &opsSess, runID)
	if st := chainStr(t, chain, "advance", "state"); st != "ACTIVE" {
		t.Fatalf("happy advance state = %s", st)
	}
	if js, ok := chain["journals"].([]any); !ok || len(js) == 0 {
		t.Fatal("the chain must carry the real ledger journals (BC-6 by correlation)")
	}

	// Runs list for oversight roles.
	finSess := f.login(t, roleKeys["FINANCE"])
	code, body = f.callBody(t, &finSess, "GET", "/v1/portal/ops/demo/runs", "")
	if code != http.StatusOK || !strings.Contains(string(body), runID) {
		t.Fatalf("finance runs list: %d", code)
	}
}

func TestM4E3_DemoGuards(t *testing.T) {
	f, _ := newDemoFixture(t, "m4e3_guards")
	ctx := context.Background()

	// A telco outside the allowlist is refused — the structural sim-only
	// guard (the config names SIM_NG only).
	if err := (&repo.Admins{Pool: f.db.Admin}).CreateWithRole(ctx, "adm_demo_o", "ops_other_demo", "portal-key-ops-dem-o-1", "OPS", "telco:REAL_NG"); err != nil {
		t.Fatal(err)
	}
	otherSess := f.login(t, "portal-key-ops-dem-o-1")
	code, body := f.callBody(t, &otherSess, "POST", "/v1/portal/ops/demo/run", `{"scenario":"happy_path"}`)
	if code != http.StatusForbidden || !strings.Contains(string(body), "DEMO_UNAVAILABLE") {
		t.Fatalf("non-allowlisted telco must 403 DEMO_UNAVAILABLE, got %d %s", code, body)
	}

	opsSess := f.login(t, roleKeys["OPS"])
	// Unknown scenario refused.
	if code, _ := f.callBody(t, &opsSess, "POST", "/v1/portal/ops/demo/run",
		`{"telco_id":"SIM_NG","scenario":"chaos_monkey"}`); code != http.StatusBadRequest {
		t.Fatalf("unknown scenario must 400, got %d", code)
	}

	// C3 floor FIRES: config gone -> every run and even the catalogue read
	// refuse. Absent config is never 'demo on'.
	if _, err := f.db.Admin.Exec(ctx,
		`DELETE FROM config_versions WHERE domain='ops.fault_demo'`); err != nil {
		t.Fatal(err)
	}
	if code, _ := f.callBody(t, &opsSess, "POST", "/v1/portal/ops/demo/run",
		`{"telco_id":"SIM_NG","scenario":"happy_path"}`); code != http.StatusForbidden {
		t.Fatalf("unconfigured demo must refuse, got %d", code)
	}
	if code, _ := f.callBody(t, &opsSess, "GET", "/v1/portal/ops/demo/scenarios?telco_id=SIM_NG", ""); code != http.StatusForbidden {
		t.Fatalf("unconfigured catalogue must refuse, got %d", code)
	}
}
