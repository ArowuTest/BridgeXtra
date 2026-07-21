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
