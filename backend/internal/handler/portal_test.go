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
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/handler"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/treasury"
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
		if err := admins.CreateWithRole(ctx, fmt.Sprintf("adm_p%d", i), strings.ToLower(role)+"_actor", key, role, "*"); err != nil {
			t.Fatal(err)
		}
	}
	// A second ADMIN for the maker-checker journey.
	if err := admins.CreateWithRole(ctx, "adm_p9", "admin_actor_2", "portal-key-admin-000002", "ADMIN", "*"); err != nil {
		t.Fatal(err)
	}

	p := &handler.Portal{
		Admins:   &repo.Admins{Pool: db.App},
		Sessions: &repo.PortalSessions{Pool: db.App},
		Config:   configsvc.New(db.Worker),
		Treasury: treasury.New(db.App, configsvc.New(db.App), slog.Default()),
		ReadPool: db.Worker,
		Log:      slog.Default(),
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
			if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode {
				t.Fatalf("session cookie must be httpOnly Secure SameSite=Strict: %+v", c)
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

// callBody is call() but returns the response body too (for read assertions).
func (f *portalFixture) callBody(t *testing.T, s *session, method, path, body string) (int, []byte) {
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
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

// concreteRoute turns an RBAC map key ("METHOD /pattern", with {id}
// placeholders) into a request the matrix can actually issue.
func concreteRoute(key string) (method, path string) {
	parts := strings.SplitN(key, " ", 2)
	method, pattern := parts[0], parts[1]
	path = strings.ReplaceAll(pattern, "{id}", "cfg_seed_outbox_v1")
	switch pattern {
	case "/v1/portal/config/active":
		path += "?domain=platform.outbox&scope=global"
	case "/v1/portal/config/versions":
		path += "?domain=platform.outbox"
	}
	return method, path
}

// The G4 groundwork matrix, DRIVEN BY THE PRODUCTION MAP (M4A-F2): every
// route in routeRoles × every role × no session. 401 without a session; 403
// for a role outside the allowlist; never a 401/403 for an allowed role. The
// matrix iterates handler.RBACRoutes() so it can never drift from the policy
// the server actually enforces — and mountRBAC panics at boot if any mounted
// route lacks a map entry, so newPortalFixture would fail first.
func TestM4A_RBACMatrix_DenyByDefault(t *testing.T) {
	f := newPortalFixture(t, "portal_rbac")
	routes := handler.RBACRoutes()
	body := `{"domain":"platform.outbox","scope":"global","reason":"t","content":{}}`

	// No session: every route is 401.
	for key := range routes {
		method, path := concreteRoute(key)
		if code := f.call(t, nil, method, path, `{}`); code != http.StatusUnauthorized {
			t.Errorf("%s without session: want 401, got %d", key, code)
		}
	}

	// Every role against every route.
	for role, key := range roleKeys {
		s := f.login(t, key)
		for route, allowedRoles := range routes {
			method, path := concreteRoute(route)
			allowed := false
			for _, ar := range allowedRoles {
				if ar == role {
					allowed = true
				}
			}
			code := f.call(t, &s, method, path, body)
			if allowed {
				if code == http.StatusUnauthorized || code == http.StatusForbidden {
					t.Errorf("role %s on %s: allowed by map but got %d", role, route, code)
				}
			} else if code != http.StatusForbidden {
				t.Errorf("role %s on %s: want 403 (deny-by-default), got %d", role, route, code)
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

// seedTrip inserts an open guardrail trip and suspends its programme (the real
// state after a breach), returning the trip id. Uses the admin pool.
func seedTrip(t *testing.T, f *portalFixture, tripID, telcoID, programmeID string) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.db.Admin.Exec(ctx, `
		INSERT INTO guardrail_trips (trip_id, telco_id, programme_id, guardrail, measured_minor, limit_minor, currency)
		VALUES ($1,$2,$3,'DAILY_DISBURSED',600000000,500000000,'NGN')`, tripID, telcoID, programmeID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(ctx,
		`UPDATE programmes SET status='SUSPENDED' WHERE programme_id=$1`, programmeID); err != nil {
		t.Fatal(err)
	}
}

// M4c: the two-person guardrail re-arm through the portal — request (maker),
// self-approve refused (409), distinct approver re-arms (200), programme
// resumes, trip leaves the open list. The distinct-actor rule is the
// schema CHECK; this proves it holds when driven by real session identities.
func TestM4C_GuardrailRearm_TwoPersonThroughPortal(t *testing.T) {
	f := newPortalFixture(t, "portal_rearm")
	seedTrip(t, f, "trp_test_1", "SIM_NG", "prg_sim_airtime01")
	maker := f.login(t, roleKeys["RISK"])
	approver := f.login(t, roleKeys["ADMIN"]) // '*' scope, distinct actor

	// Maker sees the open trip.
	status, body := f.callBody(t, &maker, "GET", "/v1/portal/risk/trips", "")
	if status != http.StatusOK {
		t.Fatalf("list trips: %d", status)
	}
	var list struct {
		Trips []struct {
			TripID string `json:"trip_id"`
			State  string `json:"state"`
		} `json:"trips"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Trips) != 1 || list.Trips[0].TripID != "trp_test_1" {
		t.Fatalf("expected the seeded open trip, got %+v", list.Trips)
	}

	// Maker requests re-arm.
	if code := f.call(t, &maker, "POST", "/v1/portal/risk/trips/trp_test_1/request-rearm",
		`{"reason":"surge subsided, verified"}`); code != http.StatusOK {
		t.Fatalf("request-rearm: %d", code)
	}
	// Maker cannot approve their own request — two-person, 409.
	if code := f.call(t, &maker, "POST", "/v1/portal/risk/trips/trp_test_1/approve-rearm", ""); code != http.StatusConflict {
		t.Fatalf("self-approve must be 409, got %d", code)
	}
	// A distinct actor approves — re-armed.
	if code := f.call(t, &approver, "POST", "/v1/portal/risk/trips/trp_test_1/approve-rearm", ""); code != http.StatusOK {
		t.Fatalf("distinct approve-rearm: %d", code)
	}
	// The trip has left the open list and the programme resumed.
	_, body = f.callBody(t, &maker, "GET", "/v1/portal/risk/trips", "")
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Trips) != 0 {
		t.Fatalf("re-armed trip must leave the open list, got %+v", list.Trips)
	}
	var pstatus string
	if err := f.db.Admin.QueryRow(context.Background(),
		`SELECT status FROM programmes WHERE programme_id='prg_sim_airtime01'`).Scan(&pstatus); err != nil {
		t.Fatal(err)
	}
	if pstatus != "ACTIVE" {
		t.Fatalf("programme must resume to ACTIVE after re-arm, got %s", pstatus)
	}
}

// M4c scope: a programme-scoped operator for a DIFFERENT programme must not see
// or act on this trip — cross-scope reads/actions return a no-oracle 404.
func TestM4C_TripScope_CrossTenantHidden(t *testing.T) {
	f := newPortalFixture(t, "portal_trip_scope")
	seedTrip(t, f, "trp_test_2", "SIM_NG", "prg_sim_airtime01")
	admins := &repo.Admins{Pool: f.db.Admin}
	if err := admins.CreateWithRole(context.Background(), "adm_otherprog", "other_prog_risk",
		"portal-key-otherprog-1", "RISK", "programme:prg_other_9999"); err != nil {
		t.Fatal(err)
	}
	s := f.login(t, "portal-key-otherprog-1")

	// The trip is invisible in the scoped list.
	status, body := f.callBody(t, &s, "GET", "/v1/portal/risk/trips", "")
	if status != http.StatusOK {
		t.Fatalf("list: %d", status)
	}
	var list struct {
		Trips []struct{ TripID string } `json:"trips"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Trips) != 0 {
		t.Fatalf("out-of-scope trip must not appear, got %+v", list.Trips)
	}
	// And acting on it by id returns the same 404 as a nonexistent trip.
	if code := f.call(t, &s, "POST", "/v1/portal/risk/trips/trp_test_2/request-rearm",
		`{"reason":"x"}`); code != http.StatusNotFound {
		t.Fatalf("cross-scope action must be 404 (no oracle), got %d", code)
	}
}

// M4A-F1: offboarding a credential (status -> REVOKED) must kill its LIVE
// sessions immediately, not leave them valid for the 8h TTL — a stale admin
// on money-config is exactly the session that must not survive.
func TestM4A_F1_OffboardKillsLiveSession(t *testing.T) {
	f := newPortalFixture(t, "portal_offboard")
	s := f.login(t, roleKeys["OPS"])
	if code := f.call(t, &s, "GET", "/v1/portal/me", ""); code != http.StatusOK {
		t.Fatalf("live session must serve /me: %d", code)
	}
	// The session row is untouched and unexpired; only the credential dies.
	if _, err := f.db.Admin.Exec(context.Background(),
		`UPDATE admin_credentials SET status='REVOKED' WHERE actor='ops_actor'`); err != nil {
		t.Fatal(err)
	}
	if code := f.call(t, &s, "GET", "/v1/portal/me", ""); code != http.StatusUnauthorized {
		t.Fatalf("offboarded credential must kill the live session: want 401, got %d", code)
	}
	// The mutating path (which also hits the credential join) is refused too.
	if code := f.call(t, &s, "POST", "/v1/portal/config/drafts",
		`{"domain":"platform.outbox","scope":"global","reason":"t","content":{}}`); code != http.StatusUnauthorized {
		t.Fatalf("offboarded credential must fail the mutating path: want 401, got %d", code)
	}
}

// M4A-F3: a programme-scoped operator has full role power but ONLY within its
// scope — reads global defaults, reads/writes its own scope, and is refused on
// every other tenant's scope. This is the cross-scope property G4 attacks.
func TestM4B_F3_ScopeEnforcement(t *testing.T) {
	f := newPortalFixture(t, "portal_scope")
	admins := &repo.Admins{Pool: f.db.Admin}
	if err := admins.CreateWithRole(context.Background(), "adm_scoped", "scoped_admin",
		"portal-key-scoped-adm-01", "ADMIN", "programme:prg_sim_airtime01"); err != nil {
		t.Fatal(err)
	}
	s := f.login(t, "portal-key-scoped-adm-01")
	if s.actor == "" {
		t.Fatal("scoped login failed")
	}

	// READ: shared global default — permitted.
	if code := f.call(t, &s, "GET",
		"/v1/portal/config/active?domain=platform.outbox&scope=global", ""); code != http.StatusOK {
		t.Fatalf("scoped operator must read global config: %d", code)
	}
	// READ own scope — permitted.
	if code := f.call(t, &s, "GET",
		"/v1/portal/config/active?domain=treasury.guardrails&scope=programme:prg_sim_airtime01", ""); code != http.StatusOK {
		t.Fatalf("scoped operator must read own-scope config: %d", code)
	}
	// READ another tenant's scope — refused.
	if code := f.call(t, &s, "GET",
		"/v1/portal/config/active?domain=telco.adapter&scope=telco:SIM_NG", ""); code != http.StatusForbidden {
		t.Fatalf("scoped operator must NOT read another scope: want 403, got %d", code)
	}
	// WRITE own scope — permitted (not 403; the draft is created).
	if code := f.call(t, &s, "POST", "/v1/portal/config/drafts",
		`{"domain":"treasury.guardrails","scope":"programme:prg_sim_airtime01","reason":"scoped write","content":{}}`); code == http.StatusForbidden {
		t.Fatalf("scoped operator must be allowed to write its own scope, got 403")
	}
	// WRITE global — refused (mutating shared defaults needs '*').
	if code := f.call(t, &s, "POST", "/v1/portal/config/drafts",
		`{"domain":"platform.outbox","scope":"global","reason":"x","content":{}}`); code != http.StatusForbidden {
		t.Fatalf("scoped operator must NOT write global: want 403, got %d", code)
	}
	// WRITE another scope — refused.
	if code := f.call(t, &s, "POST", "/v1/portal/config/drafts",
		`{"domain":"telco.adapter","scope":"telco:SIM_NG","reason":"x","content":{}}`); code != http.StatusForbidden {
		t.Fatalf("scoped operator must NOT write another scope: want 403, got %d", code)
	}

	// OVERVIEW must not leak rows outside the operator's authority: no
	// telco:SIM_NG scope may appear (only own scope + global).
	status, body := f.callBody(t, &s, "GET", "/v1/portal/config/overview", "")
	if status != http.StatusOK {
		t.Fatalf("overview: %d", status)
	}
	var ov struct {
		Domains []struct{ Scope string } `json:"domains"`
	}
	if err := json.Unmarshal(body, &ov); err != nil {
		t.Fatal(err)
	}
	if len(ov.Domains) == 0 {
		t.Fatal("overview returned no rows for scoped operator")
	}
	for _, d := range ov.Domains {
		if d.Scope != "global" && d.Scope != "programme:prg_sim_airtime01" {
			t.Errorf("overview leaked out-of-scope row: %s", d.Scope)
		}
	}
}
