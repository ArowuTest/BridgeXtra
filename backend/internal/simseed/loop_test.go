//go:build simseed_loop

package simseed

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
)

// recentBusinessDay returns today in Lagos — within the fresh-scoring window so the
// good-payer scores at its intended tier (the businessDay is an INPUT, not generated
// data, so using the wall clock here is fine).
func recentBusinessDay() string {
	lagos, _ := time.LoadLocation("Africa/Lagos")
	return time.Now().In(lagos).Format("2006-01-02")
}

func advanceCount(t *testing.T, db *testutil.DB, where string, args ...any) int {
	t.Helper()
	ctx := platform.WithTenant(context.Background(), SyntheticTelco)
	var n int
	if err := repo.WithTenantTx(ctx, db.App, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, "SELECT count(*) FROM advances "+where, args...).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

// Slice 1: the loop drives one good-payer through the REAL usecases to an ACTIVE
// advance, and a replay books no new money.
func TestLoop_Slice1_GoodPayerToActive(t *testing.T) {
	db := testutil.MustSetup(t, "simseed_loop_s1")
	ctx := context.Background()
	day := recentBusinessDay()
	plan := LoopPlan{Seed: "loop-test", BusinessDay: day, ProgrammeID: "prg_sim_airtime01", Repeat: 1}

	res, err := RunLoop(ctx, db.App, plan)
	if err != nil {
		t.Fatalf("RunLoop: %v", err)
	}
	if res.Subscribers != 1 || res.Advances != 1 || res.Declined != 0 {
		t.Fatalf("want 1 subscriber / 1 advance / 0 declined, got %+v", res)
	}
	if len(res.Members) != 1 || res.Members[0].State != "ACTIVE" {
		t.Fatalf("good-payer must reach ACTIVE, got %+v", res.Members)
	}
	// Real settlement: the advance is genuinely ACTIVE in the DB (via ResolveOutcome).
	if got := advanceCount(t, db, "WHERE state='ACTIVE'"); got != 1 {
		t.Fatalf("want 1 ACTIVE advance in DB, got %d", got)
	}

	// Replay: a second identical run reuses the booked advance (GetByIdemKey) and
	// books NO new money.
	res2, err := RunLoop(ctx, db.App, plan)
	if err != nil {
		t.Fatalf("RunLoop replay: %v", err)
	}
	if res2.Advances != 1 {
		t.Fatalf("replay must report the same one advance, got %+v", res2)
	}
	if got := advanceCount(t, db, ""); got != 1 {
		t.Fatalf("replay must not book a new advance — want 1 total, got %d", got)
	}
}

// No-direct-INSERT invariant (design Slice 1): loop.go books money ONLY through the
// usecases — it must contain no direct INSERT into the money core.
func TestLoop_Slice1_NoDirectMoneyInsert(t *testing.T) {
	_, here, _, _ := runtime.Caller(0)
	b, err := os.ReadFile(filepath.Join(filepath.Dir(here), "loop.go"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, forbidden := range []string{
		"INSERT INTO advances", "INSERT INTO offers", "INSERT INTO decision_snapshots",
		"INSERT INTO recovery_events", "INSERT INTO recovery_allocations",
		"INSERT INTO journals", "INSERT INTO consents", "INSERT INTO fulfilment_attempts",
	} {
		if strings.Contains(src, forbidden) {
			t.Errorf("loop.go contains a direct %q — the loop must book money only through the usecases", forbidden)
		}
	}
}
