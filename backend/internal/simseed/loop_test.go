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

// The loop drives the cohort through the REAL usecases: the good-payer reaches an
// ACTIVE advance (real settlement via ResolveOutcome), every borrower originates, and
// a replay books no new money.
func TestLoop_GoodPayerActiveAndReplay(t *testing.T) {
	db := testutil.MustSetup(t, "simseed_loop_replay")
	ctx := context.Background()
	plan := LoopPlan{Seed: "loop-replay", BusinessDay: recentBusinessDay(), ProgrammeID: "prg_sim_airtime01", Repeat: 1}

	borrowers := 0
	for _, p := range profiles {
		if p.originates {
			borrowers++
		}
	}

	res, err := RunLoop(ctx, db.App, plan)
	if err != nil {
		t.Fatalf("RunLoop: %v", err)
	}
	if res.Subscribers != len(profiles) || res.Advances != borrowers {
		t.Fatalf("want %d subscribers / %d advances, got %+v", len(profiles), borrowers, res)
	}
	var goodState string
	for _, m := range res.Members {
		if m.Profile == "good-payer" {
			goodState = m.State
		}
	}
	// The good-payer reaches ACTIVE at confirm (real settlement via ResolveOutcome) —
	// captured in res.Members before Slice-3 recovery later closes the advance.
	if goodState != "ACTIVE" {
		t.Fatalf("good-payer must reach ACTIVE (real settlement), got %q", goodState)
	}
	// Every borrower has a booked advance (post-recovery some are CLOSED/partial — that
	// is Slice 3's concern; here we prove origination + replay-idempotency on the count).
	if got := advanceCount(t, db, ""); got != borrowers {
		t.Fatalf("want %d advances booked, got %d", borrowers, got)
	}

	// Replay: a second identical run reuses the booked advances (GetByIdemKey) and
	// books NO new money.
	if _, err := RunLoop(ctx, db.App, plan); err != nil {
		t.Fatalf("RunLoop replay: %v", err)
	}
	if got := advanceCount(t, db, ""); got != borrowers {
		t.Fatalf("replay must not book new advances — want %d total, got %d", borrowers, got)
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
