package handler_test

// Admin config API over real HTTP against a real DB: the owner's "admin
// manages configuration" directive proven end-to-end — full lifecycle,
// maker≠checker at the API, validator rejection with stable error codes
// (BC-7), and unauthenticated access denied.

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/handler"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
)

func newAdminServer(t *testing.T, suffix string) (*httptest.Server, *testutil.DB) {
	t.Helper()
	db := testutil.MustSetup(t, suffix)
	// Provisioning admin credentials is a bootstrap/platform-owner operation
	// (admin pool); the runtime API resolves them with the least-privileged
	// app role (SELECT only) — mirroring production wiring.
	bootstrap := &repo.Admins{Pool: db.Admin}
	if err := bootstrap.Create(context.Background(), "adm_alice", "alice", "alice-key"); err != nil {
		t.Fatal(err)
	}
	if err := bootstrap.Create(context.Background(), "adm_bob", "bob", "bob-key"); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	auth := &handler.AdminAuth{Admins: &repo.Admins{Pool: db.App}, Log: slog.Default()}
	(&handler.ConfigAdmin{Svc: configsvc.New(db.Worker), Log: slog.Default()}).Mount(mux, auth)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, db
}

func adminDo(t *testing.T, srv *httptest.Server, key, method, path string, body any) (*http.Response, []byte) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequest(method, srv.URL+path, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if key != "" {
		req.Header.Set("X-Admin-Key", key)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out bytes.Buffer
	if _, err := out.ReadFrom(resp.Body); err != nil {
		t.Fatal(err)
	}
	return resp, out.Bytes()
}

func TestAdminConfigAPI_FullLifecycle_MakerChecker_Validation(t *testing.T) {
	srv, _ := newAdminServer(t, "cfgadmin_api")

	// Unauthenticated: denied.
	if resp, _ := adminDo(t, srv, "", http.MethodGet, "/v1/admin/config/active?domain=platform.outbox&scope=global", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated must be 401, got %d", resp.StatusCode)
	}

	// Alice drafts a product change through the API — no SQL, no deploy.
	draft := map[string]any{
		"domain": "product.airtime",
		"scope":  "programme:prg_sim_airtime01",
		"reason": "adjust ladder via admin API",
		"content": map[string]any{
			"currency":             "NGN",
			"denominations_minor":  []int64{5000, 10000, 25000},
			"fee_bps":              1200,
			"fee_model":            "DEDUCTED_UPFRONT",
			"offer_expiry_minutes": 720,
		},
	}
	resp, body := adminDo(t, srv, "alice-key", http.MethodPost, "/v1/admin/config/drafts", draft)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("draft: %d %s", resp.StatusCode, body)
	}
	var created struct {
		ConfigVersionID string `json:"config_version_id"`
		VersionNo       int    `json:"version_no"`
		CreatedBy       string `json:"created_by"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatal(err)
	}
	if created.CreatedBy != "alice" || created.VersionNo != 2 {
		t.Fatalf("draft attribution wrong: %+v", created)
	}
	id := created.ConfigVersionID

	// Submit (alice) → approve by ALICE must be 409 CONFIG_MAKER_CHECKER.
	if resp, body := adminDo(t, srv, "alice-key", http.MethodPost, "/v1/admin/config/"+id+"/submit", nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("submit: %d %s", resp.StatusCode, body)
	}
	resp, body = adminDo(t, srv, "alice-key", http.MethodPost, "/v1/admin/config/"+id+"/approve", nil)
	if resp.StatusCode != http.StatusConflict || !bytes.Contains(body, []byte("CONFIG_MAKER_CHECKER")) {
		t.Fatalf("maker approval must be 409 CONFIG_MAKER_CHECKER, got %d %s", resp.StatusCode, body)
	}

	// Bob approves, bob activates.
	if resp, body := adminDo(t, srv, "bob-key", http.MethodPost, "/v1/admin/config/"+id+"/approve", nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("approve: %d %s", resp.StatusCode, body)
	}
	if resp, body := adminDo(t, srv, "bob-key", http.MethodPost, "/v1/admin/config/"+id+"/activate", nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("activate: %d %s", resp.StatusCode, body)
	}

	// Active lookup returns the NEW version with the new content.
	resp, body = adminDo(t, srv, "bob-key", http.MethodGet,
		"/v1/admin/config/active?domain=product.airtime&scope=programme:prg_sim_airtime01", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("active: %d %s", resp.StatusCode, body)
	}
	var active struct {
		ConfigVersionID string `json:"config_version_id"`
		Content         struct {
			FeeBps int64 `json:"fee_bps"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &active); err != nil {
		t.Fatal(err)
	}
	if active.ConfigVersionID != id || active.Content.FeeBps != 1200 {
		t.Fatalf("active must be the new version (fee 1200bps), got %s", body)
	}
}

func TestAdminConfigAPI_ValidatorRejectsBadContent_StableErrorCode(t *testing.T) {
	srv, _ := newAdminServer(t, "cfgadmin_validate")

	// Descending ladder + fee > 100% — must be rejected at approval with
	// CONFIG_VALIDATION_FAILED (armed-but-dead prevention: bad config can
	// never become ACTIVE).
	for _, bad := range []map[string]any{
		{"currency": "NGN", "denominations_minor": []int64{10000, 5000}, "fee_bps": 1000, "fee_model": "DEDUCTED_UPFRONT", "offer_expiry_minutes": 60},
		{"currency": "NGN", "denominations_minor": []int64{5000}, "fee_bps": 10001, "fee_model": "DEDUCTED_UPFRONT", "offer_expiry_minutes": 60},
		{"currency": "NGN", "denominations_minor": []int64{5000}, "fee_bps": 1000, "fee_model": "MYSTERY_MODEL", "offer_expiry_minutes": 60},
	} {
		draft := map[string]any{
			"domain": "product.airtime", "scope": "programme:prg_sim_airtime01",
			"reason": "bad content", "content": bad,
		}
		resp, body := adminDo(t, srv, "alice-key", http.MethodPost, "/v1/admin/config/drafts", draft)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("draft: %d %s", resp.StatusCode, body)
		}
		var created struct {
			ConfigVersionID string `json:"config_version_id"`
		}
		if err := json.Unmarshal(body, &created); err != nil {
			t.Fatal(err)
		}
		if resp, _ := adminDo(t, srv, "alice-key", http.MethodPost, "/v1/admin/config/"+created.ConfigVersionID+"/submit", nil); resp.StatusCode != http.StatusNoContent {
			t.Fatal("submit failed")
		}
		resp, body = adminDo(t, srv, "bob-key", http.MethodPost, "/v1/admin/config/"+created.ConfigVersionID+"/approve", nil)
		if resp.StatusCode != http.StatusUnprocessableEntity || !bytes.Contains(body, []byte("CONFIG_VALIDATION_FAILED")) {
			t.Fatalf("bad content %v must be 422 CONFIG_VALIDATION_FAILED, got %d %s", bad, resp.StatusCode, body)
		}
	}
}

func TestAdminConfigAPI_M1SeededDomainsResolve(t *testing.T) {
	// Owner rule: every M1 knob exists as an admin-visible config record from
	// the seed — the saga never reads a constant.
	srv, _ := newAdminServer(t, "cfgadmin_seeds")
	for _, q := range []string{
		"domain=product.airtime&scope=programme:prg_sim_airtime01",
		"domain=advance.reservation&scope=global",
		"domain=advance.fulfilment&scope=telco:SIM_NG",
		"domain=recovery.allocation&scope=programme:prg_sim_airtime01",
		"domain=telco.adapter&scope=telco:SIM_NG",
		"domain=recon.tolerance&scope=programme:prg_sim_airtime01",
	} {
		resp, body := adminDo(t, srv, "alice-key", http.MethodGet, "/v1/admin/config/active?"+q, nil)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("seeded domain missing via admin API (%s): %d %s", q, resp.StatusCode, body)
		}
	}
}
