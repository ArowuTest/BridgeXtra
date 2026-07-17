// Package testutil provisions an isolated, freshly-migrated database per test
// package (fresh-DB validation on every run — reference_migration_from_zero_lesson)
// and hands back pools for each database role so tests exercise REAL privilege
// boundaries: admin (owner), tcp_app (RLS-enforced), tcp_worker (BYPASSRLS).
// RLS tests are meaningless through a superuser pool.
package testutil

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform/dbmigrate"
	"github.com/ArowuTest/telco-credit-platform/backend/migrations"
)

const (
	defaultHostPort = "localhost:5434" // A-14: telco-credit-postgres
	adminUser       = "postgres"
	adminPass       = "devlocal"
)

var dbNameRe = regexp.MustCompile(`^[a-z0-9_]+$`)

type DB struct {
	Name   string
	Admin  *pgxpool.Pool // owner: migrations, seeds, cross-tenant assertions
	App    *pgxpool.Pool // tcp_app: RLS enforced — the pool business code uses
	Worker *pgxpool.Pool // tcp_worker: BYPASSRLS dispatcher
}

func hostPort() string {
	if v := os.Getenv("TCP_TEST_HOSTPORT"); v != "" {
		return v
	}
	return defaultHostPort
}

func dsn(user, pass, db string) string {
	return fmt.Sprintf("postgres://%s:%s@%s/%s", user, pass, hostPort(), db)
}

// MustSetup creates (or resets) an isolated database named telco_credit_test_<suffix>,
// applies all migrations from zero, and returns role pools. Skips when the local
// database is unreachable UNLESS running in CI (tests must not silently pass green
// without a database in CI — that would be a false-green pipeline).
func MustSetup(t *testing.T, suffix string) *DB {
	t.Helper()
	if !dbNameRe.MatchString(suffix) {
		t.Fatalf("bad db suffix %q", suffix)
	}
	name := "telco_credit_test_" + suffix
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	boot, err := platform.NewPool(ctx, dsn(adminUser, adminPass, "postgres"))
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("CI requires a database: %v", err)
		}
		t.Skipf("local postgres unavailable on %s: %v", hostPort(), err)
	}
	defer boot.Close()

	// Force-drop and recreate: every test run proves the migrations from zero.
	if _, err := boot.Exec(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, name)); err != nil {
		t.Fatalf("drop test db: %v", err)
	}
	if _, err := boot.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %s`, name)); err != nil {
		t.Fatalf("create test db: %v", err)
	}

	admin, err := platform.NewPool(ctx, dsn(adminUser, adminPass, name))
	if err != nil {
		t.Fatalf("admin pool: %v", err)
	}
	if _, err := dbmigrate.Apply(ctx, admin, migrations.FS); err != nil {
		admin.Close()
		t.Fatalf("apply migrations from zero: %v", err)
	}

	app, err := platform.NewPool(ctx, dsn("tcp_app", "devlocal_app", name))
	if err != nil {
		admin.Close()
		t.Fatalf("app pool: %v", err)
	}
	worker, err := platform.NewPool(ctx, dsn("tcp_worker", "devlocal_worker", name))
	if err != nil {
		admin.Close()
		app.Close()
		t.Fatalf("worker pool: %v", err)
	}

	db := &DB{Name: name, Admin: admin, App: app, Worker: worker}
	t.Cleanup(func() {
		app.Close()
		worker.Close()
		admin.Close()
	})
	return db
}

// SeedTelco inserts a telco and an API credential (admin pool: telco registry
// writes are a platform-admin operation).
func (d *DB) SeedTelco(t *testing.T, telcoID, apiKey string) {
	t.Helper()
	ctx := context.Background()
	if _, err := d.Admin.Exec(ctx,
		`INSERT INTO telcos (telco_id, name, country, status) VALUES ($1,$1,'NG','ACTIVE')
		 ON CONFLICT (telco_id) DO NOTHING`, telcoID); err != nil {
		t.Fatalf("seed telco: %v", err)
	}
	if apiKey != "" {
		if _, err := d.Admin.Exec(ctx,
			`INSERT INTO telco_api_credentials (credential_id, telco_id, key_hash, label)
			 VALUES ($1, $2, sha256($3::bytea), 'test')`,
			"cred_"+telcoID, telcoID, apiKey); err != nil {
			t.Fatalf("seed credential: %v", err)
		}
	}
}
