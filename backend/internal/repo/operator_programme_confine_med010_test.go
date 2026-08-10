package repo_test

// BX-MED-010: a programme-scoped operator must be confined to its OWN programme's rows, not the
// whole telco. The OperatorReader pins the telco for a programme session; migration 0081 adds
// RESTRICTIVE op_programme_confine_* policies driven by app.programme_id so the programme-grained
// money/risk tables (advances here) are DB-confined to the session's programme — closing the
// intra-tenant scope bypass where a programme:A operator could read programme B's advances / loan
// book within the same telco. The confinement is permissive when app.programme_id is unset, so a
// telco-scoped or '*' admin operator reads exactly as before.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
)

// seedTwoProgrammeAdvancesSIM seeds SIM_NG with TWO programmes, each carrying one ACTIVE advance
// (full FK chain). prg_sim_airtime01 + pool_sim_01 exist from the migrations.
func seedTwoProgrammeAdvancesSIM(t *testing.T, db *testutil.DB) {
	t.Helper()
	ctx := context.Background()
	for _, s := range []string{
		`INSERT INTO programmes (programme_id, telco_id, code, name, status)
		   VALUES ('prg_sim_data02','SIM_NG','SIM_DATA','SIM Data','ACTIVE')`,
		`INSERT INTO funding_pools (pool_id, programme_id, telco_id, currency, committed_minor)
		   VALUES ('pool_sim_data02','prg_sim_data02','SIM_NG','NGN',1000000)`,
		// Advance on prg_sim_airtime01.
		`INSERT INTO subscriber_accounts (subscriber_account_id, telco_id, msisdn_token, status)
		   VALUES ('sub_m10_a','SIM_NG','tok_m10_a','ACTIVE')`,
		`INSERT INTO decision_snapshots (decision_snapshot_id, telco_id, subscriber_account_id,
		   max_face_value_minor, currency, config_version_id)
		   VALUES ('ds_m10_a','SIM_NG','sub_m10_a',100000,'NGN','cfgv_seed')`,
		`INSERT INTO offers (offer_id, telco_id, programme_id, subscriber_account_id, decision_snapshot_id,
		   face_value_minor, fee_minor, disbursed_minor, repayment_minor, currency, fee_model,
		   product_config_version_id, expires_at)
		   VALUES ('offer_m10_a','SIM_NG','prg_sim_airtime01','sub_m10_a','ds_m10_a',
		   1000,100,900,1000,'NGN','DEDUCTED_UPFRONT','pcfgv_seed', now()+interval '1 day')`,
		`INSERT INTO advances (advance_id, telco_id, programme_id, subscriber_account_id, offer_id,
		   funding_pool_id, idempotency_key, correlation_id, state, face_value_minor, fee_minor,
		   disbursed_minor, outstanding_minor, currency)
		   VALUES ('adv_m10_air','SIM_NG','prg_sim_airtime01','sub_m10_a','offer_m10_a','pool_sim_01',
		   'idem_m10_a','corr_m10_a','ACTIVE',1000,100,900,1000,'NGN')`,
		// Advance on prg_sim_data02 — SAME telco, DIFFERENT programme.
		`INSERT INTO subscriber_accounts (subscriber_account_id, telco_id, msisdn_token, status)
		   VALUES ('sub_m10_b','SIM_NG','tok_m10_b','ACTIVE')`,
		`INSERT INTO decision_snapshots (decision_snapshot_id, telco_id, subscriber_account_id,
		   max_face_value_minor, currency, config_version_id)
		   VALUES ('ds_m10_b','SIM_NG','sub_m10_b',100000,'NGN','cfgv_seed')`,
		`INSERT INTO offers (offer_id, telco_id, programme_id, subscriber_account_id, decision_snapshot_id,
		   face_value_minor, fee_minor, disbursed_minor, repayment_minor, currency, fee_model,
		   product_config_version_id, expires_at)
		   VALUES ('offer_m10_b','SIM_NG','prg_sim_data02','sub_m10_b','ds_m10_b',
		   1000,100,900,1000,'NGN','DEDUCTED_UPFRONT','pcfgv_seed', now()+interval '1 day')`,
		`INSERT INTO advances (advance_id, telco_id, programme_id, subscriber_account_id, offer_id,
		   funding_pool_id, idempotency_key, correlation_id, state, face_value_minor, fee_minor,
		   disbursed_minor, outstanding_minor, currency)
		   VALUES ('adv_m10_data','SIM_NG','prg_sim_data02','sub_m10_b','offer_m10_b','pool_sim_data02',
		   'idem_m10_b','corr_m10_b','ACTIVE',1000,100,900,1000,'NGN')`,
	} {
		if _, err := db.Admin.Exec(ctx, s); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
}

// Direct GUC form: prove the RESTRICTIVE policy confines by programme when app.programme_id is set,
// and is permissive when it is not.
func TestBXMED010_ProgrammeOperator_ConfinedToOwnProgramme(t *testing.T) {
	db := testutil.MustSetup(t, "med010_confine")
	seedTwoProgrammeAdvancesSIM(t, db)

	const advExists = `SELECT EXISTS (SELECT 1 FROM advances WHERE advance_id=$1)`

	// programme:airtime — telco pinned AND programme pinned.
	prgAir := map[string]string{"app.telco_id": "SIM_NG", "app.programme_id": "prg_sim_airtime01"}
	if !opRowExists(t, db, prgAir, advExists, "adv_m10_air") {
		t.Fatal("a programme:airtime operator must see its OWN programme's advance")
	}
	// Load-bearing (BX-MED-010). Mutation proof: drop app.programme_id in OperatorReader OR the
	// op_programme_confine_advances policy (migration 0081) and this returns true — RED.
	if opRowExists(t, db, prgAir, advExists, "adv_m10_data") {
		t.Fatal("BX-MED-010: a programme:airtime operator must NOT see a sibling programme's advance in the same telco")
	}

	// TELCO-scoped operator (no app.programme_id) still sees BOTH — confinement is permissive when
	// unset, so the telco boundary is unchanged (guards against over-restricting the telco role).
	telco := map[string]string{"app.telco_id": "SIM_NG"}
	if !opRowExists(t, db, telco, advExists, "adv_m10_air") || !opRowExists(t, db, telco, advExists, "adv_m10_data") {
		t.Fatal("a telco-scoped operator must see BOTH programmes' advances (confinement must not fire when app.programme_id is unset)")
	}
}

// End-to-end through the OperatorReader chokepoint: a real programme session sets app.programme_id
// itself, so the confinement holds without the caller passing any programme filter.
func TestBXMED010_ProgrammeConfinement_ThroughOperatorReaderChokepoint(t *testing.T) {
	db := testutil.MustSetup(t, "med010_chokepoint")
	seedTwoProgrammeAdvancesSIM(t, db)
	reader := repo.OperatorReader{Pool: db.Operator, Resolve: db.Config}
	scope := repo.PortalSession{Scope: "programme:prg_sim_airtime01"}.OperatorScope()

	sees := func(advID string) bool {
		var ok bool
		if err := reader.Read(context.Background(), scope, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM advances WHERE advance_id=$1)`, advID).Scan(&ok)
		}); err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if !sees("adv_m10_air") {
		t.Fatal("through the chokepoint, a programme:airtime session must see its own programme's advance")
	}
	if sees("adv_m10_data") {
		t.Fatal("BX-MED-010: through the chokepoint, a programme:airtime session must NOT see a sibling programme's advance")
	}
}
