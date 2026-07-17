package outboxdispatch_test

// Dispatcher-level proof: events flow append -> claim -> handler -> published,
// per-aggregate order preserved end to end; handler failure leaves the event
// unpublished with attempts+error recorded (visible backlog, V2-EVT-008).

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
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

func appendN(t *testing.T, db *testutil.DB, agg string, n int) {
	t.Helper()
	ctx := platform.WithTenant(context.Background(), "TELCO_A")
	for i := 1; i <= n; i++ {
		if err := repo.WithTenantTx(ctx, db.App, func(tx pgx.Tx) error {
			return repo.Outbox{}.Append(ctx, tx, entity.OutboxEvent{
				ID: platform.NewID("evt"), TelcoID: "TELCO_A", AggregateType: "Test",
				AggregateID: agg, EventType: "T.Event", SchemaVersion: 1,
				Payload: []byte(fmt.Sprintf(`{"n":%d,"agg":%q}`, i, agg)),
				OccurredAt: time.Now().UTC(),
			})
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDispatcher_DeliversInPerAggregateOrder(t *testing.T) {
	db := testutil.MustSetup(t, "dispatch_order")
	db.SeedTelco(t, "TELCO_A", "")
	appendN(t, db, "aggA", 3)
	appendN(t, db, "aggB", 2)

	var mu sync.Mutex
	delivered := map[string][]int{}
	d := outboxdispatch.New(db.Worker, configsvc.New(db.Worker), slog.Default())
	d.Register("T.Event", func(ctx context.Context, e entity.OutboxEvent) error {
		var p struct {
			N int `json:"n"`
		}
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return err
		}
		mu.Lock()
		delivered[e.AggregateID] = append(delivered[e.AggregateID], p.N)
		mu.Unlock()
		return nil
	})

	total := 0
	for i := 0; i < 10 && total < 5; i++ {
		n, err := d.RunOnce(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		total += n
	}
	if total != 5 {
		t.Fatalf("want 5 dispatched, got %d", total)
	}
	for agg, seq := range delivered {
		for i := 1; i < len(seq); i++ {
			if seq[i-1] >= seq[i] {
				t.Fatalf("aggregate %s delivered out of order: %v", agg, seq)
			}
		}
	}
	if len(delivered["aggA"]) != 3 || len(delivered["aggB"]) != 2 {
		t.Fatalf("delivery counts wrong: %+v", delivered)
	}
}

func TestDispatcher_HandlerFailureLeavesVisibleBacklog(t *testing.T) {
	db := testutil.MustSetup(t, "dispatch_fail")
	db.SeedTelco(t, "TELCO_A", "")
	appendN(t, db, "aggF", 1)

	d := outboxdispatch.New(db.Worker, configsvc.New(db.Worker), slog.Default())
	d.Register("T.Event", func(ctx context.Context, e entity.OutboxEvent) error {
		return fmt.Errorf("downstream boom")
	})
	if _, err := d.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	var published *string
	var attempts int
	var lastError string
	if err := db.Admin.QueryRow(context.Background(),
		`SELECT published_at::text, attempts, COALESCE(last_error,'') FROM outbox`).
		Scan(&published, &attempts, &lastError); err != nil {
		t.Fatal(err)
	}
	if published != nil {
		t.Fatal("failed event must remain unpublished")
	}
	if attempts != 1 || lastError == "" {
		t.Fatalf("failure must record attempts+error, got attempts=%d err=%q", attempts, lastError)
	}
}
