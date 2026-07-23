package repo

import (
	"context"
	"crypto/sha256"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Admins resolves platform-administrator credentials — the actor identity
// behind maker-checker on the admin API. Hash-only storage (V2-SEC-005).
type Admins struct{ Pool *pgxpool.Pool }

// Operator is the console view of a provisioned operator credential.
type Operator struct {
	Actor  string
	Role   string
	Scope  string
	Status string // ACTIVE | REVOKED
}

// CreateWithRoleTx provisions an operator credential inside a caller's tx — the
// governed-create path (grant: INSERT on admin_credentials, migration 0047). The
// key is stored hash-only; the plaintext is the caller's to return exactly once.
func (r *Admins) CreateWithRoleTx(ctx context.Context, tx pgx.Tx, adminID, actor, apiKey, role, scope string) error {
	h := sha256.Sum256([]byte(apiKey))
	_, err := tx.Exec(ctx,
		`INSERT INTO admin_credentials (admin_id, actor, key_hash, role, scope) VALUES ($1,$2,$3,$4,$5)`,
		adminID, actor, h[:], role, scope)
	return err
}

// RevokeCredential deactivates an operator (status -> REVOKED) via the only
// mutation the app role is granted on admin_credentials (UPDATE(status), 0047).
// The M4A-F1 kill-switch (Resolve re-checks status='ACTIVE') ends the operator's
// live sessions on their very next request. Idempotent: a not-ACTIVE actor
// returns ok=false. There is deliberately NO grant to change role or scope — a
// privilege change is impossible except by revoke-and-recreate.
func (r *Admins) RevokeCredential(ctx context.Context, tx pgx.Tx, actor string) (bool, error) {
	ct, err := tx.Exec(ctx,
		`UPDATE admin_credentials SET status = 'REVOKED' WHERE actor = $1 AND status = 'ACTIVE'`, actor)
	if err != nil {
		return false, err
	}
	return ct.RowsAffected() > 0, nil
}

// ExistsByActor reports whether an actor identity is already taken (ACTIVE or
// REVOKED) — actor is a permanent identity, so a revoked one is never reused.
func (r *Admins) ExistsByActor(ctx context.Context, q Querier, actor string) (bool, error) {
	var exists bool
	err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM admin_credentials WHERE actor=$1)`, actor).Scan(&exists)
	return exists, err
}

// ListOperators returns every provisioned operator for the admin console.
func (r *Admins) ListOperators(ctx context.Context, q Querier) ([]Operator, error) {
	rows, err := q.Query(ctx,
		`SELECT actor, role, scope, status FROM admin_credentials ORDER BY status, actor`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Operator
	for rows.Next() {
		var o Operator
		if err := rows.Scan(&o.Actor, &o.Role, &o.Scope, &o.Status); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// NOTE: the role-UNAWARE ResolveCredential was removed in EXT-1 — it backed a
// header-authenticated parallel door to configsvc that bypassed portal RBAC
// and the M4C-F1 scope model. All operator auth now goes through
// ResolveCredentialWithRole (portalsessions.go), which carries role + scope.
//
// The role-LESS Create was removed (pre-pen-test hardening): once migration 0047
// granted tcp_app INSERT on admin_credentials, a role-omitting INSERT combined
// with the column's DEFAULT 'ADMIN' was a dead path that could mint an ACTIVE
// ADMIN with no four-eyes. Migration 0048 drops that DEFAULT, so every credential
// INSERT must now name a role explicitly (CreateWithRoleTx does).
