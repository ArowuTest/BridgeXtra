package repo

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Admins resolves platform-administrator credentials — the actor identity
// behind maker-checker on the admin API. Hash-only storage (V2-SEC-005).
type Admins struct{ Pool *pgxpool.Pool }

// ResolveCredential maps a presented admin key to its stable actor identity.
// Index: admin_credentials_hash_uq.
func (r *Admins) ResolveCredential(ctx context.Context, apiKey string) (actor string, err error) {
	h := sha256.Sum256([]byte(apiKey))
	err = r.Pool.QueryRow(ctx,
		`SELECT actor FROM admin_credentials WHERE key_hash = $1 AND status = 'ACTIVE'`,
		h[:]).Scan(&actor)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("admin credential: %w", ErrNotFound)
	}
	return actor, err
}

// Create provisions an admin credential (platform-admin bootstrap path; the
// M4 portal will manage rotation over this same table).
func (r *Admins) Create(ctx context.Context, adminID, actor, apiKey string) error {
	h := sha256.Sum256([]byte(apiKey))
	_, err := r.Pool.Exec(ctx,
		`INSERT INTO admin_credentials (admin_id, actor, key_hash) VALUES ($1,$2,$3)`,
		adminID, actor, h[:])
	return err
}
