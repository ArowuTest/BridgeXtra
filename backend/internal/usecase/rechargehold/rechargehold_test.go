package rechargehold_test

// Phase 1 S2.3a — the governed HELD-release flow. Falsification pack: the
// four-eyes rule (same actor refused), release-without-request refused, reject
// never ingests, double-approve converges idempotently on ONE recovery event,
// and the crash-retry path (event already ingested, hold still HELD) completes
// the transition instead of double-ingesting.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
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

// BX-HIGH-002 (deterministic): once a hold is CLAIMED for release (RELEASE_IN_PROGRESS), a
// Reject can no longer win — MarkRejected requires HELD. This is the property that makes
// money-while-rejected impossible. Mutation proof: revert ApproveRelease to claim AFTER
// ingest (leave the hold HELD before booking) and a concurrent reject succeeds again.
func TestBXHIGH002_ClaimBeforeIngestBlocksReject(t *testing.T) {
	svc, _, db := newHoldFixture(t, "high002_claimblocks")
	id := seedHold(t, db, "wh:cb1", 1000)
	ctx := context.Background()
	if err := svc.RequestRelease(ctx, holdTelco, id, "maker", "r"); err != nil {
		t.Fatal(err)
	}
	// Claim the hold for release (HELD -> RELEASE_IN_PROGRESS), as ApproveRelease does BEFORE
	// it ingests.
	tctx := platform.WithTenant(ctx, holdTelco)
	if err := repo.WithTenantTx(tctx, db.App, func(tx pgx.Tx) error {
		ok, err := (repo.HeldRecharge{}).ClaimForRelease(ctx, tx, id, "checker")
		if err != nil || !ok {
			t.Fatalf("claim must succeed: ok=%v err=%v", ok, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := holdStatus(t, db, id); got != "RELEASE_IN_PROGRESS" {
		t.Fatalf("after claim the hold must be RELEASE_IN_PROGRESS, got %s", got)
	}
	// The reject now loses: a claimed hold is no longer HELD.
	if err := svc.Reject(ctx, holdTelco, id, "maker", "too late"); !errors.Is(err, rechargehold.ErrNotActionable) {
		t.Fatalf("rejecting a claimed (RELEASE_IN_PROGRESS) hold must be refused, got %v", err)
	}
	if got := holdStatus(t, db, id); got != "RELEASE_IN_PROGRESS" {
		t.Fatalf("a refused reject must leave the hold RELEASE_IN_PROGRESS, got %s", got)
	}
}

// BX-HIGH-002 (end-to-end, -race): fire Approve and Reject concurrently on each of many
// holds. Each must resolve cleanly — either RELEASED with exactly one recovery, or REJECTED
// with none. Money is NEVER booked against a hold that ends REJECTED, and a RELEASED hold
// always has its money. On the old ingest-then-flip ordering a reject that crossed the
// booking left money booked while REJECTED — this asserts that can no longer happen.
func TestBXHIGH002_ConcurrentApproveReject_NoMoneyWhileRejected(t *testing.T) {
	svc, _, db := newHoldFixture(t, "high002_race")
	ctx := context.Background()
	const n = 12
	ids := make([]string, n)
	srcs := make([]string, n)
	for i := 0; i < n; i++ {
		srcs[i] = fmt.Sprintf("wh:race%d", i)
		ids[i] = seedHold(t, db, srcs[i], 1000)
		if err := svc.RequestRelease(ctx, holdTelco, ids[i], "maker", "r"); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(2)
		go func(i int) { defer wg.Done(); <-start; _, _ = svc.ApproveRelease(ctx, holdTelco, ids[i], "checker") }(i)
		go func(i int) { defer wg.Done(); <-start; _ = svc.Reject(ctx, holdTelco, ids[i], "maker", "withdrawn") }(i)
	}
	close(start)
	wg.Wait()

	for i := 0; i < n; i++ {
		st := holdStatus(t, db, ids[i])
		rc := recoveryCount(t, db, srcs[i])
		switch st {
		case "RELEASED":
			if rc != 1 {
				t.Fatalf("hold %d RELEASED but recovery count = %d (want exactly 1)", i, rc)
			}
		case "REJECTED":
			if rc != 0 {
				t.Fatalf("hold %d REJECTED but %d recovery booked — money moved while rejected (BX-HIGH-002)", i, rc)
			}
		default:
			t.Fatalf("hold %d ended in %s (want RELEASED or REJECTED) — stuck/inconsistent", i, st)
		}
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
