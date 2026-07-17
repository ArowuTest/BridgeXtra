package repo_test

// ADR-0001 SF-4: per-aggregate FIFO on DB-assigned seq; SKIP LOCKED across
// aggregates. The claim-blocking test proves an aggregate's second event is
// unclaimable while its first is unpublished — even by a concurrent worker.

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
)

func appendEvent(t *testing.T, db *testutil.DB, telco, agg, payload string) {
	t.Helper()
	ctx := platform.WithTenant(context.Background(), telco)
	if err := repo.WithTenantTx(ctx, db.App, func(tx pgx.Tx) error {
		return repo.Outbox{}.Append(ctx, tx, entity.OutboxEvent{
			ID: platform.NewID("evt"), TelcoID: telco, AggregateType: "Test",
			AggregateID: agg, EventType: "M0.Ping", SchemaVersion: 1,
			Payload: []byte(fmt.Sprintf(`{"p":%q}`, payload)), OccurredAt: timeNow(),
		})
	}); err != nil {
		t.Fatal(err)
	}
}

func TestV2_EVT_004_PerAggregateFIFO_AndSkipLockedAcrossAggregates(t *testing.T) {
	db := testutil.MustSetup(t, "outbox_fifo")
	db.SeedTelco(t, "TELCO_A", "")
	outbox := repo.Outbox{}
	ctx := context.Background()

	// agg1: e1, e2, e3 (strict order); agg2: f1.
	appendEvent(t, db, "TELCO_A", "agg1", "e1")
	appendEvent(t, db, "TELCO_A", "agg1", "e2")
	appendEvent(t, db, "TELCO_A", "agg1", "e3")
	appendEvent(t, db, "TELCO_A", "agg2", "f1")

	// Worker 1 claims a batch of 10: must get ONLY agg1-head (e1) and agg2-head (f1) —
	// e2/e3 are blocked behind their unpublished predecessor.
	tx1, err := db.Worker.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx1.Rollback(ctx)
	claimed1, err := outbox.ClaimBatch(ctx, tx1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed1) != 2 {
		t.Fatalf("want heads only (2 events), got %d: %+v", len(claimed1), claimed1)
	}
	// Compare parsed payloads — JSONB normalises whitespace.
	if payloadP(t, claimed1[0]) != "e1" || payloadP(t, claimed1[1]) != "f1" {
		t.Fatalf("wrong heads claimed: %s / %s", claimed1[0].Payload, claimed1[1].Payload)
	}

	// Concurrent worker 2 while worker 1 holds locks: SKIP LOCKED must yield
	// nothing (heads locked; successors FIFO-blocked). It must NOT block.
	done := make(chan struct{})
	var claimed2 []entity.OutboxEvent
	var err2 error
	go func() {
		defer close(done)
		tx2, e := db.Worker.Begin(ctx)
		if e != nil {
			err2 = e
			return
		}
		defer tx2.Rollback(ctx)
		claimed2, err2 = outbox.ClaimBatch(ctx, tx2, 10)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent claim blocked — SKIP LOCKED not effective")
	}
	if err2 != nil {
		t.Fatal(err2)
	}
	if len(claimed2) != 0 {
		t.Fatalf("concurrent worker must claim nothing, got %d", len(claimed2))
	}

	// Publish e1 within worker 1's tx, commit. Then e2 becomes the claimable head.
	if err := outbox.MarkPublished(ctx, tx1, claimed1[0].Seq, timeNow()); err != nil {
		t.Fatal(err)
	}
	if err := outbox.MarkPublished(ctx, tx1, claimed1[1].Seq, timeNow()); err != nil {
		t.Fatal(err)
	}
	if err := tx1.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	tx3, err := db.Worker.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx3.Rollback(ctx)
	claimed3, err := outbox.ClaimBatch(ctx, tx3, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed3) != 1 || payloadP(t, claimed3[0]) != "e2" {
		t.Fatalf("after publishing e1, head must be e2; got %+v", claimed3)
	}
}

func payloadP(t *testing.T, e entity.OutboxEvent) string {
	t.Helper()
	var v struct {
		P string `json:"p"`
	}
	if err := json.Unmarshal(e.Payload, &v); err != nil {
		t.Fatalf("payload parse: %v", err)
	}
	return v.P
}

func TestOutbox_AppendIsAtomicWithBusinessTx(t *testing.T) {
	// V2-EVT-002: event and state change commit together — a rolled-back tx
	// must leave no event behind.
	db := testutil.MustSetup(t, "outbox_atomic")
	db.SeedTelco(t, "TELCO_A", "")
	ctx := platform.WithTenant(context.Background(), "TELCO_A")

	sentinel := fmt.Errorf("business failure")
	err := repo.WithTenantTx(ctx, db.App, func(tx pgx.Tx) error {
		if err := (repo.Outbox{}).Append(ctx, tx, entity.OutboxEvent{
			ID: platform.NewID("evt"), TelcoID: "TELCO_A", AggregateType: "Test",
			AggregateID: "aggX", EventType: "M0.Ping", SchemaVersion: 1,
			Payload: []byte(`{}`), OccurredAt: timeNow(),
		}); err != nil {
			return err
		}
		return sentinel // force rollback after the insert
	})
	if err != sentinel {
		t.Fatalf("expected sentinel, got %v", err)
	}
	var n int
	if err := db.Admin.QueryRow(context.Background(), `SELECT count(*) FROM outbox`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("rolled-back tx leaked %d outbox events", n)
	}
}
