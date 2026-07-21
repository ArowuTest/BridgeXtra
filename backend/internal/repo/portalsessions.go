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

// OperatorScope is the tenant-data read boundary for a portal operator. It is
// the STRUCTURAL enforcement of scope on the BYPASSRLS operator-read pool
// (M4C-F1): the operator-read functions REQUIRE one and derive their SQL
// bounds from it, and it is constructible ONLY from a session, so a
// cross-tenant read that "forgets" to scope — or passes empty bounds meaning
// "see all" — is impossible to write. The see-all bounds (empty telco AND
// programme WITH authority) are reachable exclusively for a '*' platform
// admin; every other operator gets hard bounds or no authority at all. This
// is the mountRBAC discipline applied to tenant reads: the unsafe call does
// not compile, rather than being merely absent today.
type OperatorScope struct {
	telco     string
	programme string
	authority bool // false ('global'/unrecognised) => no tenant reads at all
}

// TelcoLevelBound resolves the scope for a TELCO-GRAINED resource that has no
// programme dimension (e.g. reconciliation breaks). Because such rows can't be
// filtered by programme, a programme-scoped operator's empty telco bound would
// silently mean "all telcos" — the M4C-F1 leak class. So only a '*' admin
// (telco ” = all) or a telco-scoped operator has authority here; a
// programme- or global-scoped operator gets ok=false and reads NOTHING. The
// operator sees telco-level data only when their grant is at or above telco.
func (s OperatorScope) TelcoLevelBound() (telco string, ok bool) {
	if !s.authority || s.programme != "" {
		return "", false
	}
	return s.telco, true
}

// OperatorScope derives the read boundary from the session — the ONLY
// constructor. Its fields are unexported, so no other package can forge a
// see-all scope with empty bounds.
func (s PortalSession) OperatorScope() OperatorScope {
	switch {
	case s.Scope == "*":
		return OperatorScope{authority: true} // empty bounds => sees all (admin only)
	case len(s.Scope) > 6 && s.Scope[:6] == "telco:":
		return OperatorScope{telco: s.Scope[6:], authority: true}
	case len(s.Scope) > 10 && s.Scope[:10] == "programme:":
		return OperatorScope{programme: s.Scope[10:], authority: true}
	default:
		return OperatorScope{} // 'global' or unrecognised: authority=false, no reads
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

// --- Gate B #1 Slice 2: DB-enforced operator reads ------------------------

// OperatorReader runs portal operator READS on the RLS-enforced tcp_operator
// pool with the tenant scope set LOCAL from the TRUSTED session authority. It is
// the SINGLE server-side site where app.op_all is ever set — and only for the
// '*' platform admin. A telco- or programme-scoped session can never reach the
// op_all path (they take their own branch), which is the security boundary the
// review requires. Resolve is a trusted pool (worker/owner, BYPASSRLS) used ONLY
// to look up which telco owns a programme — never to read tenant money data.
type OperatorReader struct {
	Pool    *pgxpool.Pool
	Resolve *pgxpool.Pool
}

// ErrUnknownProgramme: a programme-scoped session named a programme with no telco.
var ErrUnknownProgramme = errors.New("repo: programme has no telco (cannot scope operator read)")

// Read opens a tx on the operator pool, sets the scope GUCs LOCAL from the scope's
// authority, and runs fn. Fail-closed: a scope without authority sets no GUC, so
// the existing RLS telco policy matches nothing and the read returns empty — a
// forgotten/mis-derived scope leaks nothing.
func (r OperatorReader) Read(ctx context.Context, scope OperatorScope, fn func(tx pgx.Tx) error) error {
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("operator read begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	switch {
	case !scope.authority:
		// No authority => no reads. Leave every GUC unset; RLS returns empty.
	case scope.telco != "":
		// Telco-scoped: DB-ENFORCED by the existing telco RLS policy.
		if _, err := tx.Exec(ctx, `SELECT set_config('app.telco_id',$1,true)`, scope.telco); err != nil {
			return fmt.Errorf("scope telco: %w", err)
		}
	case scope.programme != "":
		// Programme-scoped: pin the telco (DB-enforced) via a trusted lookup; the
		// repo query keeps the programme_id filter (intra-tenant, app-level residual).
		telco, err := r.programmeTelco(ctx, scope.programme)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT set_config('app.telco_id',$1,true)`, telco); err != nil {
			return fmt.Errorf("scope programme telco: %w", err)
		}
	default:
		// The ONLY op_all path: authority AND no telco AND no programme => the '*'
		// platform admin reading the whole estate. App-gated, bounded, audited here.
		if _, err := tx.Exec(ctx, `SELECT set_config('app.op_all','true',true)`); err != nil {
			return fmt.Errorf("scope op_all: %w", err)
		}
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// programmeTelco resolves a programme's owning telco via the trusted resolver pool
// (BYPASSRLS) — a metadata lookup, not tenant data.
func (r OperatorReader) programmeTelco(ctx context.Context, programmeID string) (string, error) {
	var telco string
	err := r.Resolve.QueryRow(ctx, `SELECT telco_id FROM programmes WHERE programme_id=$1`, programmeID).Scan(&telco)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrUnknownProgramme
	}
	if err != nil {
		return "", fmt.Errorf("resolve programme telco: %w", err)
	}
	return telco, nil
}
