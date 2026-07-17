package dbmigrate_test

// Managed-Postgres simulation (Render class): the database owner is a
// NON-superuser with CREATEROLE — exactly the privilege shape the deployment
// probe measured on Render (CREATE ROLE: OK, BYPASSRLS: DENIED). The full
// migration set must apply from zero under that identity: 0001's tcp_worker
// creation raises insufficient_privilege (Postgres checks the BYPASSRLS
// privilege BEFORE the duplicate-name check, so this fires whether or not the
// role already exists) and must take the documented fallback instead of
// failing the migration. Password rotation must also work as that owner.
//
// The shared tcp_app/tcp_worker roles are deliberately NOT dropped: they hold
// grants in every migrated database (including CI's), so DROP ROLE cannot
// succeed anywhere realistic — and the fallback branch executes regardless.

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform/dbmigrate"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform/dbroles"
	"github.com/ArowuTest/telco-credit-platform/backend/migrations"
)

func TestApply_AsManagedNonSuperuserOwner(t *testing.T) {
	// Serial-only: password rotation below touches the cluster-shared roles,
	// which would race parallel packages' authentication. CI runs this as a
	// dedicated step after the main suite; locally set the flag explicitly.
	if os.Getenv("TCP_TEST_MANAGED_OWNER") == "" {
		t.Skip("set TCP_TEST_MANAGED_OWNER=1 to run (rotates shared role passwords; serial-only)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	boot, err := platform.NewPool(ctx, fmt.Sprintf("postgres://postgres:devlocal@%s/postgres", hostPort()))
	if err != nil {
		t.Skipf("local postgres unavailable: %v", err)
	}
	defer boot.Close()

	const name = "telco_credit_test_managedowner"
	if _, err := boot.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		"DROP ROLE IF EXISTS mgd_owner",
		"CREATE ROLE mgd_owner LOGIN PASSWORD 'mgd_owner_pwd' NOSUPERUSER NOCREATEDB CREATEROLE",
		// On a real managed cluster the owner CREATES tcp_app/tcp_worker and
		// so holds ADMIN on them (PG16 creator-gets-admin). Locally the
		// superuser created them earlier; grant the same ADMIN so password
		// rotation runs under the same authority Render's owner has.
		"GRANT tcp_app TO mgd_owner WITH ADMIN OPTION",
		"GRANT tcp_worker TO mgd_owner WITH ADMIN OPTION",
	} {
		if _, err := boot.Exec(ctx, stmt); err != nil {
			t.Fatalf("shape owner role (%s): %v", stmt, err)
		}
	}
	if _, err := boot.Exec(ctx, "CREATE DATABASE "+name+" OWNER mgd_owner"); err != nil {
		t.Fatal(err)
	}

	pool, err := platform.NewPool(ctx, fmt.Sprintf("postgres://mgd_owner:mgd_owner_pwd@%s/%s", hostPort(), name))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	ms, err := dbmigrate.Load(migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	n, err := dbmigrate.Apply(ctx, pool, migrations.FS)
	if err != nil {
		t.Fatalf("full migration set must apply as a managed non-superuser owner: %v", err)
	}
	if n != len(ms) {
		t.Fatalf("applied %d migrations, want %d", n, len(ms))
	}

	// Sanity: the fallback still ends with a usable tcp_worker role and the
	// grants resolved (SELECT on outbox is granted to tcp_worker in 0001;
	// has_table_privilege proves the GRANT statements ran to completion).
	var canSelect bool
	if err := pool.QueryRow(ctx,
		"SELECT has_table_privilege('tcp_worker', 'outbox', 'SELECT')").Scan(&canSelect); err != nil {
		t.Fatal(err)
	}
	if !canSelect {
		t.Fatal("grants after the role fallback must still apply to tcp_worker")
	}

	// Password rotation as the managed owner (CREATEROLE suffices).
	t.Setenv("TCP_APP_PASSWORD", "rotated_app_pwd_1")
	t.Setenv("TCP_WORKER_PASSWORD", "rotated_worker_pwd_1")
	rotated, err := dbroles.ApplyPasswords(ctx, pool)
	if err != nil {
		t.Fatalf("password rotation as managed owner: %v", err)
	}
	if len(rotated) != 2 {
		t.Fatalf("expected both roles rotated, got %v", rotated)
	}
	appPool, err := platform.NewPool(ctx, fmt.Sprintf("postgres://tcp_app:rotated_app_pwd_1@%s/%s", hostPort(), name))
	if err != nil {
		t.Fatalf("tcp_app must authenticate with the rotated password: %v", err)
	}
	appPool.Close()

	// Restore the standard dev passwords for any later local runs.
	if _, err := boot.Exec(ctx, "ALTER ROLE tcp_app WITH PASSWORD 'devlocal_app'"); err != nil {
		t.Fatal(err)
	}
	if _, err := boot.Exec(ctx, "ALTER ROLE tcp_worker WITH PASSWORD 'devlocal_worker'"); err != nil {
		t.Fatal(err)
	}
}
