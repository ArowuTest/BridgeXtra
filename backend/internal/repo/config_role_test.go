package repo_test

// BX-HIGH-012 Part B, Stage 1 — the tcp_config role boundary.
//
// The request-serving API (cmd/api) runs config governance + the programme->telco
// resolver on the NON-BYPASSRLS tcp_config role (migration 0076) instead of the
// BYPASSRLS worker/owner pool it used before. tcp_config must be able to do EXACTLY
// those things and NOTHING else: it must never be a back-door into tenant money/PII
// the way a bypass credential is. If the internet-facing process is compromised, an
// attacker holding this role's connection still cannot read or forge tenant data.
//
// These assertions pin that boundary directly against the real role pool. The
// resolve case is also the mutation-proof for the policy: revert
// programmes_config_resolve and this non-BYPASSRLS role sees zero programmes rows,
// so the resolve returns nothing and the assertion fails.

import (
	"context"
	"testing"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
)

func TestConfigRole_ResolvesProgrammeButCannotReachTenantData(t *testing.T) {
	db := testutil.MustSetup(t, "cfgrole_boundary")
	ctx := context.Background()
	// prg_sim_airtime01 on SIM_NG exists from the migration seed.

	// (1) MUST: resolve programme->telco — the ONLY reason the API ever needed a
	// bypass credential. Works here via the SELECT-only programmes_config_resolve
	// policy, not by bypassing RLS. Red-on-revert of the policy.
	var telco string
	if err := db.Config.QueryRow(ctx,
		`SELECT telco_id FROM programmes WHERE programme_id=$1`, "prg_sim_airtime01").Scan(&telco); err != nil {
		t.Fatalf("tcp_config must resolve programme->telco via the scoped policy: %v", err)
	}
	if telco != "SIM_NG" {
		t.Fatalf("resolve returned %q, want SIM_NG", telco)
	}

	// (2) MUST NOT: read any tenant money/PII table. tcp_config holds no grant on
	// them at all, so a compromise of the API process cannot read subscribers or the
	// ledger through it — the tenant-isolation boundary is intact even on compromise.
	if _, err := db.Config.Exec(ctx, `SELECT 1 FROM subscriber_accounts LIMIT 1`); err == nil {
		t.Fatal("tcp_config must NOT be able to read subscriber_accounts (no bypass of tenant isolation)")
	}
	if _, err := db.Config.Exec(ctx, `SELECT 1 FROM journal_entries LIMIT 1`); err == nil {
		t.Fatal("tcp_config must NOT be able to read journal_entries (ledger money)")
	}

	// (3) MUST NOT: read programme columns beyond the resolver's (programme_id,
	// telco_id). The column-scoped grant caps exposure to the mapping; programme
	// configuration/codes stay unreadable.
	if _, err := db.Config.Exec(ctx,
		`SELECT code FROM programmes WHERE programme_id=$1`, "prg_sim_airtime01"); err == nil {
		t.Fatal("tcp_config must NOT read programmes.code — the column grant limits it to (programme_id, telco_id)")
	}
}

func TestConfigRole_AuditIsPlatformScopeOnly(t *testing.T) {
	db := testutil.MustSetup(t, "cfgrole_audit")
	ctx := context.Background()

	// MUST: write a PLATFORM-scope config audit row (telco_id NULL) — the audit_tenant
	// WITH CHECK admits NULL telco even under RLS, so config-governance audit works on
	// this non-BYPASSRLS role.
	if _, err := db.Config.Exec(ctx,
		`INSERT INTO audit_events (id, telco_id, actor, action, target_type, target_id)
		 VALUES ($1, NULL, 'seed:reviewer', 'config.approved', 'config_version', 'cfg_x')`,
		"aud_cfgrole_ok"); err != nil {
		t.Fatalf("tcp_config must be able to write a platform-scope (NULL telco) audit row: %v", err)
	}

	// MUST NOT: forge a TENANT-scoped audit row. With no app.telco_id GUC set, the
	// audit_tenant WITH CHECK (telco_id = GUC OR telco_id IS NULL) rejects a non-null
	// telco — tcp_config can never write into a tenant's audit trail.
	if _, err := db.Config.Exec(ctx,
		`INSERT INTO audit_events (id, telco_id, actor, action, target_type, target_id)
		 VALUES ($1, 'SIM_NG', 'attacker', 'forge', 'x', 'y')`,
		"aud_cfgrole_forge"); err == nil {
		t.Fatal("tcp_config must NOT forge a tenant-scoped audit row (RLS WITH CHECK must reject non-null telco)")
	}
}
