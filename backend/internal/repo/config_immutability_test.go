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

// TestEXT3_ContentHash_OnlyCorrectableTowardCanonical pins the ONE transition migration 0084 added
// to this trigger, and — more importantly — pins everything it did NOT add.
//
// 0084 had to repair 38 seeded rows whose content_hash was computed from a compact SQL literal
// rather than the canonical jsonb the row actually stores (BX-MED-009). The obvious route, an
// UPDATE, is refused by this very trigger, and suspending the trigger for the repair would have
// destroyed the guarantee EXT-3 exists to give. So the trigger was NARROWED instead: content_hash
// may move only to the canonical hash of the UNCHANGED content, and to nothing else.
//
// That is only safe if an arbitrary hash is still refused — otherwise the repair would have opened
// the hole it was meant to avoid. This test is the proof, and it is the case the original EXT-3
// tests never covered: they exercised content and created_by, never content_hash.
func TestEXT3_ContentHash_OnlyCorrectableTowardCanonical(t *testing.T) {
	db := testutil.MustSetup(t, "cfg_immut_hash")
	ctx := context.Background()

	// 1. An ARBITRARY hash is still refused — the row's evidence cannot be set to a value that is
	//    not a true hash of its content. This is the anti-laundering property.
	_, err := db.Admin.Exec(ctx,
		`UPDATE config_versions SET content_hash = repeat('a', 64) WHERE config_version_id = $1`, immutableSeedID)
	if err == nil {
		t.Fatal("an arbitrary content_hash must still be refused — 0084 narrowed the trigger, it did not suspend it")
	}
	if !strings.Contains(err.Error(), "immutable after creation") {
		t.Fatalf("expected the EXT-3 trigger to fire on an arbitrary hash, got: %v", err)
	}

	// 2. Content remains absolutely write-once, so a mismatch can never be manufactured by mutating
	//    content to fit a hash. Together with (1) this is what makes (3) safe: the only reachable
	//    end state is the true hash of content that nobody can change.
	_, err = db.Admin.Exec(ctx,
		`UPDATE config_versions SET content = '{"tampered":true}'::jsonb WHERE config_version_id = $1`, immutableSeedID)
	if err == nil || !strings.Contains(err.Error(), "immutable after creation") {
		t.Fatalf("content must remain immutable after 0084, got: %v", err)
	}

	// 3. The one permitted transition: setting the hash to the canonical hash of the stored content.
	//    After 0084 every row already satisfies this, so the write is a no-op in value terms — which
	//    is the point. It must be ACCEPTED rather than refused, or the repair could not have run.
	if _, err := db.Admin.Exec(ctx,
		`UPDATE config_versions
		    SET content_hash = encode(sha256(content::text::bytea), 'hex')
		  WHERE config_version_id = $1`, immutableSeedID); err != nil {
		t.Fatalf("correcting content_hash toward the canonical hash of unchanged content must be allowed: %v", err)
	}

	// 4. And the row still verifies afterwards.
	var stored, canonical string
	if err := db.Admin.QueryRow(ctx,
		`SELECT content_hash, encode(sha256(content::text::bytea), 'hex')
		   FROM config_versions WHERE config_version_id = $1`, immutableSeedID).Scan(&stored, &canonical); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if stored != canonical {
		t.Fatalf("row does not reproduce its canonical hash after the permitted correction: %s vs %s", stored, canonical)
	}
}
