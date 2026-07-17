// Package repo is the ONLY layer that contains SQL (ADR-0001 data-access rule).
// Every query here is enumerable for index review; new queries require an index
// review in PR (BUILD_PLAN §3).
package repo

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
)

// WithTenantTx opens a transaction bound to the authenticated tenant in ctx:
// it stamps app.telco_id into the transaction (SET LOCAL), which drives every
// RLS policy. Missing tenant context is an error — never a cross-tenant view
// (fail closed; V2-TEN-002, zero-config-floor).
func WithTenantTx(ctx context.Context, pool *pgxpool.Pool, fn func(tx pgx.Tx) error) error {
	telcoID, err := platform.TenantFrom(ctx)
	if err != nil {
		return err
	}
	return withTx(ctx, pool, telcoID, fn)
}

// WithExplicitTenantTx is for boundary code (middleware, workers acting FOR a
// named tenant) that has authenticated the tenant itself.
func WithExplicitTenantTx(ctx context.Context, pool *pgxpool.Pool, telcoID string, fn func(tx pgx.Tx) error) error {
	if telcoID == "" {
		return fmt.Errorf("empty telco_id: refusing cross-tenant transaction")
	}
	return withTx(ctx, pool, telcoID, fn)
}

func withTx(ctx context.Context, pool *pgxpool.Pool, telcoID string, fn func(tx pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	// SET LOCAL with a parameter requires set_config; scoped to this txn only.
	if _, err := tx.Exec(ctx, "SELECT set_config('app.telco_id', $1, true)", telcoID); err != nil {
		return fmt.Errorf("set tenant context: %w", err)
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// WithPlatformTx runs WITHOUT tenant context — for global tables (telcos,
// config) and BYPASSRLS worker paths only. Deliberately named so misuse is
// visible in review.
func WithPlatformTx(ctx context.Context, pool *pgxpool.Pool, fn func(tx pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
