package outboxdispatch_test

// REVIEWER REPRODUCER (G0-F2, uncommitted): head-of-line batch starvation.
//
// ClaimBatch orders by seq globally and applies LIMIT before any healthy
// aggregate beyond the limit is reached. Events that can never publish —
// unregistered event types (and equally, events parked at max attempts) —
// are re-claimed every cycle, occupy the entire claim batch, and healthy
// aggregates behind them are never dispatched. With the seeded default
// claim_batch_size=50, fifty poison events stall the whole dispatcher.
//
// Expected current behaviour: this test FAILS (healthy event never delivered).
// The fix must free batch slots from permanently-skippable head events while
// preserving per-aggregate FIFO (successors of a quarantined head stay blocked
// via the NOT EXISTS guard — that part is correct and must not change).

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

func TestReviewer_G0F2_PoisonHeadEventsStarveHealthyAggregates(t *testing.T) {
	db := testutil.MustSetup(t, "dispatch_starvation")
	db.SeedTelco(t, "TELCO_A", "")

	ctx := platform.WithTenant(context.Background(), "TELCO_A")

	// 50 events of an UNREGISTERED type across distinct aggregates: each is
	// "left unclaimed" by the dispatcher, but still occupies a claim slot
	// every single cycle (seeded claim_batch_size = 50).
	for i := 0; i < 50; i++ {
		agg := fmt.Sprintf("poison_%02d", i)
		if err := repo.WithTenantTx(ctx, db.App, func(tx pgx.Tx) error {
			return repo.Outbox{}.Append(ctx, tx, entity.OutboxEvent{
				ID: platform.NewID("evt"), TelcoID: "TELCO_A", AggregateType: "Test",
				AggregateID: agg, EventType: "T.NoHandlerRegistered", SchemaVersion: 1,
				Payload: []byte(`{}`), OccurredAt: time.Now().UTC(),
			})
		}); err != nil {
			t.Fatal(err)
		}
	}

	// One healthy event, appended after the poison block.
	if err := repo.WithTenantTx(ctx, db.App, func(tx pgx.Tx) error {
		return repo.Outbox{}.Append(ctx, tx, entity.OutboxEvent{
			ID: platform.NewID("evt"), TelcoID: "TELCO_A", AggregateType: "Test",
			AggregateID: "healthy", EventType: "T.Event", SchemaVersion: 1,
			Payload: []byte(`{}`), OccurredAt: time.Now().UTC(),
		})
	}); err != nil {
		t.Fatal(err)
	}

	delivered := 0
	d := outboxdispatch.New(db.Worker, configsvc.New(db.Worker), slog.Default())
	d.Register("T.Event", func(ctx context.Context, e entity.OutboxEvent) error {
		delivered++
		return nil
	})

	// Generous number of cycles: a correct dispatcher delivers the healthy
	// event on the first or second cycle.
	for i := 0; i < 10; i++ {
		if _, err := d.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}

	if delivered != 1 {
		t.Fatalf("healthy aggregate starved: delivered=%d after 10 cycles — "+
			"poison head events consume the entire claim batch (G0-F2)", delivered)
	}
}
