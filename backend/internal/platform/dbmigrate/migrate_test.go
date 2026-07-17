package dbmigrate_test

// Regression pin for the Apply return count (found lying on a corrupted
// Windows host build cache — Linux/CI builds are authoritative). Asserts the
// full contract: fresh DB applies exactly len(Load()) migrations; an immediate
// second Apply is a no-op (idempotent re-run, from-zero lesson).

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform/dbmigrate"
	"github.com/ArowuTest/telco-credit-platform/backend/migrations"
)

func hostPort() string {
	if v := os.Getenv("TCP_TEST_HOSTPORT"); v != "" {
		return v
	}
	return "localhost:5434"
}

func TestApply_CountsExactly_AndIsIdempotent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	boot, err := platform.NewPool(ctx, fmt.Sprintf("postgres://postgres:devlocal@%s/postgres", hostPort()))
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("CI requires a database: %v", err)
		}
		t.Skipf("local postgres unavailable: %v", err)
	}
	defer boot.Close()

	const name = "telco_credit_test_migratecount"
	if _, err := boot.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
		t.Fatal(err)
	}
	if _, err := boot.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}

	pool, err := platform.NewPool(ctx, fmt.Sprintf("postgres://postgres:devlocal@%s/%s", hostPort(), name))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	ms, err := dbmigrate.Load(migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) == 0 {
		t.Fatal("embedded migration set is empty")
	}

	n1, err := dbmigrate.Apply(ctx, pool, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if n1 != len(ms) {
		t.Fatalf("fresh DB must apply all %d migrations, Apply reported %d", len(ms), n1)
	}

	n2, err := dbmigrate.Apply(ctx, pool, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if n2 != 0 {
		t.Fatalf("second Apply must be a no-op, reported %d", n2)
	}

	var recorded int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != len(ms) {
		t.Fatalf("schema_migrations has %d rows, want %d", recorded, len(ms))
	}
}
