package rechargehold_test

// Phase 1 S2.3a — the governed HELD-release flow. Falsification pack: the
// four-eyes rule (same actor refused), release-without-request refused, reject
// never ingests, double-approve converges idempotently on ONE recovery event,
// and the crash-retry path (event already ingested, hold still HELD) completes
// the transition instead of double-ingesting.

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/ledger"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/rechargehold"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/recovery"
)

const holdTelco = "SIM_NG"

func newHoldFixture(t *testing.T, suffix string) (*rechargehold.Service, *recovery.Service, *testutil.DB) {
	t.Helper()
	db := testutil.MustSetup(t, suffix)
	appCfg := configsvc.New(db.App)
	rec := recovery.New(db.App, appCfg, ledger.New(appCfg), slog.Default())
	return rechargehold.New(db.App, rec, slog.Default()), rec, db
}

// seedHold parks one held recharge and returns its id.
func seedHold(t *testing.T, db *testutil.DB, src string, amountMinor int64) string {
	t.Helper()
	tctx := platform.WithTenant(context.Background(), holdTelco)
	var heldID string
	if err := repo.WithTenantTx(tctx, db.App, func(tx pgx.Tx) error {
		_, err := (repo.HeldRecharge{}).Hold(context.Background(), tx, repo.HeldEvent{
			TelcoID: holdTelco, SourceEventID: src, MSISDNToken: "tok_hold_1",
			AmountMinor: amountMinor, Currency: "NGN", OccurredAt: time.Now().UTC(),
			Reason: repo.HeldReasonPerEventClamp,
		})
		if err != nil {
			return err
		}
		return tx.QueryRow(context.Background(),
			`SELECT held_id FROM held_recharge_events WHERE source_event_id=$1`, src).Scan(&heldID)
	}); err != nil {
		t.Fatal(err)
	}
	return heldID
}

func recoveryCount(t *testing.T, db *testutil.DB, src string) int {
	t.Helper()
	var n int
	if err := db.Admin.QueryRow(context.Background(),
		`SELECT count(*) FROM recovery_events WHERE source_event_id=$1`, src).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func holdStatus(t *testing.T, db *testutil.DB, heldID string) string {
	t.Helper()
	var s string
	if err := db.Admin.QueryRow(context.Background(),
		`SELECT status FROM held_recharge_events WHERE held_id=$1`, heldID).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestS23_RequestThenApprove_IngestsOnce(t *testing.T) {
	svc, _, db := newHoldFixture(t, "hold_happy")
	id := seedHold(t, db, "wh:h1", 99_000_000)
	ctx := context.Background()

	if err := svc.RequestRelease(ctx, holdTelco, id, "maker", "verified genuine bulk recharge"); err != nil {
		t.Fatalf("request: %v", err)
	}
	res, err := svc.ApproveRelease(ctx, holdTelco, id, "checker")
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if res.RecoveryEventID == "" {
		t.Fatal("release must ingest a recovery event")
	}
	if got := holdStatus(t, db, id); got != "RELEASED" {
		t.Fatalf("hold must be RELEASED, got %s", got)
	}
	if n := recoveryCount(t, db, "wh:h1"); n != 1 {
		t.Fatalf("exactly one recovery event, got %d", n)
	}
}

func TestS23_SameActor_Refused(t *testing.T) {
	svc, _, db := newHoldFixture(t, "hold_sameactor")
	id := seedHold(t, db, "wh:h2", 1000)
	ctx := context.Background()

	if err := svc.RequestRelease(ctx, holdTelco, id, "alice", "r"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApproveRelease(ctx, holdTelco, id, "alice"); !errors.Is(err, rechargehold.ErrSameActor) {
		t.Fatalf("same-actor approval must be refused (four-eyes), got %v", err)
	}
	if got := holdStatus(t, db, id); got != "HELD" {
		t.Fatalf("hold must remain HELD after refusal, got %s", got)
	}
	if n := recoveryCount(t, db, "wh:h2"); n != 0 {
		t.Fatalf("a refused release must ingest NOTHING, got %d", n)
	}
}

func TestS23_ApproveWithoutRequest_Refused(t *testing.T) {
	svc, _, db := newHoldFixture(t, "hold_noreq")
	id := seedHold(t, db, "wh:h3", 1000)
	if _, err := svc.ApproveRelease(context.Background(), holdTelco, id, "checker"); !errors.Is(err, rechargehold.ErrNotActionable) {
		t.Fatalf("approval without a maker request must be refused, got %v", err)
	}
	if n := recoveryCount(t, db, "wh:h3"); n != 0 {
		t.Fatal("nothing may be ingested without the maker step")
	}
}

func TestS23_Reject_NeverIngests_ThenApproveRefused(t *testing.T) {
	svc, _, db := newHoldFixture(t, "hold_reject")
	id := seedHold(t, db, "wh:h4", 1000)
	ctx := context.Background()

	if err := svc.RequestRelease(ctx, holdTelco, id, "maker", "r"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Reject(ctx, holdTelco, id, "maker", "withdrawn — looks like a scaling bug"); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if got := holdStatus(t, db, id); got != "REJECTED" {
		t.Fatalf("hold must be REJECTED, got %s", got)
	}
	if _, err := svc.ApproveRelease(ctx, holdTelco, id, "checker"); !errors.Is(err, rechargehold.ErrNotActionable) {
		t.Fatalf("approving a rejected hold must be refused, got %v", err)
	}
	if n := recoveryCount(t, db, "wh:h4"); n != 0 {
		t.Fatalf("a rejected hold must NEVER be ingested, got %d", n)
	}
}

func TestS23_DoubleApprove_IdempotentSingleIngest(t *testing.T) {
	svc, _, db := newHoldFixture(t, "hold_double")
	id := seedHold(t, db, "wh:h5", 1000)
	ctx := context.Background()

	if err := svc.RequestRelease(ctx, holdTelco, id, "maker", "r"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApproveRelease(ctx, holdTelco, id, "checker"); err != nil {
		t.Fatalf("first approve: %v", err)
	}
	// A retried/duplicate approval converges: no error, still ONE recovery event.
	if _, err := svc.ApproveRelease(ctx, holdTelco, id, "checker"); err != nil {
		t.Fatalf("retried approve must be idempotent, got %v", err)
	}
	if n := recoveryCount(t, db, "wh:h5"); n != 1 {
		t.Fatalf("exactly one recovery event after double approve, got %d", n)
	}
	if got := holdStatus(t, db, id); got != "RELEASED" {
		t.Fatalf("hold must be RELEASED, got %s", got)
	}
}

// Crash-retry: the event was ingested but the claim never committed (crash
// between ingest and claim). A retried approval must replay the ingest
// byte-exact (no second event) and complete the transition.
func TestS23_CrashRetry_IngestedButHeld_Converges(t *testing.T) {
	svc, rec, db := newHoldFixture(t, "hold_crash")
	id := seedHold(t, db, "wh:h6", 1000)
	ctx := context.Background()

	if err := svc.RequestRelease(ctx, holdTelco, id, "maker", "r"); err != nil {
		t.Fatal(err)
	}
	// Simulate the crash window: the ingest happened, the hold is still HELD.
	// The recovery dedup hash covers occurred_at, so the "crashed" ingest must
	// use the STORED hold timestamp (as ApproveRelease will re-read it) — a
	// fresh now() would make the retry a false divergence instead of a replay.
	var storedAt time.Time
	if err := db.Admin.QueryRow(ctx,
		`SELECT occurred_at FROM held_recharge_events WHERE held_id=$1`, id).Scan(&storedAt); err != nil {
		t.Fatal(err)
	}
	tctx := platform.WithTenant(ctx, holdTelco)
	if _, err := rec.Ingest(tctx, recovery.IngestCmd{
		SourceEventID: "wh:h6", MSISDNToken: "tok_hold_1",
		Amount: entity.MustMoney(1000, entity.NGN), OccurredAt: storedAt,
		CorrelationID: "rel-" + id,
	}); err != nil {
		t.Fatal(err)
	}
	if got := holdStatus(t, db, id); got != "HELD" {
		t.Fatalf("precondition: hold still HELD, got %s", got)
	}

	res, err := svc.ApproveRelease(ctx, holdTelco, id, "checker")
	if err != nil {
		t.Fatalf("retried approval must converge: %v", err)
	}
	if !res.Replayed {
		t.Fatal("the retried ingest must be a byte-exact replay, not a new event")
	}
	if n := recoveryCount(t, db, "wh:h6"); n != 1 {
		t.Fatalf("exactly one recovery event after crash-retry, got %d", n)
	}
	if got := holdStatus(t, db, id); got != "RELEASED" {
		t.Fatalf("hold must be RELEASED after convergence, got %s", got)
	}
}
