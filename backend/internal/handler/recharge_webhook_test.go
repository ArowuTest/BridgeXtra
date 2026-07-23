package handler_test

// Phase 1 S2.2b — the recharge webhook money-auth spine. These prove the six
// things that matter most: the kill-switch actually gates the money path, the
// structural recon-live gate, telco-from-credential (never path/body),
// dummy-HMAC uniform 401, blast-radius clamps -> HELD (never ingested), and a
// byte-identical retry -> idempotent 200 (never 409). Plus the fail-closed
// matrix (stale, bad sig, unknown key, disabled feed).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/handler"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/ledger"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform/ratelimit"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/rechargewebhook"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/recovery"
)

const (
	whKeyID     = "kid-1"
	whSecretEnv = "TCP_TEST_WH_SECRET"
	whSecret    = "s2-hmac-shared-secret"
)

type whFixture struct {
	srv *httptest.Server
	db  *testutil.DB
}

func newWebhookFixture(t *testing.T, suffix string, telcoEnabled bool, perEventMax int64, reconLive bool) *whFixture {
	t.Helper()
	t.Setenv(whSecretEnv, whSecret)
	db := testutil.MustSetup(t, suffix)
	ctx := context.Background()

	cfgW := configsvc.New(db.Worker)
	activateRechargeFeed(t, cfgW, "global", true, 50_000_000)
	activateRechargeFeed(t, cfgW, "telco:SIM_NG", telcoEnabled, perEventMax)

	if err := (&repo.WebhookCredentials{Pool: db.Admin}).Create(ctx, whKeyID, "SIM_NG", whSecretEnv, "test"); err != nil {
		t.Fatal(err)
	}
	if reconLive {
		if err := (&repo.ReconArming{Pool: db.Admin}).SetLive(ctx, "SIM_NG", repo.ReconLayerRecovery); err != nil {
			t.Fatal(err)
		}
	}

	appCfg := configsvc.New(db.App)
	rec := recovery.New(db.App, appCfg, ledger.New(appCfg), slog.Default())
	lim := ratelimit.New(map[string]ratelimit.Limit{
		"channel":    {RatePerMinute: 1e9, Burst: 1e9},
		"channel_ip": {RatePerMinute: 1e9, Burst: 1e9},
	})
	h := &handler.RechargeWebhook{
		Recovery: rec, Config: appCfg,
		Creds: &repo.WebhookCredentials{Pool: db.App}, Recon: &repo.ReconArming{Pool: db.App},
		Pool: db.App, Auth: rechargewebhook.NewHMACSHA256Adapter(), Mapper: rechargewebhook.NewJSONMapper(),
		Limiter: lim, Log: slog.Default(),
	}
	mux := http.NewServeMux()
	h.Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &whFixture{srv: srv, db: db}
}

func activateRechargeFeed(t *testing.T, cfgW *configsvc.Service, scope string, enabled bool, perEventMax int64) {
	t.Helper()
	ctx := context.Background()
	content := fmt.Sprintf(`{"enabled":%v,"transport":"webhook_push","auth":"hmac_sha256","key_id_header":"X-Bx-Key-Id","signature_header":"X-Bx-Signature","timestamp_header":"X-Bx-Timestamp","replay_window_seconds":120,"future_skew_seconds":60,"max_body_bytes":65536,"expected_currency":"NGN","per_event_amount_max_minor":%d,"per_telco_daily_ceiling_minor":50000000000}`, enabled, perEventMax)
	c, err := cfgW.CreateDraft(ctx, "telco.recharge_feed", scope, "alice", "arm", []byte(content))
	if err != nil {
		t.Fatalf("draft %s: %v", scope, err)
	}
	if err := cfgW.Submit(ctx, c.ConfigVersionID, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := cfgW.Approve(ctx, c.ConfigVersionID, "bob"); err != nil {
		t.Fatal(err)
	}
	if err := cfgW.Activate(ctx, c.ConfigVersionID, "bob", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
}

// post signs and sends a recharge webhook with the given path telco / key / secret.
func (f *whFixture) post(t *testing.T, pathTelco, keyID, secret, tsStr, body string) *http.Response {
	t.Helper()
	sig := rechargewebhook.Sign(rechargewebhook.NewHMACSHA256Adapter(), []byte(secret), keyID, tsStr, []byte(body))
	req, err := http.NewRequest(http.MethodPost, f.srv.URL+"/v1/telcos/"+pathTelco+"/recharge-webhook", bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Bx-Key-Id", keyID)
	req.Header.Set("X-Bx-Timestamp", tsStr)
	req.Header.Set("X-Bx-Signature", sig)
	resp, err := f.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func nowTS() string { return fmt.Sprintf("%d", time.Now().UTC().Unix()) }

func rechargeBody(eventID string, amountMinor int64) string {
	return fmt.Sprintf(`{"event_id":%q,"msisdn_token":"tok_sim_0001","amount_minor":%d,"currency":"NGN","occurred_at":%q}`,
		eventID, amountMinor, time.Now().UTC().Format(time.RFC3339))
}

func (f *whFixture) recoveryEventCount(t *testing.T, src string) int {
	t.Helper()
	var n int
	if err := f.db.Admin.QueryRow(context.Background(),
		`SELECT count(*) FROM recovery_events WHERE source_event_id=$1`, src).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestS22b_HappyPath_Ingests(t *testing.T) {
	f := newWebhookFixture(t, "wh_happy", true, 50_000_000, true)
	resp := f.post(t, "SIM_NG", whKeyID, whSecret, nowTS(), rechargeBody("e1", 5000))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("valid signed recharge must ingest 200, got %d", resp.StatusCode)
	}
	if n := f.recoveryEventCount(t, "wh:e1"); n != 1 {
		t.Fatalf("exactly one recovery event must be written, got %d", n)
	}
}

func TestS22b_KillSwitch_GatesMoney(t *testing.T) {
	// Telco feed DISABLED — a valid signed request must be refused and NOTHING ingested.
	f := newWebhookFixture(t, "wh_killswitch", false, 50_000_000, true)
	resp := f.post(t, "SIM_NG", whKeyID, whSecret, nowTS(), rechargeBody("e1", 5000))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("disabled feed must 403, got %d", resp.StatusCode)
	}
	if n := f.recoveryEventCount(t, "wh:e1"); n != 0 {
		t.Fatalf("a disabled feed must ingest NOTHING, got %d recovery events", n)
	}
}

func TestS22b_StructuralReconGate(t *testing.T) {
	// RECOVERY recon layer NOT live — refuse, no ingest (no webhook money without recon).
	f := newWebhookFixture(t, "wh_recon", true, 50_000_000, false)
	resp := f.post(t, "SIM_NG", whKeyID, whSecret, nowTS(), rechargeBody("e1", 5000))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("recon-not-live must 403, got %d", resp.StatusCode)
	}
	if n := f.recoveryEventCount(t, "wh:e1"); n != 0 {
		t.Fatalf("no ingest without a live recovery recon layer, got %d", n)
	}
}

func TestS22b_TenantFromCredential_PathMismatch(t *testing.T) {
	f := newWebhookFixture(t, "wh_tenant", true, 50_000_000, true)
	// kid-1 belongs to SIM_NG; a path claiming another telco must be refused.
	resp := f.post(t, "OTHER_NG", whKeyID, whSecret, nowTS(), rechargeBody("e1", 5000))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("path/credential telco mismatch must 403 TENANT_CONTEXT_MISMATCH, got %d", resp.StatusCode)
	}
}

func TestS22b_DummyHMAC_UniformUnauthorized(t *testing.T) {
	f := newWebhookFixture(t, "wh_401", true, 50_000_000, true)
	// Unknown key_id.
	if r := f.post(t, "SIM_NG", "kid-unknown", whSecret, nowTS(), rechargeBody("e1", 5000)); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unknown key_id must 401, got %d", r.StatusCode)
	}
	// Valid key, WRONG secret -> bad MAC.
	if r := f.post(t, "SIM_NG", whKeyID, "wrong-secret", nowTS(), rechargeBody("e2", 5000)); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad signature must 401, got %d", r.StatusCode)
	}
}

func TestS22b_Stale_Rejected(t *testing.T) {
	f := newWebhookFixture(t, "wh_stale", true, 50_000_000, true)
	old := fmt.Sprintf("%d", time.Now().UTC().Add(-1000*time.Second).Unix()) // window is 120s
	resp := f.post(t, "SIM_NG", whKeyID, whSecret, old, rechargeBody("e1", 5000))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("stale timestamp must 401, got %d", resp.StatusCode)
	}
	if n := f.recoveryEventCount(t, "wh:e1"); n != 0 {
		t.Fatal("stale request must not ingest")
	}
}

func TestS22b_HELD_OverPerEventClamp(t *testing.T) {
	// Tiny per-event clamp; a larger recharge must be HELD, not ingested.
	f := newWebhookFixture(t, "wh_held", true, 100, true)
	resp := f.post(t, "SIM_NG", whKeyID, whSecret, nowTS(), rechargeBody("e1", 50000))
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("over-clamp recharge must be HELD (202), got %d", resp.StatusCode)
	}
	if n := f.recoveryEventCount(t, "wh:e1"); n != 0 {
		t.Fatalf("a HELD recharge must NOT be ingested, got %d recovery events", n)
	}
	var held int
	if err := f.db.Admin.QueryRow(context.Background(),
		`SELECT count(*) FROM held_recharge_events WHERE source_event_id='wh:e1' AND status='HELD'`).Scan(&held); err != nil {
		t.Fatal(err)
	}
	if held != 1 {
		t.Fatalf("the recharge must be parked in the HELD queue, got %d", held)
	}
}

func TestS22b_IdempotentRetry_200_Not409(t *testing.T) {
	f := newWebhookFixture(t, "wh_retry", true, 50_000_000, true)
	ts := nowTS()
	body := rechargeBody("e1", 5000)
	r1 := f.post(t, "SIM_NG", whKeyID, whSecret, ts, body)
	r2 := f.post(t, "SIM_NG", whKeyID, whSecret, ts, body) // byte-identical retry
	if r1.StatusCode != http.StatusOK || r2.StatusCode != http.StatusOK {
		t.Fatalf("a byte-identical retry must be idempotent 200/200, got %d/%d", r1.StatusCode, r2.StatusCode)
	}
	var out2 struct {
		Replayed bool `json:"replayed"`
	}
	_ = json.NewDecoder(r2.Body).Decode(&out2)
	if !out2.Replayed {
		t.Fatal("the retry must be recognised as a replay (replayed=true), never a 409")
	}
	if n := f.recoveryEventCount(t, "wh:e1"); n != 1 {
		t.Fatalf("a retry must not create a second recovery event, got %d", n)
	}
}
