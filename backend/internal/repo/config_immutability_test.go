package repo_test

// EXT-3 / DAP-1: config_versions identity + payload are write-once, enforced
// by TWO independent layers. These tests attempt the forbidden mutation
// through each and assert it fails — and prove a legitimate lifecycle update
// still succeeds (the lockdown is scoped, not a blanket freeze).

import (
	"context"
	"strings"
	"testing"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
)

// a seeded ACTIVE config that exists after migrations.
const immutableSeedID = "cfg_seed_outbox_v1"

func TestEXT3_ConfigContent_ImmutableViaGrant(t *testing.T) {
	db := testutil.MustSetup(t, "cfg_immut_grant")
	ctx := context.Background()

	// tcp_worker (the config write role) must NOT be able to touch content —
	// the column is outside its scoped UPDATE grant.
	_, err := db.Worker.Exec(ctx,
		`UPDATE config_versions SET content = '{}'::jsonb WHERE config_version_id = $1`, immutableSeedID)
	if err == nil {
		t.Fatal("tcp_worker must not be able to UPDATE config_versions.content (grant lockdown)")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "permission denied") {
		t.Fatalf("expected a permission-denied grant error, got: %v", err)
	}

	// The lifecycle columns it DOES need still work (scoped, not frozen).
	if _, err := db.Worker.Exec(ctx,
		`UPDATE config_versions SET updated_at = now() WHERE config_version_id = $1`, immutableSeedID); err != nil {
		t.Fatalf("tcp_worker must still update lifecycle columns: %v", err)
	}
}

func TestEXT3_ConfigContent_ImmutableViaTrigger(t *testing.T) {
	db := testutil.MustSetup(t, "cfg_immut_trigger")
	ctx := context.Background()

	// Even the table OWNER (bypasses grants) cannot rewrite the payload — the
	// trigger is the backstop that holds regardless of privilege.
	_, err := db.Admin.Exec(ctx,
		`UPDATE config_versions SET content = '{"x":1}'::jsonb WHERE config_version_id = $1`, immutableSeedID)
	if err == nil {
		t.Fatal("owner UPDATE of config_versions.content must be blocked by the immutability trigger")
	}
	if !strings.Contains(err.Error(), "immutable after creation") {
		t.Fatalf("expected the immutability trigger to fire, got: %v", err)
	}

	// Same for the maker identity (maker-checker integrity).
	_, err = db.Admin.Exec(ctx,
		`UPDATE config_versions SET created_by = 'attacker' WHERE config_version_id = $1`, immutableSeedID)
	if err == nil || !strings.Contains(err.Error(), "immutable after creation") {
		t.Fatalf("created_by must be immutable via trigger, got: %v", err)
	}

	// A legitimate lifecycle transition (state) is still allowed for the owner.
	if _, err := db.Admin.Exec(ctx,
		`UPDATE config_versions SET updated_at = now() WHERE config_version_id = $1`, immutableSeedID); err != nil {
		t.Fatalf("lifecycle update must still succeed: %v", err)
	}
}
