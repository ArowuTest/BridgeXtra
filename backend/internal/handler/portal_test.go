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
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/handler"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/ledger"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/mno"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/ops"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/origination"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/recovery"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/settlement"
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

	appCfg := configsvc.New(db.App)
	led := ledger.New(appCfg)
	orig := origination.New(db.App, appCfg, led, mno.NewHTTPAdapter(appCfg), slog.Default())
	p := &handler.Portal{
		Admins:     &repo.Admins{Pool: db.App},
		Sessions:   &repo.PortalSessions{Pool: db.App},
		Config:     configsvc.New(db.Worker),
		Treasury:   treasury.New(db.App, appCfg, slog.Default()),
		Ops:        ops.New(db.App, appCfg, slog.Default()),
		Settlement: settlement.New(db.App, appCfg, slog.Default()),
		Recovery:   recovery.New(db.App, appCfg, led, slog.Default()),
		Demo:       ops.NewDemo(db.App, appCfg, orig, slog.Default()),
		ReadPool:   db.Worker,
		Limiter:    testLimiter(),
		Log:        slog.Default(),
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

// seedJournal inserts a balanced journal (debit + credit) for a tenant, via
// the admin pool, returning the journal id.
func seedJournal(t *testing.T, f *portalFixture, jid, telcoID, programmeID, corr string) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.db.Admin.Exec(ctx, `
		INSERT INTO journals (journal_id, business_event_key, event_type, telco_id, programme_id, advance_id, correlation_id)
		VALUES ($1, $1||':k', 'ADVANCE_ISSUED', $2, $3, 'adv_'||$1, $4)`, jid, telcoID, programmeID, corr); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(ctx, `
		INSERT INTO journal_entries (entry_id, journal_id, account_code, debit_minor, credit_minor, currency) VALUES
		($1||'_d', $1, 'SUBSCRIBER_RECEIVABLE', 100000, 0, 'NGN'),
		($1||'_c', $1, 'AIRTIME_FUNDING_CLEARING', 0, 100000, 'NGN')`, jid); err != nil {
		t.Fatal(err)
	}
}

// M4d: the ledger browser is scope-bounded and taps through to entries. A
// programme-scoped FINANCE operator sees only their tenant's journals, cannot
// read another tenant's journal by id (no-oracle 404), and taps through to the
// balanced entries of their own.
func TestM4D_LedgerBrowser_ScopeAndTapThrough(t *testing.T) {
	f := newPortalFixture(t, "portal_ledger")
	// A second programme (out of the operator's scope) under the same telco.
	if _, err := f.db.Admin.Exec(context.Background(), `
		INSERT INTO programmes (programme_id, telco_id, code, name, status)
		SELECT 'prg_other_9999', telco_id, 'OTHER', 'Other programme', 'ACTIVE'
		FROM programmes WHERE programme_id = 'prg_sim_airtime01'`); err != nil {
		t.Fatal(err)
	}
	seedJournal(t, f, "jrn_mine", "SIM_NG", "prg_sim_airtime01", "corr_x")
	seedJournal(t, f, "jrn_other", "SIM_NG", "prg_other_9999", "corr_y")

	admins := &repo.Admins{Pool: f.db.Admin}
	if err := admins.CreateWithRole(context.Background(), "adm_fin", "fin_actor",
		"portal-key-fin-scoped-1", "FINANCE", "programme:prg_sim_airtime01"); err != nil {
		t.Fatal(err)
	}
	s := f.login(t, "portal-key-fin-scoped-1")

	// The list shows only the in-scope journal.
	status, body := f.callBody(t, &s, "GET", "/v1/portal/finance/ledger/journals", "")
	if status != http.StatusOK {
		t.Fatalf("journals list: %d", status)
	}
	var list struct {
		Journals []struct {
			JournalID string `json:"journal_id"`
		} `json:"journals"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Journals) != 1 || list.Journals[0].JournalID != "jrn_mine" {
		t.Fatalf("scoped ledger must show only the in-scope journal, got %+v", list.Journals)
	}

	// Tap-through to the balanced entries of the in-scope journal.
	status, body = f.callBody(t, &s, "GET", "/v1/portal/finance/ledger/journals/jrn_mine", "")
	if status != http.StatusOK {
		t.Fatalf("tap-through: %d", status)
	}
	var detail struct {
		Entries []struct {
			Debit struct {
				AmountMinor int64 `json:"amount_minor"`
			} `json:"debit"`
			Credit struct {
				AmountMinor int64 `json:"amount_minor"`
			} `json:"credit"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(body, &detail); err != nil {
		t.Fatal(err)
	}
	var dr, cr int64
	for _, e := range detail.Entries {
		dr += e.Debit.AmountMinor
		cr += e.Credit.AmountMinor
	}
	if len(detail.Entries) != 2 || dr != cr || dr != 100000 {
		t.Fatalf("entries must balance (debit==credit==100000), got dr=%d cr=%d n=%d", dr, cr, len(detail.Entries))
	}

	// Another tenant's journal by id is a no-oracle 404, not a 200 leak.
	if code := f.call(t, &s, "GET", "/v1/portal/finance/ledger/journals/jrn_other", ""); code != http.StatusNotFound {
		t.Fatalf("cross-scope journal by id must be 404, got %d", code)
	}
}

// seedBreak inserts an open reconciliation break for a telco via the admin pool.
func seedBreak(t *testing.T, f *portalFixture, id, telcoID string) {
	t.Helper()
	// R-P0-6: recon_items are FK-linked to a run header; seed a per-telco one.
	runID := "run_test_" + telcoID
	if _, err := f.db.Admin.Exec(context.Background(), `
		INSERT INTO recon_runs (run_id, telco_id, programme_id, layer, period_start, period_end,
		  source_record_count, source_control_total_minor, source_hash,
		  platform_record_count, platform_control_total_minor, created_by)
		VALUES ($1, $2, 'prg_sim_airtime01', 'FULFILMENT', to_timestamp(0), now(), 0,0,'seed',0,0,'test')
		ON CONFLICT (run_id) DO NOTHING`, runID, telcoID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(context.Background(), `
		INSERT INTO recon_items (recon_item_id, run_id, telco_id, item_type, status, platform_ref)
		VALUES ($1, $2, $3, 'FULFILMENT', 'BREAK_MISSING_TELCO', 'plat_'||$1)`, id, runID, telcoID); err != nil {
		t.Fatal(err)
	}
}

// M4d part 2: the breaks queue is TELCO-grained. A telco-scoped FINANCE
// operator sees and works their telco's breaks (assign -> resolve, which
// removes it from the open queue). A PROGRAMME-scoped operator has no
// telco-level authority — they see NO breaks and get a no-oracle 404 on an
// action (this is the M4C-F1 leak-class guard: a programme operator must not
// fall through to "all telcos").
func TestM4D_BreaksQueue_TelcoScopeAndWorkflow(t *testing.T) {
	f := newPortalFixture(t, "portal_breaks")
	seedBreak(t, f, "rec_break_1", "SIM_NG")
	admins := &repo.Admins{Pool: f.db.Admin}
	if err := admins.CreateWithRole(context.Background(), "adm_finT", "fin_telco",
		"portal-key-fin-telco-1", "FINANCE", "telco:SIM_NG"); err != nil {
		t.Fatal(err)
	}
	if err := admins.CreateWithRole(context.Background(), "adm_finP", "fin_prog",
		"portal-key-fin-prog-1", "FINANCE", "programme:prg_sim_airtime01"); err != nil {
		t.Fatal(err)
	}

	// Programme-scoped operator: NO telco-level authority -> empty, and a
	// 404 on any action (no cross-grain leak).
	ps := f.login(t, "portal-key-fin-prog-1")
	status, body := f.callBody(t, &ps, "GET", "/v1/portal/finance/breaks", "")
	if status != http.StatusOK {
		t.Fatalf("breaks list (programme op): %d", status)
	}
	var pl struct {
		Breaks []struct {
			ReconItemID string `json:"recon_item_id"`
		} `json:"breaks"`
	}
	if err := json.Unmarshal(body, &pl); err != nil {
		t.Fatal(err)
	}
	if len(pl.Breaks) != 0 {
		t.Fatalf("programme-scoped operator must see NO telco-level breaks, got %+v", pl.Breaks)
	}
	if code := f.call(t, &ps, "POST", "/v1/portal/finance/breaks/rec_break_1/action",
		`{"action":"RESOLVE","reason":"x"}`); code != http.StatusNotFound {
		t.Fatalf("programme-scoped action on a telco break must be 404, got %d", code)
	}

	// Telco-scoped operator: sees the break, assigns, resolves — and it leaves
	// the open queue.
	ts := f.login(t, "portal-key-fin-telco-1")
	_, body = f.callBody(t, &ts, "GET", "/v1/portal/finance/breaks", "")
	var tl struct {
		Breaks []struct {
			ReconItemID string `json:"recon_item_id"`
			Status      string `json:"status"`
		} `json:"breaks"`
	}
	if err := json.Unmarshal(body, &tl); err != nil {
		t.Fatal(err)
	}
	if len(tl.Breaks) != 1 || tl.Breaks[0].ReconItemID != "rec_break_1" {
		t.Fatalf("telco-scoped operator must see its telco's break, got %+v", tl.Breaks)
	}
	if code := f.call(t, &ts, "POST", "/v1/portal/finance/breaks/rec_break_1/action",
		`{"action":"ASSIGN","reason":"investigating"}`); code != http.StatusOK {
		t.Fatalf("assign: %d", code)
	}
	if code := f.call(t, &ts, "POST", "/v1/portal/finance/breaks/rec_break_1/action",
		`{"action":"RESOLVE","reason":"telco confirmed off-platform settlement"}`); code != http.StatusOK {
		t.Fatalf("resolve: %d", code)
	}
	// Resolved -> gone from the open queue.
	_, body = f.callBody(t, &ts, "GET", "/v1/portal/finance/breaks", "")
	if err := json.Unmarshal(body, &tl); err != nil {
		t.Fatal(err)
	}
	if len(tl.Breaks) != 0 {
		t.Fatalf("resolved break must leave the open queue, got %+v", tl.Breaks)
	}
	// A reason is mandatory on actions.
	seedBreak(t, f, "rec_break_2", "SIM_NG")
	if code := f.call(t, &ts, "POST", "/v1/portal/finance/breaks/rec_break_2/action",
		`{"action":"RESOLVE","reason":""}`); code != http.StatusBadRequest {
		t.Fatalf("action without a reason must be 400, got %d", code)
	}
}

// M4d part 3: settlement statements are telco+programme scoped. A
// programme-scoped FINANCE operator sees only their statements, taps to the
// lines, and VERIFIES a FINAL statement reproduces (verified:true) — a
// data-integrity check that recomputes from the ledger. Cross-scope reads are
// no-oracle 404s; a DRAFT statement can't be verified (409).
func TestM4D_Settlement_ScopeAndVerify(t *testing.T) {
	f := newPortalFixture(t, "portal_settle")
	ctx := context.Background()

	// A second programme (out of scope) under the same telco.
	if _, err := f.db.Admin.Exec(ctx, `
		INSERT INTO programmes (programme_id, telco_id, code, name, status)
		SELECT 'prg_other_9999', telco_id, 'OTHER', 'Other programme', 'ACTIVE'
		FROM programmes WHERE programme_id = 'prg_sim_airtime01'`); err != nil {
		t.Fatal(err)
	}

	// Generate + finalise a genuine FINAL statement for an empty period (zero
	// aggregates, but a real reproducible statement) via the settlement service.
	set := settlement.New(f.db.App, configsvc.New(f.db.App), slog.Default())
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 1, 8, 0, 0, 0, 0, time.UTC)
	st, err := set.Generate(ctx, "SIM_NG", "prg_sim_airtime01", start, end)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if err := set.Finalise(ctx, "SIM_NG", st.StatementID); err != nil {
		t.Fatalf("finalise: %v", err)
	}

	admins := &repo.Admins{Pool: f.db.Admin}
	if err := admins.CreateWithRole(ctx, "adm_finS", "fin_settle",
		"portal-key-fin-settle-1", "FINANCE", "programme:prg_sim_airtime01"); err != nil {
		t.Fatal(err)
	}
	if err := admins.CreateWithRole(ctx, "adm_finO", "fin_other",
		"portal-key-fin-other-1", "FINANCE", "programme:prg_other_9999"); err != nil {
		t.Fatal(err)
	}

	// In-scope operator: sees the statement, and it VERIFIES.
	s := f.login(t, "portal-key-fin-settle-1")
	status, body := f.callBody(t, &s, "GET", "/v1/portal/finance/settlements", "")
	if status != http.StatusOK {
		t.Fatalf("settlements list: %d", status)
	}
	var list struct {
		Statements []struct {
			StatementID string `json:"statement_id"`
			State       string `json:"state"`
		} `json:"statements"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Statements) != 1 || list.Statements[0].StatementID != st.StatementID || list.Statements[0].State != "FINAL" {
		t.Fatalf("scoped operator must see its FINAL statement, got %+v", list.Statements)
	}
	status, body = f.callBody(t, &s, "POST", "/v1/portal/finance/settlements/"+st.StatementID+"/verify", "")
	if status != http.StatusOK {
		t.Fatalf("verify: %d", status)
	}
	var vr struct {
		Verified bool `json:"verified"`
	}
	if err := json.Unmarshal(body, &vr); err != nil {
		t.Fatal(err)
	}
	if !vr.Verified {
		t.Fatal("a genuine FINAL statement must verify (verified:true)")
	}

	// TAMPER: inject a journal into the statement's period AFTER finalisation
	// (a late/edited posting). The recompute now disagrees with the pinned
	// hash — verify must flip to verified:false (a RESULT, reaching the
	// operator; not a 500).
	if _, err := f.db.Admin.Exec(ctx, `
		INSERT INTO journals (journal_id, business_event_key, event_type, telco_id, programme_id, advance_id, correlation_id, posted_at)
		VALUES ('jrn_tamper','jrn_tamper:k','ADVANCE_ISSUED','SIM_NG','prg_sim_airtime01','adv_t','corr_t','2026-01-03T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(ctx, `
		INSERT INTO journal_entries (entry_id, journal_id, account_code, debit_minor, credit_minor, currency)
		VALUES ('et_d','jrn_tamper','SUBSCRIBER_RECEIVABLE',5000,0,'NGN'),
		       ('et_c','jrn_tamper','FEE_INCOME',0,5000,'NGN')`); err != nil {
		t.Fatal(err)
	}
	status, body = f.callBody(t, &s, "POST", "/v1/portal/finance/settlements/"+st.StatementID+"/verify", "")
	if status != http.StatusOK {
		t.Fatalf("verify after tamper must still be 200 with a result: %d", status)
	}
	if err := json.Unmarshal(body, &vr); err != nil {
		t.Fatal(err)
	}
	if vr.Verified {
		t.Fatal("a tampered (post-finalisation ledger change) statement must NOT verify (verified:false)")
	}

	// Out-of-scope operator: no listing, and 404 on read + verify (no oracle).
	os := f.login(t, "portal-key-fin-other-1")
	_, body = f.callBody(t, &os, "GET", "/v1/portal/finance/settlements", "")
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Statements) != 0 {
		t.Fatalf("out-of-scope operator must see no statements, got %+v", list.Statements)
	}
	if code := f.call(t, &os, "GET", "/v1/portal/finance/settlements/"+st.StatementID, ""); code != http.StatusNotFound {
		t.Fatalf("cross-scope settlement read must be 404, got %d", code)
	}
	if code := f.call(t, &os, "POST", "/v1/portal/finance/settlements/"+st.StatementID+"/verify", ""); code != http.StatusNotFound {
		t.Fatalf("cross-scope settlement verify must be 404, got %d", code)
	}

	// A DRAFT statement can't be verified (409).
	draft, err := set.Generate(ctx, "SIM_NG", "prg_sim_airtime01",
		time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 2, 8, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if code := f.call(t, &s, "POST", "/v1/portal/finance/settlements/"+draft.StatementID+"/verify", ""); code != http.StatusConflict {
		t.Fatalf("verifying a DRAFT statement must be 409, got %d", code)
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
