package dbmigrate_test

// BX-MED-009 production-incident hardening (reviewer-requested, on e2d0ee7): migration 0084's
// incident-scoped repair (recon...no, config_versions repair) for cfg_01KXRQPASM83ZF743XP862YF81
// must behave as a SCALPEL bound to the exact investigated snapshot — identity, content, AND the
// exact pre-repair hash — never a generic "any row with this ID" bypass. The existing
// TestBXMED009_EverySeededConfigReproducesItsCanonicalHash cannot exercise this: it starts from a
// fresh database via testutil.MustSetup, where the historical production row never existed.
//
// These three cases apply migrations 0001-0083 for real (read from the actual embedded
// migrations.FS — never a hand-duplicated copy that could drift), seed a synthetic fixture row that
// stands in for "the production database's state right before 0084 ran", then apply 0084 alone and
// observe the outcome:
//
//   1. the row matches the incident snapshot EXACTLY (same identity, content, and pre-repair hash the
//      investigation captured) -> 0084's repair fires, the row's hash becomes canonical, the
//      migration succeeds and is recorded;
//   2. same config_version_id, but content/metadata differs from what was actually investigated ->
//      the repair's pinned predicates do not match, so it touches zero rows, and the PRE-EXISTING
//      generic application-authored-mismatch guard (never weakened) aborts the whole migration —
//      nothing is recorded, nothing is silently laundered;
//   3. a completely different application-authored row with an unrelated mismatch -> the generic
//      guard refuses it too, proving the incident-scoped repair was never broadened to cover
//      anything beyond the one investigated row.

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform/dbmigrate"
	"github.com/ArowuTest/telco-credit-platform/backend/migrations"
)

// The exact investigated production snapshot (see project incident record / migration 0084's own
// header comment). Any test row that does not match ALL of these, byte for byte, must NOT be
// touched by the incident repair.
const (
	med009IncidentID        = "cfg_01KXRQPASM83ZF743XP862YF81"
	med009IncidentDomain    = "telco.adapter"
	med009IncidentScope     = "telco:SIM_NG"
	med009IncidentCreatedBy = "admin:owner-a"
	med009IncidentCreatedAt = "2026-07-17 19:08:45.237223+00"
	med009IncidentContent   = `{"retry_budget": 0, "fulfilment_url": "https://bridgextra-sim.onrender.com", "request_timeout_ms": 8000, "circuit_min_requests": 20, "circuit_error_threshold_pct": 50}`
	med009IncidentStaleHash = "7c4b640364177b56872292c0f3adf93f3d1323979192ad9e03b777d0b082ee6f"
)

// med009SplitMigrations reads the REAL embedded migration set and splits it into "everything before
// version 84" and "version 84 alone", each as its own in-memory fs.FS — so both halves are applied
// through the exact same dbmigrate.Apply path production uses, never a hand-simulated substitute.
func med009SplitMigrations(t *testing.T) (before84, only84 fstest.MapFS) {
	t.Helper()
	ms, err := dbmigrate.Load(migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	before84 = fstest.MapFS{}
	only84 = fstest.MapFS{}
	for _, m := range ms {
		switch {
		case m.Version < 84:
			before84[m.Name] = &fstest.MapFile{Data: []byte(m.SQL)}
		case m.Version == 84:
			only84[m.Name] = &fstest.MapFile{Data: []byte(m.SQL)}
		}
	}
	if len(before84) == 0 {
		t.Fatal("no migrations found before version 84 — Load()/embed wiring broken")
	}
	if len(only84) != 1 {
		t.Fatalf("expected exactly 1 migration at version 84, found %d", len(only84))
	}
	return before84, only84
}

// med009InsertFixture inserts a synthetic config_versions row shaped like a real
// application-authored write, with an EXPLICITLY WRONG content_hash. The immutability trigger
// (migration 0019) only fires on UPDATE, so a plain INSERT with a deliberately mismatched hash is
// not blocked — this is how each test constructs the "state right before 0084 ran" it needs.
//
// versionNo must not collide with whatever migrations 1-83 already seeded for (domain, scope) —
// config_versions has a UNIQUE(domain, scope, version_no) constraint. telco.adapter/telco:SIM_NG
// (the real incident's domain/scope) is seeded through version_no 3 by 0003/0010/0033, so the
// incident-shaped fixtures below use 4 — matching the real row's position as the first
// admin-authored version after the seeded chain.
//
// Whatever row is currently ACTIVE for (domain, scope) is first SUPERSEDED, the same data-driven
// supersede pattern migrations like 0033 use — config_active_no_overlap is a genuine EXCLUDE
// constraint (only one ACTIVE row per domain/scope may have an open-ended effective range), and a
// real admin write would have gone through the same supersede step to land as ACTIVE at all.
func med009InsertFixture(t *testing.T, ctx context.Context, dsn, id, domain, scope, createdBy, createdAt, content, staleHash string, versionNo int) {
	t.Helper()
	pool, err := platform.NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("connect for fixture insert: %v", err)
	}
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin fixture tx: %v", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if _, err := tx.Exec(ctx, `
		UPDATE config_versions
		   SET state = 'SUPERSEDED', effective_to = $3::timestamptz
		 WHERE domain = $1 AND scope = $2 AND state = 'ACTIVE'`,
		domain, scope, createdAt); err != nil {
		t.Fatalf("supersede prior active row for %s/%s: %v", domain, scope, err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO config_versions
		  (config_version_id, domain, scope, version_no, state, content, content_hash,
		   effective_from, created_by, approved_by, reason, created_at, updated_at)
		VALUES
		  ($1, $2, $3, $8, 'ACTIVE', $4::jsonb, $5,
		   $6::timestamptz, $7, 'admin:owner-b', 'BX-MED-009 incident hardening test fixture',
		   $6::timestamptz, $6::timestamptz)`,
		id, domain, scope, content, staleHash, createdAt, createdBy, versionNo); err != nil {
		t.Fatalf("insert fixture row %s: %v", id, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit fixture tx: %v", err)
	}
}

func med009FreshDB(t *testing.T, suffix string) string {
	t.Helper()
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
	name := "telco_credit_test_med009inc_" + suffix
	if _, err := boot.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
		t.Fatal(err)
	}
	if _, err := boot.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dcancel()
		drop, err := platform.NewPool(dctx, fmt.Sprintf("postgres://postgres:devlocal@%s/postgres", hostPort()))
		if err != nil {
			return
		}
		defer drop.Close()
		_, _ = drop.Exec(dctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	})
	return fmt.Sprintf("postgres://postgres:devlocal@%s/%s", hostPort(), name)
}

func med009ContentHash(t *testing.T, ctx context.Context, dsn, id string) string {
	t.Helper()
	pool, err := platform.NewPool(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var hash string
	if err := pool.QueryRow(ctx, `SELECT content_hash FROM config_versions WHERE config_version_id=$1`, id).
		Scan(&hash); err != nil {
		t.Fatalf("read content_hash for %s: %v", id, err)
	}
	return hash
}

func med009SchemaMigrationsHas84(t *testing.T, ctx context.Context, dsn string) bool {
	t.Helper()
	pool, err := platform.NewPool(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations WHERE version=84`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

// Case 1: the exact investigated snapshot must be repaired and the migration must succeed.
func TestBXMED009Incident_ExactSnapshotIsRepaired(t *testing.T) {
	dsn := med009FreshDB(t, "exact")
	ctx := context.Background()

	before84, only84 := med009SplitMigrations(t)
	pool, err := platform.NewPool(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dbmigrate.Apply(ctx, pool, before84); err != nil {
		pool.Close()
		t.Fatalf("apply migrations before 0084: %v", err)
	}
	pool.Close()

	med009InsertFixture(t, ctx, dsn, med009IncidentID, med009IncidentDomain, med009IncidentScope,
		med009IncidentCreatedBy, med009IncidentCreatedAt, med009IncidentContent, med009IncidentStaleHash, 4)

	pool2, err := platform.NewPool(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool2.Close()
	n, err := dbmigrate.Apply(ctx, pool2, only84)
	if err != nil {
		t.Fatalf("0084 must succeed against the EXACT investigated snapshot: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 migration applied, got %d", n)
	}

	got := med009ContentHash(t, ctx, dsn, med009IncidentID)
	if got == med009IncidentStaleHash {
		t.Fatal("content_hash was not repaired — still the stale pre-incident value")
	}
	if !med009SchemaMigrationsHas84(t, ctx, dsn) {
		t.Fatal("migration 0084 must be recorded in schema_migrations after a successful apply")
	}
}

// Case 2: same config_version_id, but content diverges from the investigated snapshot — the
// incident repair must not touch it, and the generic guard must abort the whole migration.
func TestBXMED009Incident_SameIDDifferentSnapshotIsRefused(t *testing.T) {
	dsn := med009FreshDB(t, "altered")
	ctx := context.Background()

	before84, only84 := med009SplitMigrations(t)
	pool, err := platform.NewPool(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dbmigrate.Apply(ctx, pool, before84); err != nil {
		pool.Close()
		t.Fatalf("apply migrations before 0084: %v", err)
	}
	pool.Close()

	// Same primary key, but a MATERIALLY DIFFERENT content than what was actually investigated —
	// exactly the case the exact-snapshot pinning exists to catch.
	alteredContent := `{"retry_budget": 3, "fulfilment_url": "https://attacker.example/adapter", "request_timeout_ms": 8000, "circuit_min_requests": 20, "circuit_error_threshold_pct": 50}`
	med009InsertFixture(t, ctx, dsn, med009IncidentID, med009IncidentDomain, med009IncidentScope,
		med009IncidentCreatedBy, med009IncidentCreatedAt, alteredContent, med009IncidentStaleHash, 4)

	pool2, err := platform.NewPool(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool2.Close()
	_, err = dbmigrate.Apply(ctx, pool2, only84)
	if err == nil {
		t.Fatal("0084 must REFUSE a row sharing the incident's config_version_id but not its exact " +
			"investigated content — the repair must not launder an unexpected mismatch")
	}
	if !strings.Contains(err.Error(), "BX-MED-009") {
		t.Fatalf("expected the generic BX-MED-009 guard to fire, got: %v", err)
	}

	got := med009ContentHash(t, ctx, dsn, med009IncidentID)
	if got != med009IncidentStaleHash {
		t.Fatalf("content_hash must be UNCHANGED after a refused migration, got %s want %s (the stale "+
			"original)", got, med009IncidentStaleHash)
	}
	if med009SchemaMigrationsHas84(t, ctx, dsn) {
		t.Fatal("migration 0084 must NOT be recorded in schema_migrations after a refused apply")
	}
}

// Case 3: an entirely different application-authored row with an unrelated mismatch — the generic
// guard still refuses, proving the incident repair was never broadened beyond the one row.
func TestBXMED009Incident_UnrelatedMismatchIsRefused(t *testing.T) {
	dsn := med009FreshDB(t, "unrelated")
	ctx := context.Background()

	before84, only84 := med009SplitMigrations(t)
	pool, err := platform.NewPool(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dbmigrate.Apply(ctx, pool, before84); err != nil {
		pool.Close()
		t.Fatalf("apply migrations before 0084: %v", err)
	}
	pool.Close()

	const unrelatedID = "cfg_med009_incident_test_unrelated"
	med009InsertFixture(t, ctx, dsn, unrelatedID, "test.med009incident.unrelated", "telco:SIM_NG",
		"admin:owner-c", "2026-07-20 10:00:00+00", `{"unrelated":true}`, "0000000000000000000000000000000000000000000000000000000000000000", 1)

	pool2, err := platform.NewPool(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool2.Close()
	_, err = dbmigrate.Apply(ctx, pool2, only84)
	if err == nil {
		t.Fatal("0084 must refuse an unrelated application-authored mismatch — the incident repair " +
			"must never have been broadened to cover it")
	}
	if !strings.Contains(err.Error(), "BX-MED-009") {
		t.Fatalf("expected the generic BX-MED-009 guard to fire, got: %v", err)
	}
	if med009SchemaMigrationsHas84(t, ctx, dsn) {
		t.Fatal("migration 0084 must NOT be recorded in schema_migrations after a refused apply")
	}
}
