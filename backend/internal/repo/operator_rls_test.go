package repo_test

// Gate B #1 (DB role separation) — adversarial proof that the read-only
// tcp_operator role is DB-enforced, not app-enforced. These run AS tcp_operator
// (RLS applies; it does not bypass) and prove the fail-closed and forge-the-scope
// properties the design rests on. subscriber_accounts is the exemplar tenant
// table (plain telco_id policy); the same policy shape covers the other read
// tables.

import (
	"context"
	"testing"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
)

// seedTwoTenants inserts a subscriber for SIM_NG and for a second telco OTHER_NG
// (via the owner pool, bypassing RLS) so a cross-tenant leak would be visible.
func seedTwoTenants(t *testing.T, db *testutil.DB) {
	t.Helper()
	ctx := context.Background()
	stmts := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO telcos (telco_id, name, country, status) VALUES ('OTHER_NG','Other','NG','ACTIVE') ON CONFLICT DO NOTHING`, nil},
		{`INSERT INTO subscriber_accounts (subscriber_account_id, telco_id, msisdn_token, status)
		  VALUES ('sub_sim','SIM_NG','tok_sim','ACTIVE')`, nil},
		{`INSERT INTO subscriber_accounts (subscriber_account_id, telco_id, msisdn_token, status)
		  VALUES ('sub_other','OTHER_NG','tok_other','ACTIVE')`, nil},
	}
	for _, s := range stmts {
		if _, err := db.Admin.Exec(ctx, s.sql, s.args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
}

// opSees reports whether tcp_operator can see a specific subscriber row inside a
// tx after applying the given per-tx GUCs (set_config is_local=true == SET
// LOCAL). Asserting on a specific row is robust to fixture-seeded rows and is the
// direct cross-tenant-leak indicator (can a SIM_NG operator see the OTHER_NG row?).
// The query intentionally has NO telco WHERE clause — the DB is the boundary.
func opSees(t *testing.T, db *testutil.DB, gucs map[string]string, subID string) bool {
	t.Helper()
	ctx := context.Background()
	tx, err := db.Operator.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for k, v := range gucs {
		if _, err := tx.Exec(ctx, `SELECT set_config($1,$2,true)`, k, v); err != nil {
			t.Fatalf("set_config %s: %v", k, err)
		}
	}
	var seen bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM subscriber_accounts WHERE subscriber_account_id=$1)`, subID).Scan(&seen); err != nil {
		t.Fatalf("operator query: %v", err)
	}
	return seen
}

func TestOperatorRLS_FailClosedAndForgeScope(t *testing.T) {
	db := testutil.MustSetup(t, "oprls")
	seedTwoTenants(t, db)

	// Fail-closed: NO app.telco_id (a read that forgot the wrapper) -> sees nothing.
	// A BYPASSRLS role would see both; tcp_operator sees neither.
	if opSees(t, db, nil, "sub_sim") || opSees(t, db, nil, "sub_other") {
		t.Fatal("with no scope set, the operator must see nothing (fail-closed)")
	}

	// Scoped to SIM_NG, with NO telco WHERE clause: the DB still yields the SIM_NG
	// row and NEVER the OTHER_NG row. Dropping/forging the app filter cannot leak.
	simScope := map[string]string{"app.telco_id": "SIM_NG"}
	if !opSees(t, db, simScope, "sub_sim") {
		t.Fatal("a SIM_NG operator must see its own telco's row")
	}
	if opSees(t, db, simScope, "sub_other") {
		t.Fatal("a SIM_NG operator must NEVER see an OTHER_NG row (DB-enforced, not app-enforced)")
	}

	// op_all unset falls through to the telco filter (still scoped, no leak).
	if opSees(t, db, map[string]string{"app.telco_id": "SIM_NG", "app.op_all": ""}, "sub_other") {
		t.Fatal("op_all unset must fall through to the telco filter (no cross-tenant)")
	}

	// The '*' admin path: op_all='true' reads across telcos (app-gated global path).
	if !opSees(t, db, map[string]string{"app.op_all": "true"}, "sub_other") {
		t.Fatal("op_all='true' must read the whole estate")
	}
}

// SET LOCAL (not SET): two sequential txns on the same pool with different telcos
// are each isolated — no session-level GUC bleeds from one to the next.
func TestOperatorRLS_SetLocalIsolationAcrossTxns(t *testing.T) {
	db := testutil.MustSetup(t, "oplocal")
	seedTwoTenants(t, db)

	// tx1 scoped to SIM_NG: sees sub_sim, not sub_other.
	if !opSees(t, db, map[string]string{"app.telco_id": "SIM_NG"}, "sub_sim") ||
		opSees(t, db, map[string]string{"app.telco_id": "SIM_NG"}, "sub_other") {
		t.Fatal("tx scoped to SIM_NG must see sub_sim and not sub_other")
	}
	// tx2 scoped to OTHER_NG: sees sub_other, not sub_sim — the SIM_NG GUC from a
	// prior tx did not persist on the pooled connection.
	if !opSees(t, db, map[string]string{"app.telco_id": "OTHER_NG"}, "sub_other") ||
		opSees(t, db, map[string]string{"app.telco_id": "OTHER_NG"}, "sub_sim") {
		t.Fatal("tx scoped to OTHER_NG must see sub_other and not sub_sim (SET LOCAL, not SET)")
	}
	// A following un-scoped tx sees nothing — no session-level GUC leaked.
	if opSees(t, db, nil, "sub_sim") || opSees(t, db, nil, "sub_other") {
		t.Fatal("a later un-scoped tx must see nothing (GUC did not leak across txns)")
	}
}

// tcp_operator is SELECT-only: it can never mutate money, even within scope.
func TestOperatorRLS_ReadOnly(t *testing.T) {
	db := testutil.MustSetup(t, "opro")
	seedTwoTenants(t, db)
	ctx := context.Background()

	err := func() error {
		tx, err := db.Operator.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `SELECT set_config('app.telco_id','SIM_NG',true)`); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE subscriber_accounts SET status='BARRED' WHERE subscriber_account_id='sub_sim'`)
		return err
	}()
	if err == nil {
		t.Fatal("tcp_operator must not be able to UPDATE (SELECT-only)")
	}
	// A non-nil error here is the grant refusing the write (permission denied).
}

// opRowExists reports whether tcp_operator can see the row matched by existsSQL after
// applying the given per-tx GUCs (SET LOCAL). Generic sibling of opSees — used to prove the
// 0067 op_all policies (programmes, funding_pools) fail closed for a telco-scoped operator.
func opRowExists(t *testing.T, db *testutil.DB, gucs map[string]string, existsSQL string, arg string) bool {
	t.Helper()
	ctx := context.Background()
	tx, err := db.Operator.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for k, v := range gucs {
		if _, err := tx.Exec(ctx, `SELECT set_config($1,$2,true)`, k, v); err != nil {
			t.Fatalf("set_config %s: %v", k, err)
		}
	}
	var seen bool
	if err := tx.QueryRow(ctx, existsSQL, arg).Scan(&seen); err != nil {
		t.Fatalf("operator query: %v", err)
	}
	return seen
}

// The one thing 0067's op_all policies exist to protect: telco-A cannot see telco-B's
// programmes or funding pools. Both tables carry a role-agnostic telco RLS policy
// (programmes_tenant 0001, t_funding 0004); op_all is the ONLY cross-telco path and fires
// only for the '*' admin. This test seeds an OTHER_NG programme + pool alongside the migrated
// SIM_NG ones and drives all three scopes on the real operator pool. It is the mutation
// canary for 0067: change either op_all policy's USING to `true` and a telco-scoped operator
// starts seeing OTHER_NG — turning the "must NEVER see" assertions red.
func TestOperatorRLS_ProgrammesAndPools_CrossTelcoIsolation(t *testing.T) {
	db := testutil.MustSetup(t, "opprogpool")
	ctx := context.Background()
	// prg_sim_airtime01 + pool_sim_01 (SIM_NG) already exist from the migration seed; add an
	// OTHER_NG programme + pool so a cross-tenant leak would be visible.
	for _, s := range []string{
		`INSERT INTO telcos (telco_id, name, country, status) VALUES ('OTHER_NG','Other','NG','ACTIVE') ON CONFLICT DO NOTHING`,
		`INSERT INTO programmes (programme_id, telco_id, code, name, status)
		   VALUES ('prg_other','OTHER_NG','OTHER_AIRTIME','Other Airtime','ACTIVE')`,
		`INSERT INTO funding_pools (pool_id, programme_id, telco_id, currency, committed_minor)
		   VALUES ('pool_other','prg_other','OTHER_NG','NGN',1000000)`,
	} {
		if _, err := db.Admin.Exec(ctx, s); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	const progExists = `SELECT EXISTS (SELECT 1 FROM programmes WHERE programme_id=$1)`
	const poolExists = `SELECT EXISTS (SELECT 1 FROM funding_pools WHERE pool_id=$1)`
	sim := map[string]string{"app.telco_id": "SIM_NG"}
	allEstate := map[string]string{"app.op_all": "true"}

	// A SIM_NG operator sees its own programme, NEVER OTHER_NG's (op_all_programmes fail-closed).
	if !opRowExists(t, db, sim, progExists, "prg_sim_airtime01") {
		t.Fatal("a SIM_NG operator must see its own programme")
	}
	if opRowExists(t, db, sim, progExists, "prg_other") {
		t.Fatal("a SIM_NG operator must NEVER see an OTHER_NG programme — op_all_programmes must fail closed (USING(true) regression?)")
	}
	// …and NEVER OTHER_NG's funding pool (op_all_funding_pools fail-closed).
	if opRowExists(t, db, sim, poolExists, "pool_other") {
		t.Fatal("a SIM_NG operator must NEVER see an OTHER_NG funding pool — op_all_funding_pools must fail closed (USING(true) regression?)")
	}

	// Fail-closed: no scope set → sees nothing.
	if opRowExists(t, db, nil, progExists, "prg_sim_airtime01") || opRowExists(t, db, nil, poolExists, "pool_other") {
		t.Fatal("with no scope, the operator must see nothing (fail-closed)")
	}

	// The '*' admin path MUST work: op_all='true' reads the whole estate (both telcos) — this
	// is what 0067's policies enable; if they were missing the admin dashboard would be empty.
	if !opRowExists(t, db, allEstate, progExists, "prg_other") {
		t.Fatal("op_all='true' must read the OTHER_NG programme (op_all_programmes present + firing)")
	}
	if !opRowExists(t, db, allEstate, poolExists, "pool_other") {
		t.Fatal("op_all='true' must read the OTHER_NG funding pool (op_all_funding_pools present + firing)")
	}
}

// The same cross-telco isolation proof for the FOUR feed-health tables granted in 0068
// (recon_runs, suspense_items, recovery_unconfirmed_closes, audit_events). Each carries a
// role-agnostic telco RLS policy; op_all is the only cross-telco path and fires only for the
// '*' admin. Seeds an OTHER_NG row in every table and drives all three scopes. This is the
// mutation canary for 0068: flip ANY of the four op_all_* USING clauses to `true` and a
// telco-scoped operator starts seeing OTHER_NG — turning a "must NEVER see" assertion red
// (the R1 lesson from the MI dashboard, applied to the four new doors).
func TestOperatorRLS_FeedHealthTables_CrossTelcoIsolation(t *testing.T) {
	db := testutil.MustSetup(t, "opfeedhealth")
	ctx := context.Background()
	for _, s := range []string{
		`INSERT INTO telcos (telco_id, name, country, status) VALUES ('OTHER_NG','Other','NG','ACTIVE') ON CONFLICT DO NOTHING`,
		`INSERT INTO programmes (programme_id, telco_id, code, name, status)
		   VALUES ('prg_other','OTHER_NG','OTHER_AIRTIME','Other Airtime','ACTIVE')`,
		// a recovery_event to satisfy the suspense / unconfirmed-close FKs
		`INSERT INTO recovery_events (recovery_event_id, telco_id, source_event_id, amount_minor, currency, occurred_at)
		   VALUES ('ev_other','OTHER_NG','wh:ev_other',1000,'NGN', now())`,
		`INSERT INTO recon_runs (run_id, telco_id, programme_id, layer, period_start, period_end,
		   source_record_count, source_control_total_minor, source_hash,
		   platform_record_count, platform_control_total_minor, created_by)
		   VALUES ('run_other','OTHER_NG','prg_other','RECOVERY', now()-interval '1 day', now(),
		   1, 1000, 'hash_other', 1, 1000, 'seed:test')`,
		`INSERT INTO suspense_items (suspense_id, telco_id, recovery_event_id, amount_minor, currency, reason)
		   VALUES ('susp_other','OTHER_NG','ev_other',1000,'NGN','over-recovery')`,
		`INSERT INTO recovery_unconfirmed_closes (id, telco_id, subscriber_account_id, advance_id, recovery_event_id)
		   VALUES ('uc_other','OTHER_NG','sub_other','adv_other','ev_other')`,
		// audit row stamped with a telco (real feed-denials are telco_id NULL; a non-NULL telco
		// row is what lets us prove the op_all_audit_events isolation directly)
		`INSERT INTO audit_events (id, telco_id, actor, action, target_type, target_id)
		   VALUES ('aud_other','OTHER_NG','system','RECHARGE_FEED_DENIED','telco','OTHER_NG')`,
	} {
		if _, err := db.Admin.Exec(ctx, s); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	sim := map[string]string{"app.telco_id": "SIM_NG"}
	allEstate := map[string]string{"app.op_all": "true"}
	checks := []struct {
		name string
		sql  string
		id   string
	}{
		{"recon_runs", `SELECT EXISTS (SELECT 1 FROM recon_runs WHERE run_id=$1)`, "run_other"},
		{"suspense_items", `SELECT EXISTS (SELECT 1 FROM suspense_items WHERE suspense_id=$1)`, "susp_other"},
		{"recovery_unconfirmed_closes", `SELECT EXISTS (SELECT 1 FROM recovery_unconfirmed_closes WHERE id=$1)`, "uc_other"},
		{"audit_events", `SELECT EXISTS (SELECT 1 FROM audit_events WHERE id=$1)`, "aud_other"},
	}
	for _, c := range checks {
		// A SIM_NG operator must NEVER see the OTHER_NG row (op_all_* fails closed).
		if opRowExists(t, db, sim, c.sql, c.id) {
			t.Fatalf("a SIM_NG operator must NEVER see an OTHER_NG %s row — op_all_%s must fail closed (USING(true) regression?)", c.name, c.name)
		}
		// No scope → sees nothing (fail-closed).
		if opRowExists(t, db, nil, c.sql, c.id) {
			t.Fatalf("with no scope the operator must not see the OTHER_NG %s row (fail-closed)", c.name)
		}
		// The '*' admin path MUST read the estate (else the monitor is blank for admin).
		if !opRowExists(t, db, allEstate, c.sql, c.id) {
			t.Fatalf("op_all='true' must read the OTHER_NG %s row (op_all_%s present + firing)", c.name, c.name)
		}
	}
}
