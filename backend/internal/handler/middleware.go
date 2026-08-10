// Package handler is the HTTP boundary: authentication, tenant-context
// resolution and validation. It never touches the repo layer directly except
// through the boundary-approved helpers (credential resolution, audit).
package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform/buildinfo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
)

// mustJSON marshals audit detail maps; the inputs are our own string fields,
// so a marshal error is impossible — but never build JSON by concatenation.
func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte(`{}`)
	}
	return b
}

// #nosec G101 -- these are HTTP header NAMES, not credential values.
const (
	headerAPIKey  = "X-Api-Key"
	headerTelcoID = "X-Telco-Id"
)

// TenantAuth resolves the telco context from the presented credential
// (V2-TEN-002: authenticated identity, never the payload alone) and rejects
// any conflicting caller-supplied telco identifier (V2-TEN-003, EDG-026),
// security-auditing the attempt.
type TenantAuth struct {
	Telcos *repo.Telcos
	Audit  repo.Audit
	Pool   *pgxpool.Pool // app-role pool for audit writes
	Log    *slog.Logger
}

func (m *TenantAuth) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiKey := r.Header.Get(headerAPIKey)
		if apiKey == "" {
			writeErr(w, http.StatusUnauthorized, "AUTH_INVALID_CLIENT", "missing credential")
			return
		}
		telcoID, credentialID, err := m.Telcos.ResolveCredential(r.Context(), apiKey)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "AUTH_INVALID_CLIENT", "unknown or revoked credential")
			return
		}
		// Conflicting payload/header tenant → reject + security audit, no data lookup.
		if claimed := r.Header.Get(headerTelcoID); claimed != "" && claimed != telcoID {
			// Platform-scope audit row (no tenant context exists for a rejected request);
			// the credential's telco travels in detail, not in the tenant column.
			_ = m.Audit.InsertPlatform(r.Context(), m.Pool, entity.AuditEvent{
				ID:         platform.NewID("aud"),
				Actor:      "credential:" + credentialID,
				Action:     entity.AuditTenantContextMismatch,
				TargetType: "telco",
				TargetID:   claimed,
				Reason:     "credential tenant does not match claimed tenant",
				Detail:     mustJSON(map[string]string{"credential_telco": telcoID, "claimed_telco": claimed}),
				SourceIP:   r.RemoteAddr,
			})
			m.Log.Warn("tenant context mismatch rejected",
				"credential", credentialID, "credential_telco", telcoID, "claimed_telco", claimed)
			writeErr(w, http.StatusForbidden, "TENANT_CONTEXT_MISMATCH", "tenant context mismatch")
			return
		}
		next.ServeHTTP(w, r.WithContext(platform.WithTenant(r.Context(), telcoID)))
	})
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error_code": code, "message": msg, "retryable": false,
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Live is the unauthenticated LIVENESS endpoint (BX-MED-005): is the process up?
// It deliberately takes NO dependency on the database. A liveness probe that pinged
// the DB would turn a transient DB blip into a pod-kill/restart storm — restarting
// the process cannot fix an unreachable database, it only removes capacity while the
// DB recovers. Liveness answers "would a restart help?"; readiness (Health) answers
// "can I serve traffic right now?".
func Live() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}
}

// Version reports BUILD IDENTITY (BX-MED-005) so an operator can answer "which build is serving
// this traffic?" from the process itself during an incident. Build identity only — never config,
// environment, DSNs or secrets — because it is reachable wherever its plane is reachable.
func Version() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, buildinfo.Info())
	}
}

// Health is the unauthenticated READINESS endpoint (V2-SRV-008, BX-MED-005): can this
// instance serve traffic — is its database reachable? A failing readiness probe drains
// the instance from the load balancer WITHOUT killing it, so a DB outage sheds traffic
// instead of triggering restarts. Wire this to /readyz and Live() to /healthz.
func Health(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2_000_000_000)
		defer cancel()
		if err := pool.Ping(ctx); err != nil {
			writeErr(w, http.StatusServiceUnavailable, "SYSTEM_TEMPORARILY_UNAVAILABLE", "db unreachable")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}
}
