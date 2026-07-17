package repo_test

// SF-3 tenant-isolation negative pack (V2-TST-005, V2-TEN-002/003, EDG-026).
// Every test here ATTEMPTS a cross-tenant violation through the real tcp_app
// role and asserts it fails. These run with -race in CI from M0 onward.

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
)

func seedTwoTenantsWithProgrammes(t *testing.T, db *testutil.DB) {
	t.Helper()
	db.SeedTelco(t, "TELCO_A", "key-a")
	db.SeedTelco(t, "TELCO_B", "key-b")
	progs := repo.Programmes{}
	for _, tc := range []string{"TELCO_A", "TELCO_B"} {
		ctx := platform.WithTenant(context.Background(), tc)
		if err := repo.WithTenantTx(ctx, db.App, func(tx pgx.Tx) error {
			return progs.Create(ctx, tx, entity.Programme{
				ProgrammeID: "prg_" + tc, TelcoID: tc, Code: "AIRTIME01",
				Name: "Airtime Advance", Status: entity.ProgrammeDraft,
			})
		}); err != nil {
			t.Fatalf("seed programme for %s: %v", tc, err)
		}
	}
}

func TestV2_TEN_005_TenantA_CannotReadTenantB_Rows(t *testing.T) {
	db := testutil.MustSetup(t, "iso_read")
	seedTwoTenantsWithProgrammes(t, db)
	progs := repo.Programmes{}

	ctxA := platform.WithTenant(context.Background(), "TELCO_A")
	// Direct fetch of B's row under A's context must be invisible (RLS).
	err := repo.WithTenantTx(ctxA, db.App, func(tx pgx.Tx) error {
		_, err := progs.GetByID(ctxA, tx, "prg_TELCO_B")
		return err
	})
	if !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("cross-tenant read must be NotFound, got: %v", err)
	}
	// List under A must contain exactly A's rows.
	var got []entity.Programme
	if err := repo.WithTenantTx(ctxA, db.App, func(tx pgx.Tx) error {
		var e error
		got, e = progs.ListForTenant(ctxA, tx)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].TelcoID != "TELCO_A" {
		t.Fatalf("tenant A list leaked rows: %+v", got)
	}
}

func TestV2_TEN_005_TenantA_CannotWriteTenantB_Rows(t *testing.T) {
	db := testutil.MustSetup(t, "iso_write")
	seedTwoTenantsWithProgrammes(t, db)
	progs := repo.Programmes{}

	ctxA := platform.WithTenant(context.Background(), "TELCO_A")
	// UPDATE of B's row under A's context: RLS hides it -> NotFound, never success.
	err := repo.WithTenantTx(ctxA, db.App, func(tx pgx.Tx) error {
		return progs.UpdateStatus(ctxA, tx, "prg_TELCO_B", entity.ProgrammeSuspended)
	})
	if !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("cross-tenant update must be NotFound, got: %v", err)
	}
	// INSERT with B's telco_id under A's context: WITH CHECK policy must reject.
	err = repo.WithTenantTx(ctxA, db.App, func(tx pgx.Tx) error {
		return progs.Create(ctxA, tx, entity.Programme{
			ProgrammeID: "prg_forged", TelcoID: "TELCO_B", Code: "FORGED",
			Name: "forged", Status: entity.ProgrammeDraft,
		})
	})
	if err == nil {
		t.Fatal("INSERT with foreign telco_id must be rejected by RLS WITH CHECK")
	}
	// Verify (as admin, which sees everything) that B's row is untouched and no forged row exists.
	var status string
	if err := db.Admin.QueryRow(context.Background(),
		`SELECT status FROM programmes WHERE programme_id = 'prg_TELCO_B'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != string(entity.ProgrammeDraft) {
		t.Fatalf("tenant B row was modified cross-tenant: %s", status)
	}
	var forged int
	_ = db.Admin.QueryRow(context.Background(),
		`SELECT count(*) FROM programmes WHERE programme_id = 'prg_forged'`).Scan(&forged)
	if forged != 0 {
		t.Fatal("forged cross-tenant row was inserted")
	}
}

func TestZeroConfigFloor_NoTenantContext_SeesNothing(t *testing.T) {
	// Missing tenant setting must mean ZERO rows, never all rows (fail closed).
	db := testutil.MustSetup(t, "iso_floor")
	seedTwoTenantsWithProgrammes(t, db)

	var n int
	if err := db.App.QueryRow(context.Background(),
		`SELECT count(*) FROM programmes`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("no-context query must see 0 rows, saw %d — RLS floor broken", n)
	}
	// And the Go layer refuses to even open a tenant tx without context.
	err := repo.WithTenantTx(context.Background(), db.App, func(tx pgx.Tx) error { return nil })
	if err == nil {
		t.Fatal("WithTenantTx must refuse a context with no tenant")
	}
}

func TestV2_TEN_005_IdempotencyAndOutbox_TenantScoped(t *testing.T) {
	db := testutil.MustSetup(t, "iso_idem_outbox")
	seedTwoTenantsWithProgrammes(t, db)
	idem := repo.Idempotency{}
	outbox := repo.Outbox{}

	ctxA := platform.WithTenant(context.Background(), "TELCO_A")
	ctxB := platform.WithTenant(context.Background(), "TELCO_B")

	// Tenant A writes one idempotency record and one outbox event.
	if err := repo.WithTenantTx(ctxA, db.App, func(tx pgx.Tx) error {
		if _, _, err := idem.PutIfAbsent(ctxA, tx, entity.IdempotencyRecord{
			TelcoID: "TELCO_A", Operation: "op", IdemKey: "k1",
			RequestHash: "h", ResponseStatus: 200, ResponseBody: []byte(`{}`),
		}); err != nil {
			return err
		}
		return outbox.Append(ctxA, tx, entity.OutboxEvent{
			ID: platform.NewID("evt"), TelcoID: "TELCO_A", AggregateType: "Test",
			AggregateID: "agg1", EventType: "M0.Ping", SchemaVersion: 1,
			Payload: []byte(`{}`), OccurredAt: timeNow(),
		})
	}); err != nil {
		t.Fatal(err)
	}

	// Tenant B must not see either row.
	if err := repo.WithTenantTx(ctxB, db.App, func(tx pgx.Tx) error {
		if _, err := idem.Get(ctxB, tx, "TELCO_A", "op", "k1"); !errors.Is(err, repo.ErrNotFound) {
			t.Errorf("tenant B read tenant A idempotency record: %v", err)
		}
		var n int
		if err := tx.QueryRow(ctxB, `SELECT count(*) FROM outbox`).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Errorf("tenant B sees %d outbox rows belonging to A", n)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// The worker role (BYPASSRLS) DOES see it — that is its documented purpose.
	var n int
	if err := db.Worker.QueryRow(context.Background(), `SELECT count(*) FROM outbox`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("worker should see 1 outbox row, saw %d", n)
	}
}
