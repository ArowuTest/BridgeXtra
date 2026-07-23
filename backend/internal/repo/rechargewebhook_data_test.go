package repo_test

// Phase 1 S2.2a — the recon-arming gate marker and the HELD-recharge queue.
// The RECOVERY layer is NOT live until S3 arms it (structural gate); a hold is
// idempotent per event; the daily-ingested running total starts at zero.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
)

func TestS22_ReconArming_Gate(t *testing.T) {
	db := testutil.MustSetup(t, "repo_reconarm")
	r := &repo.ReconArming{Pool: db.Admin}
	ctx := context.Background()

	// Structural gate: RECOVERY is NOT live before S3 arms it.
	if live, err := r.IsLayerLive(ctx, "SIM_NG", repo.ReconLayerRecovery); err != nil || live {
		t.Fatalf("RECOVERY layer must not be live before arming, live=%v err=%v", live, err)
	}
	if err := r.SetLive(ctx, "SIM_NG", repo.ReconLayerRecovery); err != nil {
		t.Fatal(err)
	}
	if live, _ := r.IsLayerLive(ctx, "SIM_NG", repo.ReconLayerRecovery); !live {
		t.Fatal("must be live after SetLive")
	}
	// Idempotent.
	if err := r.SetLive(ctx, "SIM_NG", repo.ReconLayerRecovery); err != nil {
		t.Fatalf("SetLive must be idempotent: %v", err)
	}
	// Disarm stops ingestion immediately.
	if err := r.SetDown(ctx, "SIM_NG", repo.ReconLayerRecovery); err != nil {
		t.Fatal(err)
	}
	if live, _ := r.IsLayerLive(ctx, "SIM_NG", repo.ReconLayerRecovery); live {
		t.Fatal("must be down after SetDown")
	}
}

func TestS22_HeldRecharge_HoldIdempotentAndDailyZero(t *testing.T) {
	db := testutil.MustSetup(t, "repo_held")
	ctx := platform.WithTenant(context.Background(), "SIM_NG")

	ev := repo.HeldEvent{
		TelcoID: "SIM_NG", SourceEventID: "wh:e1", MSISDNToken: "tok1",
		AmountMinor: 99_999_999, Currency: "NGN", OccurredAt: time.Now().UTC(),
		Reason: repo.HeldReasonPerEventClamp,
	}

	var created1, created2 bool
	var daily int64
	if err := repo.WithTenantTx(ctx, db.App, func(tx pgx.Tx) error {
		var e error
		if daily, e = (repo.HeldRecharge{}).DailyIngestedMinor(ctx, tx, "SIM_NG"); e != nil {
			return e
		}
		created1, e = (repo.HeldRecharge{}).Hold(ctx, tx, ev)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.WithTenantTx(ctx, db.App, func(tx pgx.Tx) error {
		var e error
		created2, e = (repo.HeldRecharge{}).Hold(ctx, tx, ev)
		return e
	}); err != nil {
		t.Fatal(err)
	}

	if daily != 0 {
		t.Fatalf("no webhook recoveries yet, daily total must be 0, got %d", daily)
	}
	if !created1 {
		t.Fatal("first hold must create a row")
	}
	if created2 {
		t.Fatal("a duplicate hold of the same event must be idempotent (no second row)")
	}
}
