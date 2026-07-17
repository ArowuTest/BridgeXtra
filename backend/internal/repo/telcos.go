package repo

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
)

var ErrNotFound = errors.New("not found")

// Telcos is the global telco registry (no RLS; usecase-layer authz).
type Telcos struct{ Pool *pgxpool.Pool }

func (r *Telcos) Get(ctx context.Context, telcoID string) (entity.Telco, error) {
	var t entity.Telco
	err := r.Pool.QueryRow(ctx,
		`SELECT telco_id, name, country, status, created_at FROM telcos WHERE telco_id = $1`,
		telcoID).Scan(&t.TelcoID, &t.Name, &t.Country, &t.Status, &t.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, fmt.Errorf("telco %q: %w", telcoID, ErrNotFound)
	}
	return t, err
}

func (r *Telcos) Create(ctx context.Context, t entity.Telco) error {
	_, err := r.Pool.Exec(ctx,
		`INSERT INTO telcos (telco_id, name, country, status) VALUES ($1,$2,$3,$4)`,
		t.TelcoID, t.Name, t.Country, t.Status)
	return err
}

// ResolveCredential maps a presented API key to its telco. Only the SHA-256 of
// the key is stored or compared (V2-SEC-005). Index: telco_api_credentials_hash_uq.
func (r *Telcos) ResolveCredential(ctx context.Context, apiKey string) (telcoID string, credentialID string, err error) {
	h := sha256.Sum256([]byte(apiKey))
	err = r.Pool.QueryRow(ctx,
		`SELECT telco_id, credential_id FROM telco_api_credentials
		 WHERE key_hash = $1 AND status = 'ACTIVE'`, h[:]).Scan(&telcoID, &credentialID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", fmt.Errorf("credential: %w", ErrNotFound)
	}
	return telcoID, credentialID, err
}

func (r *Telcos) CreateCredential(ctx context.Context, credentialID, telcoID, apiKey, label string) error {
	h := sha256.Sum256([]byte(apiKey))
	_, err := r.Pool.Exec(ctx,
		`INSERT INTO telco_api_credentials (credential_id, telco_id, key_hash, label)
		 VALUES ($1,$2,$3,$4)`, credentialID, telcoID, h[:], label)
	return err
}

// Programmes is tenant-scoped: every method runs inside a tenant transaction,
// so RLS is the enforcement boundary, not this code.
type Programmes struct{}

func (Programmes) Create(ctx context.Context, tx pgx.Tx, p entity.Programme) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO programmes (programme_id, telco_id, code, name, status)
		 VALUES ($1,$2,$3,$4,$5)`,
		p.ProgrammeID, p.TelcoID, p.Code, p.Name, p.Status)
	return err
}

func (Programmes) GetByID(ctx context.Context, tx pgx.Tx, programmeID string) (entity.Programme, error) {
	var p entity.Programme
	err := tx.QueryRow(ctx,
		`SELECT programme_id, telco_id, code, name, status, created_at
		 FROM programmes WHERE programme_id = $1`, programmeID).
		Scan(&p.ProgrammeID, &p.TelcoID, &p.Code, &p.Name, &p.Status, &p.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, fmt.Errorf("programme %q: %w", programmeID, ErrNotFound)
	}
	return p, err
}

// ListForTenant returns the current tenant's programmes (RLS-scoped).
// Index: programmes (telco_id, code) unique covers the tenant scan.
func (Programmes) ListForTenant(ctx context.Context, tx pgx.Tx) ([]entity.Programme, error) {
	rows, err := tx.Query(ctx,
		`SELECT programme_id, telco_id, code, name, status, created_at
		 FROM programmes ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []entity.Programme
	for rows.Next() {
		var p entity.Programme
		if err := rows.Scan(&p.ProgrammeID, &p.TelcoID, &p.Code, &p.Name, &p.Status, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpdateStatus returns ErrNotFound when RLS hides the row (cross-tenant
// attempts surface as "not found", never as success).
func (Programmes) UpdateStatus(ctx context.Context, tx pgx.Tx, programmeID string, status entity.ProgrammeStatus) error {
	ct, err := tx.Exec(ctx,
		`UPDATE programmes SET status = $2 WHERE programme_id = $1`, programmeID, status)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("programme %q: %w", programmeID, ErrNotFound)
	}
	return nil
}
