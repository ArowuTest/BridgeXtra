package handler_test

// M4a pack (G4 groundwork): server-side authorization proven table-driven —
// every portal route × every role × no-session, over real HTTP with the
// real session store. Plus session hygiene: cookie flags, CSRF on mutation,
// logout revocation, maker-checker THROUGH the portal.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/handler"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
)

type portalFixture struct {
	db  *testutil.DB
	srv *httptest.Server
}

// keys per role, provisioned through the bootstrap path.
var roleKeys = map[string]string{
	"ADMIN":   "portal-key-admin-000001",
	"RISK":    "portal-key-risk-0000001",
	"FINANCE": "portal-key-fin-00000001",
	"OPS":     "portal-key-ops-00000001",
	"SUPPORT": "portal-key-sup-00000001",
}

func newPortalFixture(t *testing.T, suffix string) *portalFixture {
	t.Helper()
	db := testutil.MustSetup(t, suffix)
	admins := &repo.Admins{Pool: db.Admin} // bootstrap provisioning right
	ctx := context.Background()
	i := 0
	for role, key := range roleKeys {
		i++
		if err := admins.CreateWithRole(ctx, fmt.Sprintf("adm_p%d", i), strings.ToLower(role)+"_actor", key, role); err != nil {
			t.Fatal(err)
		}
	}
	// A second ADMIN for the maker-checker journey.
	if err := admins.CreateWithRole(ctx, "adm_p9", "admin_actor_2", "portal-key-admin-000002", "ADMIN"); err != nil {
		t.Fatal(err)
	}

	p := &handler.Portal{
		Admins:   &repo.Admins{Pool: db.App},
		Sessions: &repo.PortalSessions{Pool: db.App},
		Config:   configsvc.New(db.Worker),
		Log:      slog.Default(),
		Secure:   false, // httptest is plain http
	}
	mux := http.NewServeMux()
	p.Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &portalFixture{db: db, srv: srv}
}

type session struct {
	cookie *http.Cookie
	csrf   string
	actor  string
}

func (f *portalFixture) login(t *testing.T, key string) session {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"api_key": key})
	resp, err := http.Post(f.srv.URL+"/v1/portal/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	var lr struct {
		Actor     string `json:"actor"`
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&lr); err != nil {
		t.Fatal(err)
	}
	for _, c := range resp.Cookies() {
		if c.Name == "bx_portal_session" {
			if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
				t.Fatalf("session cookie must be httpOnly SameSite=Strict: %+v", c)
			}
			return session{cookie: c, csrf: lr.CSRFToken, actor: lr.Actor}
		}
	}
	t.Fatal("no session cookie set")
	return session{}
}

func (f *portalFixture) call(t *testing.T, s *session, method, path, body string) int {
	t.Helper()
	var rdr *bytes.Reader
	if body == "" {
		rdr = bytes.NewReader(nil)
	} else {
		rdr = bytes.NewReader([]byte(body))
	}
	req, err := http.NewRequest(method, f.srv.URL+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if s != nil {
		req.AddCookie(s.cookie)
		req.Header.Set("X-CSRF-Token", s.csrf)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// The G4 groundwork matrix: every route × every role × no session. 401
// without a session; 403 for a role outside the allowlist; never a 200 the
// map does not grant.
func TestM4A_RBACMatrix_DenyByDefault(t *testing.T) {
	f := newPortalFixture(t, "portal_rbac")

	routes := []struct {
		method, path string
		allowed      map[string]bool
	}{
		{"GET", "/v1/portal/me", map[string]bool{"ADMIN": true, "RISK": true, "FINANCE": true, "OPS": true, "SUPPORT": true}},
		{"GET", "/v1/portal/config/active?domain=product.airtime&scope=programme:prg_sim_airtime01",
			map[string]bool{"ADMIN": true, "RISK": true, "FINANCE": true}},
		{"POST", "/v1/portal/config/drafts", map[string]bool{"ADMIN": true}},
		{"POST", "/v1/portal/config/cfg_none/submit", map[string]bool{"ADMIN": true}},
		{"POST", "/v1/portal/config/cfg_none/approve", map[string]bool{"ADMIN": true}},
		{"POST", "/v1/portal/config/cfg_none/activate", map[string]bool{"ADMIN": true}},
	}

	// No session: everything is 401.
	for _, rt := range routes {
		if code := f.call(t, nil, rt.method, rt.path, `{}`); code != http.StatusUnauthorized {
			t.Errorf("%s %s without session: want 401, got %d", rt.method, rt.path, code)
		}
	}

	// Every role against every route.
	for role, key := range roleKeys {
		s := f.login(t, key)
		for _, rt := range routes {
			code := f.call(t, &s, rt.method, rt.path, `{"domain":"platform.outbox","scope":"global","reason":"t","content":{}}`)
			if rt.allowed[role] {
				if code == http.StatusUnauthorized || code == http.StatusForbidden {
					t.Errorf("role %s on %s %s: allowed by map but got %d", role, rt.method, rt.path, code)
				}
			} else if code != http.StatusForbidden {
				t.Errorf("role %s on %s %s: want 403 (deny-by-default), got %d", role, rt.method, rt.path, code)
			}
		}
	}
}

func TestM4A_CSRF_RequiredOnMutation(t *testing.T) {
	f := newPortalFixture(t, "portal_csrf")
	s := f.login(t, roleKeys["ADMIN"])

	// Mutating call WITHOUT the CSRF header: 403 even with a live session.
	req, _ := http.NewRequest("POST", f.srv.URL+"/v1/portal/config/drafts",
		bytes.NewReader([]byte(`{"domain":"platform.outbox","scope":"global","reason":"t","content":{}}`)))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(s.cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("mutation without CSRF token must be 403, got %d", resp.StatusCode)
	}
	// GET without CSRF is fine (safe method).
	if code := f.call(t, &session{cookie: s.cookie}, "GET", "/v1/portal/me", ""); code != http.StatusOK {
		t.Fatalf("GET with session but no CSRF must pass: %d", code)
	}
}

func TestM4A_Logout_RevokesServerSide(t *testing.T) {
	f := newPortalFixture(t, "portal_logout")
	s := f.login(t, roleKeys["SUPPORT"])

	if code := f.call(t, &s, "GET", "/v1/portal/me", ""); code != http.StatusOK {
		t.Fatalf("live session must serve /me: %d", code)
	}
	if code := f.call(t, &s, "POST", "/v1/portal/logout", ""); code != http.StatusOK {
		t.Fatalf("logout: %d", code)
	}
	// The old cookie is dead SERVER-SIDE, not just cleared client-side.
	if code := f.call(t, &s, "GET", "/v1/portal/me", ""); code != http.StatusUnauthorized {
		t.Fatalf("revoked session must be 401, got %d", code)
	}
}

// Maker-checker THROUGH the portal: the approver's SESSION identity is the
// actor — the same admin cannot approve their own draft, a second admin can.
func TestM4A_MakerChecker_ThroughPortalSessions(t *testing.T) {
	f := newPortalFixture(t, "portal_mc")
	maker := f.login(t, roleKeys["ADMIN"])
	approver := f.login(t, "portal-key-admin-000002")

	// Maker drafts + submits.
	body := `{"domain":"platform.outbox","scope":"global","reason":"portal journey",
		"content":{"claim_batch_size":100,"max_attempts":5,"retry_backoff_seconds":10}}`
	req, _ := http.NewRequest("POST", f.srv.URL+"/v1/portal/config/drafts", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(maker.cookie)
	req.Header.Set("X-CSRF-Token", maker.csrf)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var cv struct {
		ConfigVersionID string `json:"config_version_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&cv); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated || cv.ConfigVersionID == "" {
		t.Fatalf("draft: %d %+v", resp.StatusCode, cv)
	}
	if code := f.call(t, &maker, "POST", "/v1/portal/config/"+cv.ConfigVersionID+"/submit", ""); code != http.StatusOK {
		t.Fatalf("submit: %d", code)
	}
	// Maker tries to approve their OWN draft: 409 maker-checker.
	if code := f.call(t, &maker, "POST", "/v1/portal/config/"+cv.ConfigVersionID+"/approve", ""); code != http.StatusConflict {
		t.Fatalf("self-approval through the portal must be 409, got %d", code)
	}
	// A different admin approves + activates.
	if code := f.call(t, &approver, "POST", "/v1/portal/config/"+cv.ConfigVersionID+"/approve", ""); code != http.StatusOK {
		t.Fatalf("distinct approve: %d", code)
	}
	if code := f.call(t, &approver, "POST", "/v1/portal/config/"+cv.ConfigVersionID+"/activate", ""); code != http.StatusOK {
		t.Fatalf("activate: %d", code)
	}
}
