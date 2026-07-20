package handler

// M4a portal surface: session auth (httpOnly cookie + CSRF) and
// DENY-BY-DEFAULT RBAC. The G4 bar is server-side authorization — every
// portal route names the roles allowed to reach it, and a route without an
// entry in the map is unreachable for everyone. UI hiding is decoration;
// THIS is the control.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform/ratelimit"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/ops"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/recovery"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/settlement"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/treasury"
)

const (
	portalCookie = "bx_portal_session"
	csrfHeader   = "X-CSRF-Token"
	sessionTTL   = 8 * time.Hour
	roleAdmin    = "ADMIN"
	roleRisk     = "RISK"
	roleFinance  = "FINANCE"
	roleOps      = "OPS"
	roleSupport  = "SUPPORT"
)

// Portal serves the operator console API.
type Portal struct {
	Admins     *repo.Admins
	Sessions   *repo.PortalSessions
	Config     *configsvc.Service  // ADMIN config lifecycle (M4b UI sits on this)
	Treasury   *treasury.Service   // M4c guardrail re-arm actions (tenant tx)
	Ops        *ops.Service        // M4d breaks-queue actions (tenant tx)
	Settlement *settlement.Service // M4d settlement verification (tenant tx)
	Recovery   *recovery.Service   // M4e parked-reversal retry (tenant tx, reuses guarded apply)
	Demo       *ops.Demo           // M4e-3 fault demo (real origination path, sim-only allowlist)
	ReadPool   *pgxpool.Pool       // M4c operator cross-tenant reads (worker/BYPASSRLS)
	Limiter    *ratelimit.Limiter  // R-P0-8 inbound rate limit (login)
	Log        *slog.Logger
}

// routeRoles is THE authorization map: method+pattern -> allowed roles.
// Deny-by-default — a route absent here cannot be mounted through rbac().
var routeRoles = map[string][]string{
	"GET /v1/portal/me":                    {roleAdmin, roleRisk, roleFinance, roleOps, roleSupport},
	"GET /v1/portal/config/active":         {roleAdmin, roleRisk, roleFinance},
	"GET /v1/portal/config/overview":       {roleAdmin, roleRisk, roleFinance},
	"GET /v1/portal/config/versions":       {roleAdmin, roleRisk, roleFinance},
	"GET /v1/portal/config/{id}":           {roleAdmin, roleRisk, roleFinance},
	"POST /v1/portal/config/drafts":        {roleAdmin},
	"POST /v1/portal/config/{id}/submit":   {roleAdmin},
	"POST /v1/portal/config/{id}/approve":  {roleAdmin},
	"POST /v1/portal/config/{id}/activate": {roleAdmin},

	// M4c risk workspace: reads for oversight roles; re-arm actions for the
	// risk actioners (two-person rule schema-enforced regardless).
	"GET /v1/portal/risk/trips":                     {roleAdmin, roleRisk, roleFinance},
	"POST /v1/portal/risk/trips/{id}/request-rearm": {roleAdmin, roleRisk},
	"POST /v1/portal/risk/trips/{id}/approve-rearm": {roleAdmin, roleRisk},

	// M4d finance workspace: ledger browser + breaks queue + settlement verify.
	"GET /v1/portal/finance/ledger/journals":          {roleAdmin, roleFinance},
	"GET /v1/portal/finance/ledger/journals/{id}":     {roleAdmin, roleFinance},
	"GET /v1/portal/finance/breaks":                   {roleAdmin, roleFinance},
	"POST /v1/portal/finance/breaks/{id}/action":      {roleAdmin, roleFinance},
	"GET /v1/portal/finance/settlements":              {roleAdmin, roleFinance},
	"GET /v1/portal/finance/settlements/{id}":         {roleAdmin, roleFinance},
	"POST /v1/portal/finance/settlements/{id}/verify": {roleAdmin, roleFinance},

	// M4e ops workspace (C7 — roles pinned deliberately): queue READS go to
	// OPS plus FINANCE (both queues are money-adjacent: ambiguous fulfilments
	// are unresolved exposure; parked reversals are pending money movements).
	// ACTIONS are OPS-only (+ ADMIN): enquire-now reschedules the resolver,
	// retry re-runs the guarded apply. RISK/SUPPORT get neither — their
	// surfaces are the risk workspace and (M4f) complaints.
	"GET /v1/portal/ops/fulfilments":                   {roleAdmin, roleOps, roleFinance},
	"POST /v1/portal/ops/fulfilments/{id}/enquire-now": {roleAdmin, roleOps},
	"GET /v1/portal/ops/reversals":                     {roleAdmin, roleOps, roleFinance},
	"POST /v1/portal/ops/reversals/{id}/retry":         {roleAdmin, roleOps},

	// M4e-2 subscriber status actions (VR-35-F1): reads for all oversight
	// roles; request/decide for OPS and RISK (barring is a conduct/risk
	// action) — the two-actor rule is schema-enforced regardless of role.
	"GET /v1/portal/ops/status-actions":               {roleAdmin, roleOps, roleRisk, roleFinance},
	"POST /v1/portal/ops/status-actions":              {roleAdmin, roleOps, roleRisk},
	"POST /v1/portal/ops/status-actions/{id}/approve": {roleAdmin, roleOps, roleRisk},
	"POST /v1/portal/ops/status-actions/{id}/reject":  {roleAdmin, roleOps, roleRisk},

	// M4e-3 fault demo: runs for OPS (+ADMIN); the artifact chain is
	// readable by all oversight roles. Config-allowlisted to the simulator
	// tenant — structurally cannot touch a real telco.
	"GET /v1/portal/ops/demo/scenarios": {roleAdmin, roleOps, roleRisk, roleFinance},
	"POST /v1/portal/ops/demo/run":      {roleAdmin, roleOps},
	"GET /v1/portal/ops/demo/runs":      {roleAdmin, roleOps, roleRisk, roleFinance},
	"GET /v1/portal/ops/demo/runs/{id}": {roleAdmin, roleOps, roleRisk, roleFinance},

	// M4f support workspace (V3-ORG-005): SUPPORT's ENTIRE write surface is
	// the complaint workflow — case management, never financial truth. The
	// masked timeline is read-only evidence; reads go to all console roles.
	"GET /v1/portal/support/subscriber":                {roleAdmin, roleSupport, roleOps, roleRisk, roleFinance},
	"GET /v1/portal/support/complaints":                {roleAdmin, roleSupport, roleOps, roleRisk, roleFinance},
	"POST /v1/portal/support/complaints":               {roleAdmin, roleSupport, roleOps},
	"POST /v1/portal/support/complaints/{id}/progress": {roleAdmin, roleSupport, roleOps},
}

// RBACRoutes returns a copy of the route->roles authorization map. It exists
// so tests (and future tooling) can drive their coverage from the SAME map
// production enforces — the matrix cannot silently drift from the real policy
// (M4A-F2).
func RBACRoutes() map[string][]string {
	out := make(map[string][]string, len(routeRoles))
	for k, v := range routeRoles {
		out[k] = append([]string(nil), v...)
	}
	return out
}

// Mount registers the portal routes. Login/logout sit OUTSIDE rbac (login
// creates the session; logout only needs a valid session). Every other route
// is mounted through mountRBAC so the mux pattern, the RBAC key, and the role
// list are ONE fact (M4A-F2).
func (p *Portal) Mount(mux *http.ServeMux) {
	if p.Limiter == nil {
		panic("portal: rate limiter is required (R-P0-8 fail-closed)")
	}
	// R-P0-8: /login is rate-limited by client IP (credential-stuffing).
	mux.Handle("POST /v1/portal/login", rateLimited(p.Limiter, "login", clientIP, http.HandlerFunc(p.login)))
	mux.Handle("POST /v1/portal/logout", p.withSession(http.HandlerFunc(p.logout)))

	p.mountRBAC(mux, "GET /v1/portal/me", http.HandlerFunc(p.me))
	p.mountRBAC(mux, "GET /v1/portal/config/active", http.HandlerFunc(p.configActive))
	p.mountRBAC(mux, "GET /v1/portal/config/overview", http.HandlerFunc(p.configOverview))
	p.mountRBAC(mux, "GET /v1/portal/config/versions", http.HandlerFunc(p.configVersions))
	p.mountRBAC(mux, "GET /v1/portal/config/{id}", http.HandlerFunc(p.configGet))
	p.mountRBAC(mux, "POST /v1/portal/config/drafts", http.HandlerFunc(p.configDraft))
	p.mountRBAC(mux, "POST /v1/portal/config/{id}/submit", p.configLifecycle("submit"))
	p.mountRBAC(mux, "POST /v1/portal/config/{id}/approve", p.configLifecycle("approve"))
	p.mountRBAC(mux, "POST /v1/portal/config/{id}/activate", p.configLifecycle("activate"))

	p.mountRBAC(mux, "GET /v1/portal/risk/trips", http.HandlerFunc(p.riskTrips))
	p.mountRBAC(mux, "POST /v1/portal/risk/trips/{id}/request-rearm", http.HandlerFunc(p.riskRequestRearm))
	p.mountRBAC(mux, "POST /v1/portal/risk/trips/{id}/approve-rearm", http.HandlerFunc(p.riskApproveRearm))

	p.mountRBAC(mux, "GET /v1/portal/finance/ledger/journals", http.HandlerFunc(p.ledgerJournals))
	p.mountRBAC(mux, "GET /v1/portal/finance/ledger/journals/{id}", http.HandlerFunc(p.ledgerJournal))
	p.mountRBAC(mux, "GET /v1/portal/finance/breaks", http.HandlerFunc(p.financeBreaks))
	p.mountRBAC(mux, "POST /v1/portal/finance/breaks/{id}/action", http.HandlerFunc(p.financeBreakAction))
	p.mountRBAC(mux, "GET /v1/portal/finance/settlements", http.HandlerFunc(p.financeSettlements))
	p.mountRBAC(mux, "GET /v1/portal/finance/settlements/{id}", http.HandlerFunc(p.financeSettlement))
	p.mountRBAC(mux, "POST /v1/portal/finance/settlements/{id}/verify", http.HandlerFunc(p.financeSettlementVerify))

	p.mountRBAC(mux, "GET /v1/portal/ops/fulfilments", http.HandlerFunc(p.opsFulfilments))
	p.mountRBAC(mux, "POST /v1/portal/ops/fulfilments/{id}/enquire-now", http.HandlerFunc(p.opsEnquireNow))
	p.mountRBAC(mux, "GET /v1/portal/ops/reversals", http.HandlerFunc(p.opsReversals))
	p.mountRBAC(mux, "POST /v1/portal/ops/reversals/{id}/retry", http.HandlerFunc(p.opsReversalRetry))

	p.mountRBAC(mux, "GET /v1/portal/ops/status-actions", http.HandlerFunc(p.opsStatusActions))
	p.mountRBAC(mux, "POST /v1/portal/ops/status-actions", http.HandlerFunc(p.opsStatusActionRequest))
	p.mountRBAC(mux, "POST /v1/portal/ops/status-actions/{id}/approve", p.opsStatusActionDecide(true))
	p.mountRBAC(mux, "POST /v1/portal/ops/status-actions/{id}/reject", p.opsStatusActionDecide(false))

	p.mountRBAC(mux, "GET /v1/portal/ops/demo/scenarios", http.HandlerFunc(p.opsDemoScenarios))
	p.mountRBAC(mux, "POST /v1/portal/ops/demo/run", http.HandlerFunc(p.opsDemoRun))
	p.mountRBAC(mux, "GET /v1/portal/ops/demo/runs", http.HandlerFunc(p.opsDemoRuns))
	p.mountRBAC(mux, "GET /v1/portal/ops/demo/runs/{id}", http.HandlerFunc(p.opsDemoRunDetail))

	p.mountRBAC(mux, "GET /v1/portal/support/subscriber", http.HandlerFunc(p.supportTimeline))
	p.mountRBAC(mux, "GET /v1/portal/support/complaints", http.HandlerFunc(p.supportComplaints))
	p.mountRBAC(mux, "POST /v1/portal/support/complaints", http.HandlerFunc(p.supportComplaintOpen))
	p.mountRBAC(mux, "POST /v1/portal/support/complaints/{id}/progress", http.HandlerFunc(p.supportComplaintProgress))
}

// mountRBAC registers a route through the RBAC middleware and REQUIRES a
// routeRoles entry for the exact pattern — a mount without a role list panics
// at boot (fail-closed: a portal route can never become reachable without an
// explicit allowlist). This collapses the three former copies of the
// route->roles fact — mux pattern, rbac key, map key — into one (M4A-F2).
func (p *Portal) mountRBAC(mux *http.ServeMux, pattern string, h http.Handler) {
	if _, ok := routeRoles[pattern]; !ok {
		panic("portal: route mounted without an RBAC entry: " + pattern)
	}
	mux.Handle(pattern, p.rbac(pattern, h))
}

// restrictFor returns the config-scope filter for a session: ” (unrestricted)
// for a platform admin ('*'), otherwise the operator's single scope. Reads are
// bounded to this plus shared 'global' defaults in the repo layer (M4A-F3).
func restrictFor(s repo.PortalSession) string {
	if s.Scope == "*" {
		return ""
	}
	return s.Scope
}

// --- session + RBAC middleware ---------------------------------------------

type sessionKey struct{}

func contextWithSession(ctx context.Context, s repo.PortalSession) context.Context {
	return context.WithValue(ctx, sessionKey{}, s)
}

func sessionFrom(ctx context.Context) repo.PortalSession {
	s, _ := ctx.Value(sessionKey{}).(repo.PortalSession)
	return s
}

// withSession authenticates the cookie, enforces CSRF on mutating methods,
// and stashes the session in context. 401 always looks the same — absent,
// expired, revoked and forged are indistinguishable (no oracle).
func (p *Portal) withSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(portalCookie)
		if err != nil || c.Value == "" {
			writeErr(w, http.StatusUnauthorized, "PORTAL_UNAUTHENTICATED", "sign in required")
			return
		}
		sess, err := p.Sessions.Resolve(r.Context(), c.Value)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "PORTAL_UNAUTHENTICATED", "sign in required")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if err := p.Sessions.VerifyCSRF(r.Context(), c.Value, r.Header.Get(csrfHeader)); err != nil {
				writeErr(w, http.StatusForbidden, "PORTAL_CSRF", "missing or invalid CSRF token")
				return
			}
		}
		ctx := contextWithSession(r.Context(), sess)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// rbac wraps withSession and enforces the role map for the named route.
// A route missing from the map is refused for EVERYONE — deny by default.
func (p *Portal) rbac(route string, next http.Handler) http.Handler {
	allowed, ok := routeRoles[route]
	return p.withSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !ok {
			p.Log.Error("portal route mounted without an RBAC entry — refusing all access", "route", route)
			writeErr(w, http.StatusForbidden, "PORTAL_FORBIDDEN", "not permitted")
			return
		}
		sess := sessionFrom(r.Context())
		for _, role := range allowed {
			if sess.Role == role {
				next.ServeHTTP(w, r)
				return
			}
		}
		p.Log.Warn("portal RBAC refusal", "route", route, "actor", sess.Actor, "role", sess.Role)
		writeErr(w, http.StatusForbidden, "PORTAL_FORBIDDEN", "not permitted")
	}))
}

// --- auth endpoints ---------------------------------------------------------

type loginRequest struct {
	APIKey string `json:"api_key"`
}

type loginResponse struct {
	Actor     string    `json:"actor"`
	Role      string    `json:"role"`
	Scope     string    `json:"scope"`
	CSRFToken string    `json:"csrf_token"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (p *Portal) login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&req); err != nil || req.APIKey == "" {
		writeErr(w, http.StatusBadRequest, "PORTAL_BAD_REQUEST", "api_key is required")
		return
	}
	actor, role, scope, err := p.Admins.ResolveCredentialWithRole(r.Context(), req.APIKey)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			writeErr(w, http.StatusUnauthorized, "PORTAL_UNAUTHENTICATED", "invalid credentials")
			return
		}
		writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
		return
	}
	token, err := randomToken()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
		return
	}
	csrf, err := randomToken()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
		return
	}
	if err := p.Sessions.Create(r.Context(), token, csrf, actor, role, scope, sessionTTL); err != nil {
		p.Log.Error("session create failed", "err", err)
		writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
		return
	}
	// Secure is unconditional — a cookie-security attribute must not be
	// env-disarmable (browsers accept Secure cookies on localhost, so local
	// dev over plain http still works).
	http.SetCookie(w, &http.Cookie{
		Name: portalCookie, Value: token, Path: "/v1/portal",
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
		MaxAge: int(sessionTTL.Seconds()),
	})
	p.Log.Info("portal login", "actor", actor, "role", role)
	writeJSON(w, http.StatusOK, loginResponse{
		Actor: actor, Role: role, Scope: scope, CSRFToken: csrf,
		ExpiresAt: time.Now().UTC().Add(sessionTTL),
	})
}

func (p *Portal) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(portalCookie); err == nil {
		_ = p.Sessions.Revoke(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name: portalCookie, Value: "", Path: "/v1/portal",
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: -1,
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "signed_out"})
}

func (p *Portal) me(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"actor": sess.Actor, "role": sess.Role, "scope": sess.Scope, "expires_at": sess.ExpiresAt,
	})
}

// --- config lifecycle (the M4b UI sits on these; RBAC ADMIN) ---------------

func (p *Portal) configDraft(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	var req draftRequest // shared with the header-authenticated admin API
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "PORTAL_BAD_REQUEST", "malformed JSON body")
		return
	}
	if !sess.PermitsWrite(req.Scope) {
		p.Log.Warn("portal scope refusal (draft)", "actor", sess.Actor, "session_scope", sess.Scope, "record_scope", req.Scope)
		writeErr(w, http.StatusForbidden, "PORTAL_FORBIDDEN", "not permitted for this scope")
		return
	}
	cv, err := p.Config.CreateDraft(r.Context(), req.Domain, req.Scope, sess.Actor, req.Reason, req.Content)
	if err != nil {
		p.writeConfigErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toConfigResponse(cv))
}

func (p *Portal) configLifecycle(step string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess := sessionFrom(r.Context())
		id := r.PathValue("id")
		// Load the target version to authorize against ITS scope — a scoped
		// operator can only move versions within their own scope (M4A-F3).
		cv, err := p.Config.GetVersion(r.Context(), id)
		if err != nil {
			p.writeConfigErr(w, err)
			return
		}
		if !sess.PermitsWrite(cv.Scope) {
			p.Log.Warn("portal scope refusal (lifecycle)", "actor", sess.Actor, "step", step, "session_scope", sess.Scope, "record_scope", cv.Scope)
			writeErr(w, http.StatusForbidden, "PORTAL_FORBIDDEN", "not permitted for this scope")
			return
		}
		switch step {
		case "submit":
			err = p.Config.Submit(r.Context(), id, sess.Actor)
		case "approve":
			err = p.Config.Approve(r.Context(), id, sess.Actor)
		case "activate":
			err = p.Config.Activate(r.Context(), id, sess.Actor, time.Now().UTC())
		}
		if err != nil {
			p.writeConfigErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"config_version_id": id, "step": step})
	}
}

func (p *Portal) configActive(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	domain := r.URL.Query().Get("domain")
	scope := r.URL.Query().Get("scope")
	if domain == "" || scope == "" {
		writeErr(w, http.StatusBadRequest, "PORTAL_BAD_REQUEST", "domain and scope are required")
		return
	}
	if !sess.PermitsRead(scope) {
		writeErr(w, http.StatusForbidden, "PORTAL_FORBIDDEN", "not permitted for this scope")
		return
	}
	cv, err := p.Config.ActiveAt(r.Context(), domain, scope, time.Now().UTC())
	if err != nil {
		p.writeConfigErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toConfigResponse(cv))
}

type configSummaryResponse struct {
	Domain          string     `json:"domain"`
	Scope           string     `json:"scope"`
	ActiveVersionNo int        `json:"active_version_no"`
	ActiveSince     *time.Time `json:"active_since,omitempty"`
	PendingCount    int        `json:"pending_count"`
}

func (p *Portal) configOverview(w http.ResponseWriter, r *http.Request) {
	ss, err := p.Config.Overview(r.Context(), restrictFor(sessionFrom(r.Context())))
	if err != nil {
		p.writeConfigErr(w, err)
		return
	}
	out := make([]configSummaryResponse, 0, len(ss))
	for _, s := range ss {
		out = append(out, configSummaryResponse{
			Domain: s.Domain, Scope: s.Scope,
			ActiveVersionNo: s.ActiveVersionNo, ActiveSince: s.ActiveSince,
			PendingCount: s.PendingCount,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"domains": out})
}

func (p *Portal) configVersions(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	q := r.URL.Query()
	// If a specific scope is requested, a scoped operator must be permitted to
	// read it; otherwise results are bounded to their authority in the repo.
	if s := q.Get("scope"); s != "" && !sess.PermitsRead(s) {
		writeErr(w, http.StatusForbidden, "PORTAL_FORBIDDEN", "not permitted for this scope")
		return
	}
	limit := 0
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			writeErr(w, http.StatusBadRequest, "PORTAL_BAD_REQUEST", "limit must be a positive integer")
			return
		}
		limit = n
	}
	vs, err := p.Config.ListVersions(r.Context(), q.Get("domain"), q.Get("scope"), restrictFor(sess), limit)
	if err != nil {
		p.writeConfigErr(w, err)
		return
	}
	out := make([]configVersionResponse, 0, len(vs))
	for _, v := range vs {
		out = append(out, toConfigResponse(v))
	}
	writeJSON(w, http.StatusOK, map[string]any{"versions": out})
}

func (p *Portal) configGet(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	cv, err := p.Config.GetVersion(r.Context(), r.PathValue("id"))
	if err != nil {
		p.writeConfigErr(w, err)
		return
	}
	if !sess.PermitsRead(cv.Scope) {
		// A scoped operator asking for another tenant's version by id gets the
		// same 404 as a nonexistent id — no cross-scope existence oracle.
		writeErr(w, http.StatusNotFound, "CONFIG_NOT_FOUND", "config version not found")
		return
	}
	writeJSON(w, http.StatusOK, toConfigResponse(cv))
}

// writeConfigErr maps config-service errors once (BC-7), mirroring the
// header-authenticated admin API's vocabulary.
func (p *Portal) writeConfigErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, configsvc.ErrMakerChecker):
		writeErr(w, http.StatusConflict, "CONFIG_MAKER_CHECKER", "approver must differ from maker")
	case errors.Is(err, configsvc.ErrValidation):
		writeErr(w, http.StatusUnprocessableEntity, "CONFIG_VALIDATION_FAILED", err.Error())
	case errors.Is(err, repo.ErrNotFound):
		writeErr(w, http.StatusNotFound, "CONFIG_NOT_FOUND", "config version not found")
	default:
		p.Log.Error("portal config error", "err", err)
		writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
	}
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
