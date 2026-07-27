//go:build simseed_loop

package simseed

import (
	"context"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/recon"
)

func subIDByToken(t *testing.T, db *testutil.DB, token string) string {
	t.Helper()
	ctx := platform.WithTenant(context.Background(), SyntheticTelco)
	var id string
	if err := repo.WithTenantTx(ctx, db.App, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT subscriber_account_id FROM subscriber_accounts WHERE msisdn_token=$1 AND effective_to IS NULL`, token).Scan(&id)
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func holdUncleared(t *testing.T, db *testutil.DB, subID string) bool {
	t.Helper()
	ctx := platform.WithTenant(context.Background(), SyntheticTelco)
	var held bool
	if err := repo.WithTenantTx(ctx, db.App, func(tx pgx.Tx) error {
		h, e := (repo.RecoveryHolds{}).HasUncleared(ctx, tx, subID)
		held = h
		return e
	}); err != nil {
		t.Fatal(err)
	}
	return held
}

func countRows(t *testing.T, db *testutil.DB, q string) int {
	t.Helper()
	ctx := platform.WithTenant(context.Background(), SyntheticTelco)
	var n int
	if err := repo.WithTenantTx(ctx, db.App, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, q).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

// Slice 3 (condition-4 closer): the loop's recoveries flow through the REAL recovery
// usecase (wh:loop-) with matching EOD feed rows, and ReconcileRecoveryDay reconciles
// them MATCHED — proving the loop-produced money reconciles against the independent
// telco source. Holds are created + cleared; the layer never arms.
func TestLoop_Slice3_RecoveryReconcilesMatched(t *testing.T) {
	db := testutil.MustSetup(t, "simseed_loop_s3")
	ctx := context.Background()
	day := recentBusinessDay()
	plan := LoopPlan{Seed: "loop-s3", BusinessDay: day, ProgrammeID: "prg_sim_airtime01", Repeat: 1}

	res, err := RunLoop(ctx, db.App, plan)
	if err != nil {
		t.Fatalf("RunLoop: %v", err)
	}
	if res.Recovered != 3 { // good-payer (full) + varied (half) + thin (full); defaulter/declined none
		t.Fatalf("want 3 recoveries, got %d (%+v)", res.Recovered, res)
	}

	loopSeed := plan.Seed + "|loop"

	// Namespace disjoint (R4): every loop recovery event is wh:loop-%; zero reversal
	// (negative) allocations (R5), so platform NET == gross == the feed.
	if whAll := countRows(t, db, `SELECT count(*) FROM recovery_events WHERE source_event_id LIKE 'wh:%'`); whAll != 3 {
		t.Fatalf("want 3 wh:%% recovery events, got %d", whAll)
	}
	if whLoop := countRows(t, db, `SELECT count(*) FROM recovery_events WHERE source_event_id LIKE 'wh:loop-%'`); whLoop != 3 {
		t.Fatalf("all loop recovery events must be wh:loop-%%, got %d", whLoop)
	}
	if neg := countRows(t, db, `SELECT count(*) FROM recovery_allocations WHERE amount_minor < 0`); neg != 0 {
		t.Fatalf("loop path must have zero reversal (negative) allocations, got %d", neg)
	}

	// Hold ordering (R3): the good-payer's full close leaves an uncleared
	// re-origination hold BEFORE recon.
	goodSub := subIDByToken(t, db, msisdnToken(loopSeed, 0))
	if !holdUncleared(t, db, goodSub) {
		t.Fatal("good-payer full-close must leave an uncleared re-origination hold before recon")
	}

	// RECON MATCHED: reconcile the loop's wh:% recoveries against the EOD feed the
	// loop emitted — all MATCHED, zero breaks.
	svc := recon.New(db.App, configsvc.New(db.App), slog.Default())
	sum, err := svc.ReconcileRecoveryDay(ctx, SyntheticTelco, day)
	if err != nil {
		t.Fatalf("ReconcileRecoveryDay: %v", err)
	}
	if sum.MissingTelco != 0 || sum.MissingPlatform != 0 || sum.AmountMismatch != 0 {
		t.Fatalf("recon must be all-MATCHED (0 breaks), got %+v", sum)
	}
	if sum.Matched != 3 {
		t.Fatalf("want 3 MATCHED subjects (good/varied/thin), got %d", sum.Matched)
	}

	// Recon clears the feed-confirmed hold (R3).
	if holdUncleared(t, db, goodSub) {
		t.Fatal("a MATCHED recon must clear the good-payer's re-origination hold")
	}

	// Dormancy: the RECOVERY layer is never live — the loop arms nothing and
	// ReconcileRecoveryDay does not advance freshness.
	if live, _ := svc.Arming.IsLayerLive(ctx, SyntheticTelco, repo.ReconLayerRecovery); live {
		t.Fatal("RECOVERY layer must not be live — the loop arms nothing")
	}

	// Determinism/replay: a second identical run adds no new EOD feed rows.
	feedBefore := countRows(t, db, `SELECT count(*) FROM recovery_eod_feed`)
	if _, err := RunLoop(ctx, db.App, plan); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if feedAfter := countRows(t, db, `SELECT count(*) FROM recovery_eod_feed`); feedAfter != feedBefore {
		t.Fatalf("replay must not add feed rows — before=%d after=%d", feedBefore, feedAfter)
	}
}
