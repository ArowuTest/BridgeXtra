package recon

// S3-C2 re-origination hold — the clear-precision money-safety proof (I12 core):
// the RECOVERY recon clears a subscriber's hold ONLY when the feed CONFIRMED that
// subscriber's recovery (its token MATCHED); a phantom (booked, unconfirmed) stays
// held so the borrower stays blocked from re-borrowing.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
)

func (f *reconFixture) seedSubscriber(t *testing.T, subID, token string) {
	t.Helper()
	if _, err := f.db.Admin.Exec(context.Background(),
		`INSERT INTO subscriber_accounts (subscriber_account_id, telco_id, msisdn_token, status)
		 VALUES ($1,'SIM_NG',$2,'ACTIVE')`, subID, token); err != nil {
		t.Fatalf("seed subscriber: %v", err)
	}
}

func (f *reconFixture) seedHold(t *testing.T, subID, advID, evID string) {
	t.Helper()
	if _, err := f.db.Admin.Exec(context.Background(),
		`INSERT INTO recovery_unconfirmed_closes (id, telco_id, subscriber_account_id, advance_id, recovery_event_id)
		 VALUES ('ruc_'||$3, 'SIM_NG', $1, $2, $3)`, subID, advID, evID); err != nil {
		t.Fatalf("seed hold: %v", err)
	}
}

func (f *reconFixture) holdCleared(t *testing.T, evID string) bool {
	t.Helper()
	var cleared *string
	if err := f.db.Admin.QueryRow(context.Background(),
		`SELECT cleared_at::text FROM recovery_unconfirmed_closes WHERE recovery_event_id=$1`, evID).Scan(&cleared); err != nil {
		t.Fatalf("read hold: %v", err)
	}
	return cleared != nil
}

func TestS3C2_HoldClear_OnlyOnMatch(t *testing.T) {
	f := newRecoveryFixture(t, "s3c2_holdclear")
	// subA's recovery is confirmed by the feed (token MATCHED); subB's is a phantom
	// (booked, no feed row -> MISSING_TELCO).
	f.seedSubscriber(t, "subA", "tokA")
	f.seedSubscriber(t, "subB", "tokB")
	f.seedRecoveryEvent(t, "evA", "tokA", 500, "subA")
	f.seedRecoveryEvent(t, "evB", "tokB", 500, "subB")
	f.seedHold(t, "subA", "advA", "evA")
	f.seedHold(t, "subB", "advB", "evB")
	f.seedFeedRow(t, "tokA", 500) // confirms subA only

	sum := f.reconcileDay(t)
	if sum.Matched != 1 || sum.MissingTelco != 1 {
		t.Fatalf("want 1 matched (subA) + 1 phantom (subB), got %+v", sum)
	}

	if !f.holdCleared(t, "evA") {
		t.Fatal("subA's hold must be CLEARED — the feed confirmed the recovery")
	}
	if f.holdCleared(t, "evB") {
		t.Fatal("subB's hold must STAY held — the recovery is a phantom the feed did not confirm")
	}

	// The repo precondition sees the surviving hold (subB blocked, subA free).
	tctx := platform.WithTenant(context.Background(), "SIM_NG")
	if err := repo.WithTenantTx(tctx, f.db.App, func(tx pgx.Tx) error {
		if held, _ := (repo.RecoveryHolds{}).HasUncleared(context.Background(), tx, "subB"); !held {
			t.Fatal("subB must still be held")
		}
		if held, _ := (repo.RecoveryHolds{}).HasUncleared(context.Background(), tx, "subA"); held {
			t.Fatal("subA must no longer be held")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
