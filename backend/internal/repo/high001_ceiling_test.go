package repo_test

// BX-HIGH-001: the per-telco daily recharge ceiling must be atomic under concurrency AND
// idempotent per source_event_id. The old path read a SUM in one tx and booked in another, so
// concurrent webhook deliveries both observed a stale total and both booked past the ceiling.
// HeldRecharge.ReserveDaily replaces that with a conditional UPSERT that row-locks the (telco,
// day) counter (concurrency), plus a per-event dedup so a crash-retry / cross-MAC replay of the
// same event cannot consume capacity twice (crash-reservation hardening).

import (
	"context"
	"fmt"
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
	const n = 8                 // 8 concurrent reservers (distinct events) competing for 3 slots

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
			// Distinct source_event_ids so the dedup never collides — they genuinely compete.
			errs[i] = repo.WithExplicitTenantTx(ctx, db.App, "CEIL_NG", func(tx pgx.Tx) error {
				_, reserved, _, err := (repo.HeldRecharge{}).ReserveDaily(ctx, tx, "CEIL_NG", fmt.Sprintf("wh:c%d", i), amount, ceiling)
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
	// drop the "WHERE reserved+amount <= ceiling" conditional in ReserveDaily and every reserver
	// wins (succeeded == 8, reserved == 24000).
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

// A retry of the SAME source_event_id (crash between reserve and book, or a cross-MAC replay)
// must NOT consume capacity twice — the per-event dedup makes the counter increment exactly once.
func TestBXHIGH001_ReservationIdempotentPerSourceEvent(t *testing.T) {
	db := testutil.MustSetup(t, "high001_idem")
	db.SeedTelco(t, "IDEM_NG", "")
	ctx := context.Background()
	const ceiling = int64(10_000)

	if err := repo.WithExplicitTenantTx(ctx, db.App, "IDEM_NG", func(tx pgx.Tx) error {
		_, ok1, fresh1, err := (repo.HeldRecharge{}).ReserveDaily(ctx, tx, "IDEM_NG", "wh:dup", 6_000, ceiling)
		if err != nil || !ok1 || !fresh1 {
			t.Fatalf("first reserve must be fresh+reserved: ok=%v fresh=%v err=%v", ok1, fresh1, err)
		}
		// Retry of the same event: still reserved, but NOT fresh (no second capacity taken).
		// Mutation proof: drop the recharge_reserved_event dedup and fresh is true both times,
		// so the counter reaches 12000.
		_, ok2, fresh2, err := (repo.HeldRecharge{}).ReserveDaily(ctx, tx, "IDEM_NG", "wh:dup", 6_000, ceiling)
		if err != nil || !ok2 {
			t.Fatalf("a retry of the same event must still be reserved: ok=%v err=%v", ok2, err)
		}
		if fresh2 {
			t.Fatal("a retry of the same source_event_id must NOT re-reserve capacity (fresh must be false) — BX-HIGH-001 crash hardening")
		}
		var reserved int64
		if err := tx.QueryRow(ctx,
			`SELECT reserved_minor FROM recharge_daily_reservation WHERE telco_id='IDEM_NG'`).Scan(&reserved); err != nil {
			return err
		}
		if reserved != 6_000 {
			t.Fatalf("a retried reservation must not double-consume: counter=%d, want 6000", reserved)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// ReleaseDaily returns a fresh reservation's capacity (and clears its per-event claim), so a
// released reservation frees the ceiling for later events (the handler releases only when a
// fresh reservation's booking failed/diverged).
func TestBXHIGH001_ReleaseReturnsCeilingCapacity(t *testing.T) {
	db := testutil.MustSetup(t, "high001_release")
	db.SeedTelco(t, "REL_NG", "")
	ctx := context.Background()
	const ceiling = int64(10_000)

	if err := repo.WithExplicitTenantTx(ctx, db.App, "REL_NG", func(tx pgx.Tx) error {
		day, okA, _, err := (repo.HeldRecharge{}).ReserveDaily(ctx, tx, "REL_NG", "wh:rel-a", 10_000, ceiling)
		if err != nil || !okA {
			t.Fatalf("first reserve of 10000 must succeed: ok=%v err=%v", okA, err)
		}
		// Ceiling full: a distinct event is refused.
		if _, okB, _, err := (repo.HeldRecharge{}).ReserveDaily(ctx, tx, "REL_NG", "wh:rel-b", 6_000, ceiling); err != nil {
			return err
		} else if okB {
			t.Fatal("a reserve that would exceed the full ceiling must be refused")
		}
		// Release the full reservation (as the handler does — the whole event amount); capacity
		// and the per-event claim are both returned.
		if err := (repo.HeldRecharge{}).ReleaseDaily(ctx, tx, "REL_NG", day, 10_000, "wh:rel-a"); err != nil {
			return err
		}
		if _, okC, _, err := (repo.HeldRecharge{}).ReserveDaily(ctx, tx, "REL_NG", "wh:rel-c", 6_000, ceiling); err != nil {
			return err
		} else if !okC {
			t.Fatal("after releasing the full 10000, a 6000 reserve must fit again (release returns capacity)")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
