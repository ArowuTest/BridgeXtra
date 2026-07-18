package repo

import (
	"context"
	"crypto/sha256"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Admins resolves platform-administrator credentials — the actor identity
// behind maker-checker on the admin API. Hash-only storage (V2-SEC-005).
type Admins struct{ Pool *pgxpool.Pool }

// NOTE: the role-UNAWARE ResolveCredential was removed in EXT-1 — it backed a
// header-authenticated parallel door to configsvc that bypassed portal RBAC
// and the M4C-F1 scope model. All operator auth now goes through
// ResolveCredentialWithRole (portalsessions.go), which carries role + scope.

// Create provisions an admin credential (platform-admin bootstrap path; the
// M4 portal will manage rotation over this same table).
func (r *Admins) Create(ctx context.Context, adminID, actor, apiKey string) error {
	h := sha256.Sum256([]byte(apiKey))
	_, err := r.Pool.Exec(ctx,
		`INSERT INTO admin_credentials (admin_id, actor, key_hash) VALUES ($1,$2,$3)`,
		adminID, actor, h[:])
	return err
}
