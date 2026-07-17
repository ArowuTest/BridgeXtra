package outboxdispatch_test

// G0-F2 fix verification (builder-side, complements the reviewer reproducer):
// 1. max-attempts events leave the claim window (dead-lettered) instead of
//    dragging every cycle;
// 2. a dead-lettered head still blocks ITS OWN aggregate's successors
//    (FIFO never silently skips a financial event);
// 3. other aggregates keep flowing;
// 4. operator requeue restores the event and unblocks its aggregate.

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/outboxdispatch"
)

func appendEvt(t *testing.T, db *testutil.DB, agg, eventType string) {
	t.Helper()
	ctx := platform.WithTenant(context.Background(), "TELCO_A")
	if err := repo.WithTenantTx(ctx, db.App, func(tx pgx.Tx) error {
		return repo.Outbox{}.Append(ctx, tx, entity.OutboxEvent{
			ID: platform.NewID("evt"), TelcoID: "TELCO_A", AggregateType: "Test",
			AggregateID: agg, EventType: eventType, SchemaVersion: 1,
			Payload: []byte(`{}`), OccurredAt: time.Now().UTC(),
		})
	}); err != nil {
		t.Fatal(err)
	}
}

func TestG0F2_MaxAttempts_DeadLettersAndPreservesOwnAggregateFIFO(t *testing.T) {
	db := testutil.MustSetup(t, "dispatch_deadletter")
	db.SeedTelco(t, "TELCO_A", "")

	// agg1: failing event then a successor; agg2: healthy event.
	appendEvt(t, db, "agg1", "T.Fails")
	appendEvt(t, db, "agg1", "T.Event")
	appendEvt(t, db, "agg2", "T.Event")

	delivered := map[string]int{}
	d := outboxdispatch.New(db.Worker, configsvc.New(db.Worker), slog.Default())
	d.Register("T.Fails", func(ctx context.Context, e entity.OutboxEvent) error {
		return fmt.Errorf("permanent downstream failure")
	})
	d.Register("T.Event", func(ctx context.Context, e entity.OutboxEvent) error {
		delivered[e.AggregateID]++
		return nil
	})

	// Seeded max_attempts=10: run enough cycles to exhaust and dead-letter.
	for i := 0; i < 13; i++ {
		if _, err := d.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}

	// agg2 flowed; agg1's successor is STILL blocked behind its dead-lettered head.
	if delivered["agg2"] != 1 {
		t.Fatalf("healthy aggregate must deliver exactly once, got %d", delivered["agg2"])
	}
	if delivered["agg1"] != 0 {
		t.Fatal("successor of a dead-lettered head must stay blocked (FIFO must not skip)")
	}

	// The failing event is dead-lettered exactly once and out of the claim window.
	var deadSeq int64
	var attempts int
	if err := db.Admin.QueryRow(context.Background(),
		`SELECT seq, attempts FROM outbox WHERE event_type='T.Fails' AND dead_lettered_at IS NOT NULL`).
		Scan(&deadSeq, &attempts); err != nil {
		t.Fatalf("failing event must be dead-lettered: %v", err)
	}
	if attempts < 10 {
		t.Fatalf("dead-letter must only happen at max attempts, got %d", attempts)
	}

	// Operator requeue (V2-EVT-009): handler fixed, event replays, aggregate unblocks.
	d2 := outboxdispatch.New(db.Worker, configsvc.New(db.Worker), slog.Default())
	d2.Register("T.Fails", func(ctx context.Context, e entity.OutboxEvent) error { return nil }) // "fixed"
	d2.Register("T.Event", func(ctx context.Context, e entity.OutboxEvent) error {
		delivered[e.AggregateID]++
		return nil
	})
	if err := repo.WithPlatformTx(context.Background(), db.Worker, func(tx pgx.Tx) error {
		return repo.Outbox{}.Requeue(context.Background(), tx, deadSeq)
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := d2.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if delivered["agg1"] != 1 {
		t.Fatalf("after requeue+fix, agg1 successor must deliver exactly once, got %d", delivered["agg1"])
	}

	// Nothing left unpublished or dead-lettered.
	var unpub, dead int
	if err := db.Admin.QueryRow(context.Background(),
		`SELECT count(*) FILTER (WHERE published_at IS NULL),
		        count(*) FILTER (WHERE dead_lettered_at IS NOT NULL)
		 FROM outbox`).Scan(&unpub, &dead); err != nil {
		t.Fatal(err)
	}
	if unpub != 0 || dead != 0 {
		t.Fatalf("backlog must be drained: unpublished=%d dead=%d", unpub, dead)
	}
}

func TestG0F2_EmptyRegistry_ClaimsNothing(t *testing.T) {
	db := testutil.MustSetup(t, "dispatch_emptyreg")
	db.SeedTelco(t, "TELCO_A", "")
	appendEvt(t, db, "aggX", "T.Event")

	d := outboxdispatch.New(db.Worker, configsvc.New(db.Worker), slog.Default())
	n, err := d.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("dispatcher with no handlers must process nothing, got %d", n)
	}
	// Event untouched: no attempts consumed, not dead-lettered.
	var attempts int
	var dead bool
	if err := db.Admin.QueryRow(context.Background(),
		`SELECT attempts, dead_lettered_at IS NOT NULL FROM outbox WHERE aggregate_id='aggX'`).
		Scan(&attempts, &dead); err != nil {
		t.Fatal(err)
	}
	if attempts != 0 || dead {
		t.Fatalf("unclaimed event must be untouched: attempts=%d dead=%v", attempts, dead)
	}
}
