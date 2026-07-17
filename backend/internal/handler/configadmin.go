package handler

// Admin configuration API (owner directive: admin manages configuration — no
// code change for config changes). Full governed lifecycle over HTTP:
// draft → submit → approve (maker≠checker) → activate → active-lookup.
// The M4 portal UI sits on exactly these endpoints.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
)

// #nosec G101 -- HTTP header NAME, not a credential value.
const headerAdminKey = "X-Admin-Key"

type adminActorKey struct{}

// AdminAuth resolves the platform-admin actor from the presented credential.
// The actor identity drives maker-checker; it is never caller-supplied.
type AdminAuth struct {
	Admins *repo.Admins
	Log    *slog.Logger
}

func (a *AdminAuth) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get(headerAdminKey)
		if key == "" {
			writeErr(w, http.StatusUnauthorized, "AUTH_INVALID_CLIENT", "missing admin credential")
			return
		}
		actor, err := a.Admins.ResolveCredential(r.Context(), key)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "AUTH_INVALID_CLIENT", "unknown or revoked admin credential")
			return
		}
		ctx := contextWithAdminActor(r.Context(), actor)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func contextWithAdminActor(ctx context.Context, actor string) context.Context {
	return context.WithValue(ctx, adminActorKey{}, actor)
}

func adminActorFrom(ctx context.Context) string {
	v, _ := ctx.Value(adminActorKey{}).(string)
	return v
}

// ConfigAdmin exposes the governed config lifecycle.
type ConfigAdmin struct {
	Svc *configsvc.Service
	Log *slog.Logger
}

type draftRequest struct {
	Domain  string          `json:"domain"`
	Scope   string          `json:"scope"`
	Reason  string          `json:"reason"`
	Content json.RawMessage `json:"content"`
}

type activateRequest struct {
	EffectiveFrom *time.Time `json:"effective_from,omitempty"`
}

type configVersionResponse struct {
	ConfigVersionID string          `json:"config_version_id"`
	Domain          string          `json:"domain"`
	Scope           string          `json:"scope"`
	VersionNo       int             `json:"version_no"`
	State           string          `json:"state"`
	Content         json.RawMessage `json:"content"`
	ContentHash     string          `json:"content_hash"`
	EffectiveFrom   *time.Time      `json:"effective_from,omitempty"`
	EffectiveTo     *time.Time      `json:"effective_to,omitempty"`
	CreatedBy       string          `json:"created_by"`
	ApprovedBy      string          `json:"approved_by,omitempty"`
	Reason          string          `json:"reason"`
}

func toConfigResponse(c entity.ConfigVersion) configVersionResponse {
	return configVersionResponse{
		ConfigVersionID: c.ConfigVersionID,
		Domain:          c.Domain,
		Scope:           c.Scope,
		VersionNo:       c.VersionNo,
		State:           string(c.State),
		Content:         json.RawMessage(c.Content),
		ContentHash:     c.ContentHash,
		EffectiveFrom:   c.EffectiveFrom,
		EffectiveTo:     c.EffectiveTo,
		CreatedBy:       c.CreatedBy,
		ApprovedBy:      c.ApprovedBy,
		Reason:          c.Reason,
	}
}

// Mount registers the admin config routes on mux, all wrapped in auth.
func (h *ConfigAdmin) Mount(mux *http.ServeMux, auth *AdminAuth) {
	mux.Handle("POST /v1/admin/config/drafts", auth.Wrap(http.HandlerFunc(h.createDraft)))
	mux.Handle("POST /v1/admin/config/{id}/submit", auth.Wrap(http.HandlerFunc(h.submit)))
	mux.Handle("POST /v1/admin/config/{id}/approve", auth.Wrap(http.HandlerFunc(h.approve)))
	mux.Handle("POST /v1/admin/config/{id}/activate", auth.Wrap(http.HandlerFunc(h.activate)))
	mux.Handle("GET /v1/admin/config/active", auth.Wrap(http.HandlerFunc(h.getActive)))
}

func (h *ConfigAdmin) createDraft(w http.ResponseWriter, r *http.Request) {
	var req draftRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "CONFIG_BAD_REQUEST", "malformed JSON body")
		return
	}
	c, err := h.Svc.CreateDraft(r.Context(), req.Domain, req.Scope, adminActorFrom(r.Context()), req.Reason, req.Content)
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, "CONFIG_BAD_REQUEST", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, toConfigResponse(c))
}

func (h *ConfigAdmin) submit(w http.ResponseWriter, r *http.Request) {
	err := h.Svc.Submit(r.Context(), r.PathValue("id"), adminActorFrom(r.Context()))
	h.writeLifecycleResult(w, r, err)
}

func (h *ConfigAdmin) approve(w http.ResponseWriter, r *http.Request) {
	err := h.Svc.Approve(r.Context(), r.PathValue("id"), adminActorFrom(r.Context()))
	h.writeLifecycleResult(w, r, err)
}

func (h *ConfigAdmin) activate(w http.ResponseWriter, r *http.Request) {
	var req activateRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "CONFIG_BAD_REQUEST", "malformed JSON body")
			return
		}
	}
	at := time.Now().UTC()
	if req.EffectiveFrom != nil {
		at = req.EffectiveFrom.UTC()
	}
	err := h.Svc.Activate(r.Context(), r.PathValue("id"), adminActorFrom(r.Context()), at)
	h.writeLifecycleResult(w, r, err)
}

func (h *ConfigAdmin) getActive(w http.ResponseWriter, r *http.Request) {
	domain := r.URL.Query().Get("domain")
	scope := r.URL.Query().Get("scope")
	if domain == "" || scope == "" {
		writeErr(w, http.StatusBadRequest, "CONFIG_BAD_REQUEST", "domain and scope query params are required")
		return
	}
	c, err := h.Svc.ActiveAt(r.Context(), domain, scope, time.Now().UTC())
	if err != nil {
		writeErr(w, http.StatusNotFound, "CONFIG_NOT_FOUND", "no active configuration for domain/scope")
		return
	}
	writeJSON(w, http.StatusOK, toConfigResponse(c))
}

// writeLifecycleResult maps typed domain errors (BC-7) to stable error codes
// exactly once, here at the boundary (V2-API-011).
func (h *ConfigAdmin) writeLifecycleResult(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, configsvc.ErrMakerChecker):
		writeErr(w, http.StatusConflict, "CONFIG_MAKER_CHECKER", "maker cannot approve their own change")
	case errors.Is(err, configsvc.ErrValidation):
		writeErr(w, http.StatusUnprocessableEntity, "CONFIG_VALIDATION_FAILED", err.Error())
	case errors.Is(err, repo.ErrNotFound):
		writeErr(w, http.StatusNotFound, "CONFIG_NOT_FOUND", "config version not found or not in the required state")
	default:
		h.Log.Error("config lifecycle failure", "err", err, "path", r.URL.Path)
		writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
