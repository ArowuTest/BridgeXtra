package repo_test

// Gate B #1 Slice 2 (chokepoint) — the OperatorReader is the single site that
// sets the tenant scope from the trusted session authority. These prove the
// security property the review requires MOST: only the '*' platform admin can
// reach the op_all (read-all) path; a telco- or programme-scoped session never
// can. Scopes are built through the REAL session path (PortalSession.OperatorScope)
// so the test exercises the actual authority mapping, not a hand-forged scope.

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
)

// readerSees reports whether an operator with the given session Scope can see a
// specific subscriber row, going through the OperatorReader chokepoint.
func readerSees(t *testing.T, db *testutil.DB, sessionScope, subID string) bool {
	t.Helper()
	reader := repo.OperatorReader{Pool: db.Operator, Resolve: db.Worker}
	scope := repo.PortalSession{Scope: sessionScope}.OperatorScope()
	var seen bool
	if err := reader.Read(context.Background(), scope, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM subscriber_accounts WHERE subscriber_account_id=$1)`, subID).Scan(&seen)
	}); err != nil {
		t.Fatalf("reader.Read(%q): %v", sessionScope, err)
	}
	return seen
}

// The op_all path is reachable ONLY by the '*' platform admin — never by a
// telco-scoped operator, which is the real security boundary now.
func TestOperatorReader_OpAllContainment(t *testing.T) {
	db := testutil.MustSetup(t, "oprdr_opall")
	seedTwoTenants(t, db)

	// Telco-scoped SIM_NG operator: sees its own telco, NEVER the other's — the
	// chokepoint took the telco branch and did not set op_all.
	if !readerSees(t, db, "telco:SIM_NG", "sub_sim") {
		t.Fatal("a SIM_NG operator must see its own telco's row")
	}
	if readerSees(t, db, "telco:SIM_NG", "sub_other") {
		t.Fatal("a telco-scoped operator must NOT reach op_all — must never see OTHER_NG")
	}

	// The '*' platform admin: op_all path, reads across telcos.
	if !readerSees(t, db, "*", "sub_other") {
		t.Fatal("the '*' platform admin must read the whole estate (op_all)")
	}
}

// Fail-closed: a session without read authority ('global'/unrecognised) sets no
// scope, so the DB returns nothing.
func TestOperatorReader_FailClosedNoAuthority(t *testing.T) {
	db := testutil.MustSetup(t, "oprdr_failclosed")
	seedTwoTenants(t, db)

	if readerSees(t, db, "global", "sub_sim") || readerSees(t, db, "global", "sub_other") {
		t.Fatal("a no-authority scope must see nothing (fail-closed)")
	}
}

// A programme-scoped operator has its telco pinned (DB-enforced) via the trusted
// resolver — it sees its programme's telco and no other.
func TestOperatorReader_ProgrammeScopePinsTelco(t *testing.T) {
	db := testutil.MustSetup(t, "oprdr_prog")
	seedTwoTenants(t, db)

	// prg_sim_airtime01 is seeded on SIM_NG by the fixtures.
	if !readerSees(t, db, "programme:prg_sim_airtime01", "sub_sim") {
		t.Fatal("a programme operator must see its programme's telco rows")
	}
	if readerSees(t, db, "programme:prg_sim_airtime01", "sub_other") {
		t.Fatal("a programme operator must never see another telco's rows (telco pinned via resolver)")
	}
}

// The safety net: a portal read that does NOT go through the OperatorReader
// wrapper runs on the operator pool with no scope GUC. Because tcp_operator does
// not bypass RLS, the read fails closed to empty — a forgotten wrapper leaks
// nothing. Through the wrapper the same in-scope row is visible.
func TestOperatorReader_MissedWrapperFailsClosed(t *testing.T) {
	db := testutil.MustSetup(t, "oprdr_missed")
	seedTwoTenants(t, db) // sub_sim: telco SIM_NG, token tok_sim
	ctx := context.Background()
	scope := repo.PortalSession{Scope: "telco:SIM_NG"}.OperatorScope()

	// UNWRAPPED: call the read directly on the operator pool (no scoped tx) ->
	// RLS returns nothing -> the subscriber is not found.
	if _, _, _, _, _, err := repo.SubscriberTimeline(ctx, db.Operator, scope, "tok_sim"); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("an unwrapped operator read must fail closed (ErrNotFound), got %v", err)
	}

	// WRAPPED: through the chokepoint the scope GUC is set and the in-scope
	// subscriber is visible.
	reader := repo.OperatorReader{Pool: db.Operator, Resolve: db.Worker}
	var found bool
	if err := reader.Read(ctx, scope, func(ctx context.Context, tx pgx.Tx) error {
		_, _, _, _, _, e := repo.SubscriberTimeline(ctx, tx, scope, "tok_sim")
		found = e == nil
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("through the wrapper, the in-scope subscriber must be visible")
	}
}
