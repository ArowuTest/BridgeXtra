package repo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
)

// ConfigVersions persists governed configuration (V2-CFG-001..007).
// All writes run in a platform transaction; reads are global.
type ConfigVersions struct{}

func (ConfigVersions) Insert(ctx context.Context, tx pgx.Tx, c entity.ConfigVersion) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO config_versions
		 (config_version_id, domain, scope, version_no, state, content, content_hash,
		  effective_from, effective_to, created_by, approved_by, reason)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NULLIF($11,''),$12)`,
		c.ConfigVersionID, c.Domain, c.Scope, c.VersionNo, c.State, c.Content, c.ContentHash,
		c.EffectiveFrom, c.EffectiveTo, c.CreatedBy, c.ApprovedBy, c.Reason)
	return err
}

func (ConfigVersions) Get(ctx context.Context, tx pgx.Tx, id string) (entity.ConfigVersion, error) {
	return scanConfig(tx.QueryRow(ctx, `
		SELECT config_version_id, domain, scope, version_no, state, content, content_hash,
		       effective_from, effective_to, created_by, COALESCE(approved_by,''), reason,
		       created_at, updated_at
		FROM config_versions WHERE config_version_id = $1`, id))
}

// NextVersionNo allocates the next version number for (domain, scope) under a
// per-key advisory xact lock so concurrent drafts cannot collide, backed by the
// UNIQUE (domain, scope, version_no) constraint either way.
func (ConfigVersions) NextVersionNo(ctx context.Context, tx pgx.Tx, domain, scope string) (int, error) {
	if _, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtext($1 || '/' || $2))`, domain, scope); err != nil {
		return 0, err
	}
	var n int
	err := tx.QueryRow(ctx,
		`SELECT COALESCE(MAX(version_no), 0) + 1 FROM config_versions WHERE domain=$1 AND scope=$2`,
		domain, scope).Scan(&n)
	return n, err
}

// TransitionState moves a version between lifecycle states with an expected-
// state guard (optimistic; V2-CFG-001). Zero rows = state raced or missing.
func (ConfigVersions) TransitionState(ctx context.Context, tx pgx.Tx, id string, from, to entity.ConfigState, approvedBy string) error {
	ct, err := tx.Exec(ctx, `
		UPDATE config_versions
		SET state = $3,
		    approved_by = CASE WHEN $4 <> '' THEN $4 ELSE approved_by END,
		    updated_at = now()
		WHERE config_version_id = $1 AND state = $2`,
		id, from, to, approvedBy)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("config %s: not in state %s: %w", id, from, ErrNotFound)
	}
	return nil
}

// Activate sets effective_from and flips APPROVED->ACTIVE, superseding the
// previously ACTIVE version in the same statement set. The EXCLUDE constraint
// (config_active_no_overlap) is the structural guarantee (V2-CFG-006).
func (ConfigVersions) Activate(ctx context.Context, tx pgx.Tx, id string, at time.Time) error {
	// Close the currently active version(s) for the same (domain, scope).
	if _, err := tx.Exec(ctx, `
		UPDATE config_versions prev
		SET state = 'SUPERSEDED', effective_to = $2, updated_at = now()
		FROM config_versions next
		WHERE next.config_version_id = $1
		  AND prev.domain = next.domain AND prev.scope = next.scope
		  AND prev.state = 'ACTIVE'`, id, at); err != nil {
		return err
	}
	ct, err := tx.Exec(ctx, `
		UPDATE config_versions
		SET state = 'ACTIVE', effective_from = $2, updated_at = now()
		WHERE config_version_id = $1 AND state = 'APPROVED'`, id, at)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("config %s: not APPROVED: %w", id, ErrNotFound)
	}
	return nil
}

// GetActiveAt returns the version whose effective window covers t for
// (domain, scope). SUPERSEDED versions remain resolvable for their historical
// window — a decision made at time t must always re-resolve to the version
// that governed it (V1-CFG-007 pinning). ROLLED_BACK versions are excluded:
// their window is disavowed. Index: config_lookup_ix.
func (ConfigVersions) GetActiveAt(ctx context.Context, tx pgx.Tx, domain, scope string, t time.Time) (entity.ConfigVersion, error) {
	return scanConfig(tx.QueryRow(ctx, `
		SELECT config_version_id, domain, scope, version_no, state, content, content_hash,
		       effective_from, effective_to, created_by, COALESCE(approved_by,''), reason,
		       created_at, updated_at
		FROM config_versions
		WHERE domain = $1 AND scope = $2 AND state IN ('ACTIVE','SUPERSEDED')
		  AND effective_from <= $3
		  AND (effective_to IS NULL OR effective_to > $3)
		ORDER BY effective_from DESC
		LIMIT 1`,
		domain, scope, t))
}

// List returns versions newest-first, optionally filtered by domain and/or
// scope (” = no filter). restrictScope (” = unrestricted) bounds the result
// to a scoped operator's authority: their own scope plus shared 'global'
// defaults (M4A-F3). Bounded by limit (caller validates).
func (ConfigVersions) List(ctx context.Context, tx pgx.Tx, domain, scope, restrictScope string, limit int) ([]entity.ConfigVersion, error) {
	rows, err := tx.Query(ctx, `
		SELECT config_version_id, domain, scope, version_no, state, content, content_hash,
		       effective_from, effective_to, created_by, COALESCE(approved_by,''), reason,
		       created_at, updated_at
		FROM config_versions
		WHERE ($1 = '' OR domain = $1) AND ($2 = '' OR scope = $2)
		  AND ($3 = '' OR scope = $3 OR scope = 'global')
		ORDER BY domain, scope, version_no DESC
		LIMIT $4`, domain, scope, restrictScope, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []entity.ConfigVersion
	for rows.Next() {
		c, err := scanConfig(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Overview aggregates one row per (domain, scope): the currently ACTIVE
// version (if any) and how many versions sit in the pre-active pipeline.
// restrictScope (” = unrestricted) bounds the rows to a scoped operator's
// authority (own scope + shared 'global'; M4A-F3). Set-based — one statement
// regardless of domain count.
func (ConfigVersions) Overview(ctx context.Context, tx pgx.Tx, restrictScope string) ([]entity.ConfigSummary, error) {
	rows, err := tx.Query(ctx, `
		SELECT domain, scope,
		       COALESCE(MAX(version_no) FILTER (WHERE state = 'ACTIVE'), 0),
		       MAX(effective_from) FILTER (WHERE state = 'ACTIVE'),
		       COUNT(*) FILTER (WHERE state IN ('DRAFT','SUBMITTED','APPROVED'))
		FROM config_versions
		WHERE ($1 = '' OR scope = $1 OR scope = 'global')
		GROUP BY domain, scope
		ORDER BY domain, scope`, restrictScope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []entity.ConfigSummary
	for rows.Next() {
		var s entity.ConfigSummary
		if err := rows.Scan(&s.Domain, &s.Scope, &s.ActiveVersionNo, &s.ActiveSince, &s.PendingCount); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func scanConfig(row pgx.Row) (entity.ConfigVersion, error) {
	var c entity.ConfigVersion
	err := row.Scan(&c.ConfigVersionID, &c.Domain, &c.Scope, &c.VersionNo, &c.State,
		&c.Content, &c.ContentHash, &c.EffectiveFrom, &c.EffectiveTo,
		&c.CreatedBy, &c.ApprovedBy, &c.Reason, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// AdvancesOneActiveIndexExists reports whether the SF-2-guarded schema backstop
// (one-active-advance partial unique index) is present. The SF-2 validator
// checks the REAL schema, not an assumption — config can never silently
// diverge from what the database enforces.
func AdvancesOneActiveIndexExists(ctx context.Context, tx pgx.Tx) (advancesTableExists, indexExists bool, err error) {
	err = tx.QueryRow(ctx, `
		SELECT
		  EXISTS (SELECT 1 FROM pg_tables  WHERE schemaname='public' AND tablename='advances'),
		  EXISTS (SELECT 1 FROM pg_indexes WHERE schemaname='public' AND tablename='advances'
		            AND indexname='advances_one_active_uq')`).
		Scan(&advancesTableExists, &indexExists)
	return
}
