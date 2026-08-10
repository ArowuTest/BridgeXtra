package repo_test

// BX-HIGH-001: the per-telco daily recharge ceiling must be atomic under concurrency. The
// old path read a SUM in one tx and booked in another, so concurrent webhook deliveries both
// observed a stale total and both booked past the ceiling. HeldRecharge.ReserveDaily replaces
// that with a conditional UPSERT that row-locks the (telco, day) counter, so concurrent
// reservers serialize and the ceiling can never be exceeded. This test fires many concurrent
// reservations that TOGETHER far exceed the ceiling and proves exactly the ceiling's worth is
// admitted — run under -race, the row lock makes it deterministic, not probabilistic.

import (
	"context"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
)

func TestBXHIGH001_DailyCeilingAtomicUnderConcurrency(t *testing.T) {
	db := testutil.MustSetup(t, "high001_ceiling")
	db.SeedTelco(t, "CEIL_NG", "") // just the telco (FK target); no credential needed
	ctx := context.Background()

	const ceiling = int64(10_000)
	const amount = int64(3_000) // 3 fit (9000 <= 10000); a 4th would be 12000 > 10000
	const n = 8                 // 8 concurrent reservers competing for 3 slots

	var wg sync.WaitGroup
	ok := make([]bool, n)
	errs := make([]error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // release all goroutines together to maximise contention
			// Each reservation runs in its own tenant tx, exactly as the webhook handler does.
			errs[i] = repo.WithExplicitTenantTx(ctx, db.App, "CEIL_NG", func(tx pgx.Tx) error {
				_, reserved, err := (repo.HeldRecharge{}).ReserveDaily(ctx, tx, "CEIL_NG", amount, ceiling)
				ok[i] = reserved
				return err
			})
		}(i)
	}
	close(start)
	wg.Wait()

	succeeded := 0
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("reserve %d errored: %v", i, errs[i])
		}
		if ok[i] {
			succeeded++
		}
	}
	// Exactly ceiling/amount = 3 reservations may win; the rest are refused. Mutation proof:
	// drop the "WHERE reserved+amount <= ceiling" conditional in ReserveDaily and every
	// reserver wins (succeeded == 8, reserved == 24000).
	if succeeded != 3 {
		t.Fatalf("under %d concurrent reservers of %d against ceiling %d, exactly 3 must win, got %d", n, amount, ceiling, succeeded)
	}

	var reserved int64
	if err := db.Admin.QueryRow(ctx,
		`SELECT reserved_minor FROM recharge_daily_reservation WHERE telco_id='CEIL_NG'`).Scan(&reserved); err != nil {
		t.Fatal(err)
	}
	if reserved > ceiling {
		t.Fatalf("reserved_minor %d exceeds the ceiling %d — the ceiling was raced past (BX-HIGH-001)", reserved, ceiling)
	}
	if reserved != 9_000 {
		t.Fatalf("exactly 3 x 3000 = 9000 must be reserved, got %d", reserved)
	}
}

// ReleaseDaily returns capacity, so a released reservation frees a slot for a later booking
// (the handler releases when a booking fails or is an idempotent replay).
func TestBXHIGH001_ReleaseReturnsCeilingCapacity(t *testing.T) {
	db := testutil.MustSetup(t, "high001_release")
	db.SeedTelco(t, "REL_NG", "")
	ctx := context.Background()
	const ceiling = int64(10_000)

	if err := repo.WithExplicitTenantTx(ctx, db.App, "REL_NG", func(tx pgx.Tx) error {
		day, okA, err := (repo.HeldRecharge{}).ReserveDaily(ctx, tx, "REL_NG", 10_000, ceiling)
		if err != nil || !okA {
			t.Fatalf("first reserve of 10000 must succeed: ok=%v err=%v", okA, err)
		}
		// Ceiling full: the next reserve is refused.
		if _, okB, err := (repo.HeldRecharge{}).ReserveDaily(ctx, tx, "REL_NG", 3_000, ceiling); err != nil {
			return err
		} else if okB {
			t.Fatal("a reserve that would exceed the full ceiling must be refused")
		}
		// Release 4000 of the original reservation; capacity returns.
		if err := (repo.HeldRecharge{}).ReleaseDaily(ctx, tx, "REL_NG", day, 4_000); err != nil {
			return err
		}
		if _, okC, err := (repo.HeldRecharge{}).ReserveDaily(ctx, tx, "REL_NG", 3_000, ceiling); err != nil {
			return err
		} else if !okC {
			t.Fatal("after releasing 4000, a 3000 reserve must fit again (release returns capacity)")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
