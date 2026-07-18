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
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
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
	Admins   *repo.Admins
	Sessions *repo.PortalSessions
	Config   *configsvc.Service // ADMIN config lifecycle (M4b UI sits on this)
	Log      *slog.Logger
}

// routeRoles is THE authorization map: method+pattern -> allowed roles.
// Deny-by-default — a route absent here cannot be mounted through rbac().
var routeRoles = map[string][]string{
	"GET /v1/portal/me":                    {roleAdmin, roleRisk, roleFinance, roleOps, roleSupport},
	"GET /v1/portal/config/active":         {roleAdmin, roleRisk, roleFinance},
	"POST /v1/portal/config/drafts":        {roleAdmin},
	"POST /v1/portal/config/{id}/submit":   {roleAdmin},
	"POST /v1/portal/config/{id}/approve":  {roleAdmin},
	"POST /v1/portal/config/{id}/activate": {roleAdmin},
}

// Mount registers the portal routes. Login/logout sit OUTSIDE rbac (login
// creates the session; logout only needs a valid session).
func (p *Portal) Mount(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/portal/login", p.login)
	mux.Handle("POST /v1/portal/logout", p.withSession(http.HandlerFunc(p.logout)))

	mux.Handle("GET /v1/portal/me", p.rbac("GET /v1/portal/me", http.HandlerFunc(p.me)))
	mux.Handle("GET /v1/portal/config/active", p.rbac("GET /v1/portal/config/active", http.HandlerFunc(p.configActive)))
	mux.Handle("POST /v1/portal/config/drafts", p.rbac("POST /v1/portal/config/drafts", http.HandlerFunc(p.configDraft)))
	mux.Handle("POST /v1/portal/config/{id}/submit", p.rbac("POST /v1/portal/config/{id}/submit", p.configLifecycle("submit")))
	mux.Handle("POST /v1/portal/config/{id}/approve", p.rbac("POST /v1/portal/config/{id}/approve", p.configLifecycle("approve")))
	mux.Handle("POST /v1/portal/config/{id}/activate", p.rbac("POST /v1/portal/config/{id}/activate", p.configLifecycle("activate")))
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
	CSRFToken string    `json:"csrf_token"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (p *Portal) login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&req); err != nil || req.APIKey == "" {
		writeErr(w, http.StatusBadRequest, "PORTAL_BAD_REQUEST", "api_key is required")
		return
	}
	actor, role, err := p.Admins.ResolveCredentialWithRole(r.Context(), req.APIKey)
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
	if err := p.Sessions.Create(r.Context(), token, csrf, actor, role, sessionTTL); err != nil {
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
		Actor: actor, Role: role, CSRFToken: csrf,
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
		"actor": sess.Actor, "role": sess.Role, "expires_at": sess.ExpiresAt,
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
		var err error
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
	domain := r.URL.Query().Get("domain")
	scope := r.URL.Query().Get("scope")
	if domain == "" || scope == "" {
		writeErr(w, http.StatusBadRequest, "PORTAL_BAD_REQUEST", "domain and scope are required")
		return
	}
	cv, err := p.Config.ActiveAt(r.Context(), domain, scope, time.Now().UTC())
	if err != nil {
		p.writeConfigErr(w, err)
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
