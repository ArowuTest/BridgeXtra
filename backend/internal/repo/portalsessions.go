package repo

// M4a portal sessions: server-side rows keyed by token HASH (a database
// leak never leaks live sessions), absolute expiry, revocation, per-session
// CSRF secret. Deny-by-default: resolution returns ErrNotFound unless the
// session is live.

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PortalSession is a resolved live session. Scope is the operator's telco/
// programme authorization scope ('*' = all); the handler enforces read/write
// rules against the config record's scope (M4A-F3).
type PortalSession struct {
	Actor     string
	Role      string
	Scope     string
	ExpiresAt time.Time
}

// PermitsRead reports whether this session may READ a record at recordScope.
// Global config is a shared platform default readable by every operator; a
// specific scope is readable only by a matching or all-scopes ('*') session.
func (s PortalSession) PermitsRead(recordScope string) bool {
	return s.Scope == "*" || recordScope == "global" || s.Scope == recordScope
}

// PermitsWrite reports whether this session may WRITE a record at recordScope.
// Stricter than read: writing 'global' (or any scope) requires '*' or an exact
// grant — a scoped operator can never mutate another tenant's config or the
// shared global defaults.
func (s PortalSession) PermitsWrite(recordScope string) bool {
	return s.Scope == "*" || s.Scope == recordScope
}

// PermitsTenant reports whether this session covers a tenant record that is
// scoped by BOTH a telco and a programme (guardrail trips, advances,
// settlements — the operator-read surfaces from M4c on). A '*' operator sees
// all; a telco- or programme-scoped operator sees only matching rows; a
// 'global'-only operator has no tenant authority and sees none.
func (s PortalSession) PermitsTenant(telcoID, programmeID string) bool {
	return s.Scope == "*" || s.Scope == "telco:"+telcoID || s.Scope == "programme:"+programmeID
}

// TenantFilter translates the session scope into (telco, programme) SQL bounds
// for operator reads, plus authority: authority=false means the operator has
// no tenant scope at all (e.g. 'global') and the caller must return an empty
// set rather than query. Bounds are mutually exclusive; '*' yields empty
// bounds with authority=true (sees all).
func (s PortalSession) TenantFilter() (telco, programme string, authority bool) {
	switch {
	case s.Scope == "*":
		return "", "", true
	case len(s.Scope) > 6 && s.Scope[:6] == "telco:":
		return s.Scope[6:], "", true
	case len(s.Scope) > 10 && s.Scope[:10] == "programme:":
		return "", s.Scope[10:], true
	default:
		return "", "", false // 'global' or unrecognised: no tenant authority
	}
}

// ResolveCredentialWithRole authenticates an admin key and returns identity,
// role, and authorization scope (the RBAC inputs).
func (r *Admins) ResolveCredentialWithRole(ctx context.Context, apiKey string) (actor, role, scope string, err error) {
	h := sha256.Sum256([]byte(apiKey))
	err = r.Pool.QueryRow(ctx,
		`SELECT actor, role, scope FROM admin_credentials WHERE key_hash = $1 AND status = 'ACTIVE'`,
		h[:]).Scan(&actor, &role, &scope)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", "", fmt.Errorf("admin credential: %w", ErrNotFound)
	}
	return actor, role, scope, err
}

// CreateWithRole provisions an admin credential with an explicit role and
// authorization scope ('*' = platform admin, all scopes).
func (r *Admins) CreateWithRole(ctx context.Context, adminID, actor, apiKey, role, scope string) error {
	h := sha256.Sum256([]byte(apiKey))
	_, err := r.Pool.Exec(ctx,
		`INSERT INTO admin_credentials (admin_id, actor, key_hash, role, scope) VALUES ($1,$2,$3,$4,$5)`,
		adminID, actor, h[:], role, scope)
	return err
}

// PortalSessions is the session store (tcp_app pool).
type PortalSessions struct{ Pool *pgxpool.Pool }

// Create stores a session for the opaque cookie token + CSRF token pair.
func (r *PortalSessions) Create(ctx context.Context, token, csrfToken, actor, role, scope string, ttl time.Duration) error {
	th := sha256.Sum256([]byte(token))
	ch := sha256.Sum256([]byte(csrfToken))
	_, err := r.Pool.Exec(ctx, `
		INSERT INTO portal_sessions (session_hash, actor, role, scope, csrf_hash, expires_at)
		VALUES ($1,$2,$3,$4,$5, now() + $6::interval)`,
		th[:], actor, role, scope, ch[:], fmt.Sprintf("%d seconds", int(ttl.Seconds())))
	return err
}

// Resolve returns the LIVE session for a cookie token; expired or revoked
// sessions are ErrNotFound (indistinguishable from absent — no oracle).
//
// M4A-F1: the join on admin_credentials re-checks status='ACTIVE' on EVERY
// request, so offboarding/suspending/compromising a credential kills its live
// sessions immediately rather than leaving them valid for the 8h TTL. Role
// and scope stay the login snapshot (a change there needs re-login, by design)
// — only the kill-switch is live.
func (r *PortalSessions) Resolve(ctx context.Context, token string) (PortalSession, error) {
	th := sha256.Sum256([]byte(token))
	var s PortalSession
	err := r.Pool.QueryRow(ctx, `
		SELECT s.actor, s.role, s.scope, s.expires_at
		FROM portal_sessions s
		JOIN admin_credentials a ON a.actor = s.actor
		WHERE s.session_hash = $1 AND s.revoked_at IS NULL AND s.expires_at > now()
		  AND a.status = 'ACTIVE'`,
		th[:]).Scan(&s.Actor, &s.Role, &s.Scope, &s.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, fmt.Errorf("portal session: %w", ErrNotFound)
	}
	return s, err
}

// VerifyCSRF constant-time-compares the presented CSRF token against the
// session's stored hash. Same M4A-F1 status re-check as Resolve — a revoked
// credential cannot pass CSRF either.
func (r *PortalSessions) VerifyCSRF(ctx context.Context, token, csrfToken string) error {
	th := sha256.Sum256([]byte(token))
	var stored []byte
	err := r.Pool.QueryRow(ctx, `
		SELECT s.csrf_hash
		FROM portal_sessions s
		JOIN admin_credentials a ON a.actor = s.actor
		WHERE s.session_hash = $1 AND s.revoked_at IS NULL AND s.expires_at > now()
		  AND a.status = 'ACTIVE'`,
		th[:]).Scan(&stored)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("portal session: %w", ErrNotFound)
	}
	if err != nil {
		return err
	}
	ch := sha256.Sum256([]byte(csrfToken))
	if subtle.ConstantTimeCompare(stored, ch[:]) != 1 {
		return fmt.Errorf("csrf token mismatch: %w", ErrNotFound)
	}
	return nil
}

// Revoke ends a session (logout); idempotent.
func (r *PortalSessions) Revoke(ctx context.Context, token string) error {
	th := sha256.Sum256([]byte(token))
	_, err := r.Pool.Exec(ctx, `
		UPDATE portal_sessions SET revoked_at = now()
		WHERE session_hash = $1 AND revoked_at IS NULL`, th[:])
	return err
}
