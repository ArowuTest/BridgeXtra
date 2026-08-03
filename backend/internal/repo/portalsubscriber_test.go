package repo_test

// Wave B.2a Subscriber-360 — scope isolation, proven on the REAL RLS operator pool
// (db.Operator via the OperatorReader chokepoint), never a superuser pool. The security
// property the reviewer locked (condition #2): search + open + phone→token are all
// scope-bound — a telco operator can never reach another tenant's subscriber, and a
// no-authority operator reaches nothing. A direct out-of-scope id returns ErrNotFound
// (no cross-tenant oracle), exactly like the loan-book and support reads.

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
)

func searchUnderScope(t *testing.T, db *testutil.DB, sessionScope, query string) []repo.SubscriberDirectoryRow {
	t.Helper()
	reader := repo.OperatorReader{Pool: db.Operator, Resolve: db.Worker}
	scope := repo.PortalSession{Scope: sessionScope}.OperatorScope()
	var out []repo.SubscriberDirectoryRow
	if err := reader.Read(context.Background(), scope, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		out, e = repo.SearchSubscribers(ctx, tx, scope, query, 100)
		return e
	}); err != nil {
		t.Fatalf("search(%q,%q): %v", sessionScope, query, err)
	}
	return out
}

// profileUnderScope surfaces the DOMAIN error (ErrNotFound) to the caller rather than
// failing the read — an out-of-scope id is a not-found, not a read failure.
func profileUnderScope(t *testing.T, db *testutil.DB, sessionScope, acct string) (repo.SubscriberProfileResult, error) {
	t.Helper()
	reader := repo.OperatorReader{Pool: db.Operator, Resolve: db.Worker}
	scope := repo.PortalSession{Scope: sessionScope}.OperatorScope()
	var res repo.SubscriberProfileResult
	var domainErr error
	if err := reader.Read(context.Background(), scope, func(ctx context.Context, tx pgx.Tx) error {
		res, domainErr = repo.GetSubscriberProfile(ctx, tx, scope, acct)
		return nil
	}); err != nil {
		t.Fatalf("profile read(%q): %v", sessionScope, err)
	}
	return res, domainErr
}

func hasSub(rows []repo.SubscriberDirectoryRow, id string) bool {
	for _, r := range rows {
		if r.SubscriberAccountID == id {
			return true
		}
	}
	return false
}

func TestB2_SubscriberScope_CrossTenantIsolation(t *testing.T) {
	db := testutil.MustSetup(t, "b2_scope")
	seedTwoTenants(t, db) // sub_sim/tok_sim @ SIM_NG, sub_other/tok_other @ OTHER_NG

	sim := searchUnderScope(t, db, "telco:SIM_NG", "")
	if !hasSub(sim, "sub_sim") {
		t.Fatal("a SIM_NG operator must see its own subscriber")
	}
	if hasSub(sim, "sub_other") {
		t.Fatal("a SIM_NG operator must NOT see OTHER_NG's subscriber (cross-tenant leak)")
	}

	if _, err := profileUnderScope(t, db, "telco:SIM_NG", "sub_sim"); err != nil {
		t.Fatalf("a SIM_NG operator must open its own subscriber: %v", err)
	}
	if _, err := profileUnderScope(t, db, "telco:SIM_NG", "sub_other"); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("opening another tenant's subscriber must be ErrNotFound (no oracle), got %v", err)
	}

	// The '*' platform admin reads the whole estate (op_all).
	all := searchUnderScope(t, db, "*", "")
	if !hasSub(all, "sub_sim") || !hasSub(all, "sub_other") {
		t.Fatal("the '*' admin must see both tenants' subscribers")
	}
}

func TestB2_SubscriberScope_NoAuthorityFailsClosed(t *testing.T) {
	db := testutil.MustSetup(t, "b2_noauth")
	seedTwoTenants(t, db)

	if rows := searchUnderScope(t, db, "global", ""); len(rows) != 0 {
		t.Fatalf("a no-authority scope must return no subscribers, got %d", len(rows))
	}
	if _, err := profileUnderScope(t, db, "global", "sub_sim"); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("a no-authority scope must not open any subscriber (ErrNotFound), got %v", err)
	}
}

func TestB2_SubscriberSearch_SuffixMatch(t *testing.T) {
	db := testutil.MustSetup(t, "b2_suffix")
	seedTwoTenants(t, db) // token tok_sim

	// The last 4 of tok_sim ("_sim") is the tail an admin sees masked — searching it finds
	// the subscriber; a non-matching suffix must return nothing (not everything).
	if rows := searchUnderScope(t, db, "telco:SIM_NG", "_sim"); !hasSub(rows, "sub_sim") {
		t.Fatal("suffix search on the visible tail must find the subscriber")
	}
	if rows := searchUnderScope(t, db, "telco:SIM_NG", "zzzz"); len(rows) != 0 {
		t.Fatalf("a non-matching suffix must return nothing, got %d", len(rows))
	}
}
