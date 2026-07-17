package repo_test

// V2-API-002/003: the DB is the arbiter; a duplicate key returns the ORIGINAL
// outcome; concurrent duplicates produce exactly one stored record.

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
)

func TestV2_API_003_DuplicateKeyReturnsOriginalOutcome(t *testing.T) {
	db := testutil.MustSetup(t, "idem_dup")
	db.SeedTelco(t, "TELCO_A", "")
	idem := repo.Idempotency{}
	ctx := platform.WithTenant(context.Background(), "TELCO_A")

	// First request commits outcome 200/{"advance":"adv_1"}.
	if err := repo.WithTenantTx(ctx, db.App, func(tx pgx.Tx) error {
		_, stored, err := idem.PutIfAbsent(ctx, tx, entity.IdempotencyRecord{
			TelcoID: "TELCO_A", Operation: "advance.create", IdemKey: "req-1",
			RequestHash: "h1", ResponseStatus: 200, ResponseBody: []byte(`{"advance":"adv_1"}`),
		})
		if err != nil {
			return err
		}
		if !stored {
			t.Error("first put must store")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Retry — crash-after-commit semantics: a NEW transaction (as after a
	// process restart) presents the same key with a DIFFERENT outcome; it must
	// receive the ORIGINAL, and the original must remain stored.
	if err := repo.WithTenantTx(ctx, db.App, func(tx pgx.Tx) error {
		got, stored, err := idem.PutIfAbsent(ctx, tx, entity.IdempotencyRecord{
			TelcoID: "TELCO_A", Operation: "advance.create", IdemKey: "req-1",
			RequestHash: "h1", ResponseStatus: 500, ResponseBody: []byte(`{"would_be":"duplicate"}`),
		})
		if err != nil {
			return err
		}
		if stored {
			t.Error("duplicate must not store")
		}
		// Parse — JSONB normalises whitespace.
		var body struct {
			Advance string `json:"advance"`
		}
		if err := json.Unmarshal(got.ResponseBody, &body); err != nil {
			t.Fatalf("parse original body: %v", err)
		}
		if got.ResponseStatus != 200 || body.Advance != "adv_1" {
			t.Errorf("duplicate must return original outcome, got %d %s", got.ResponseStatus, got.ResponseBody)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestIdempotency_ConcurrentDuplicates_ExactlyOneStored(t *testing.T) {
	db := testutil.MustSetup(t, "idem_race")
	db.SeedTelco(t, "TELCO_A", "")
	idem := repo.Idempotency{}
	ctx := platform.WithTenant(context.Background(), "TELCO_A")

	const workers = 16
	var wg sync.WaitGroup
	storedCount := make(chan bool, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = repo.WithTenantTx(ctx, db.App, func(tx pgx.Tx) error {
				_, stored, err := idem.PutIfAbsent(ctx, tx, entity.IdempotencyRecord{
					TelcoID: "TELCO_A", Operation: "advance.create", IdemKey: "race-key",
					RequestHash: "h", ResponseStatus: 200 + i, ResponseBody: []byte(`{}`),
				})
				if err != nil {
					return err
				}
				storedCount <- stored
				return nil
			})
		}(i)
	}
	wg.Wait()
	close(storedCount)

	wins := 0
	for s := range storedCount {
		if s {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("exactly one concurrent duplicate may store, got %d", wins)
	}
	var n int
	if err := db.Admin.QueryRow(context.Background(),
		`SELECT count(*) FROM idempotency_records WHERE idem_key='race-key'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("want 1 stored record, got %d", n)
	}
}
