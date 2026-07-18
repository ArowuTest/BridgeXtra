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

// PortalSession is a resolved live session.
type PortalSession struct {
	Actor     string
	Role      string
	ExpiresAt time.Time
}

// ResolveCredentialWithRole authenticates an admin key and returns identity
// plus role (the RBAC input).
func (r *Admins) ResolveCredentialWithRole(ctx context.Context, apiKey string) (actor, role string, err error) {
	h := sha256.Sum256([]byte(apiKey))
	err = r.Pool.QueryRow(ctx,
		`SELECT actor, role FROM admin_credentials WHERE key_hash = $1 AND status = 'ACTIVE'`,
		h[:]).Scan(&actor, &role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", fmt.Errorf("admin credential: %w", ErrNotFound)
	}
	return actor, role, err
}

// CreateWithRole provisions an admin credential with an explicit role.
func (r *Admins) CreateWithRole(ctx context.Context, adminID, actor, apiKey, role string) error {
	h := sha256.Sum256([]byte(apiKey))
	_, err := r.Pool.Exec(ctx,
		`INSERT INTO admin_credentials (admin_id, actor, key_hash, role) VALUES ($1,$2,$3,$4)`,
		adminID, actor, h[:], role)
	return err
}

// PortalSessions is the session store (tcp_app pool).
type PortalSessions struct{ Pool *pgxpool.Pool }

// Create stores a session for the opaque cookie token + CSRF token pair.
func (r *PortalSessions) Create(ctx context.Context, token, csrfToken, actor, role string, ttl time.Duration) error {
	th := sha256.Sum256([]byte(token))
	ch := sha256.Sum256([]byte(csrfToken))
	_, err := r.Pool.Exec(ctx, `
		INSERT INTO portal_sessions (session_hash, actor, role, csrf_hash, expires_at)
		VALUES ($1,$2,$3,$4, now() + $5::interval)`,
		th[:], actor, role, ch[:], fmt.Sprintf("%d seconds", int(ttl.Seconds())))
	return err
}

// Resolve returns the LIVE session for a cookie token; expired or revoked
// sessions are ErrNotFound (indistinguishable from absent — no oracle).
func (r *PortalSessions) Resolve(ctx context.Context, token string) (PortalSession, error) {
	th := sha256.Sum256([]byte(token))
	var s PortalSession
	err := r.Pool.QueryRow(ctx, `
		SELECT actor, role, expires_at FROM portal_sessions
		WHERE session_hash = $1 AND revoked_at IS NULL AND expires_at > now()`,
		th[:]).Scan(&s.Actor, &s.Role, &s.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, fmt.Errorf("portal session: %w", ErrNotFound)
	}
	return s, err
}

// VerifyCSRF constant-time-compares the presented CSRF token against the
// session's stored hash.
func (r *PortalSessions) VerifyCSRF(ctx context.Context, token, csrfToken string) error {
	th := sha256.Sum256([]byte(token))
	var stored []byte
	err := r.Pool.QueryRow(ctx, `
		SELECT csrf_hash FROM portal_sessions
		WHERE session_hash = $1 AND revoked_at IS NULL AND expires_at > now()`,
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
