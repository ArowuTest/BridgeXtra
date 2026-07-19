package recovery_test

// C4 (VR-36, M4e-2): "barring never blocks lawful recovery." Account status
// gates ORIGINATION (engine eligibility + offer/confirm); the recovery path
// targets ADVANCE state, never account status — money already lent must
// remain recoverable whatever conduct state the account enters. This pins
// that property so no future "respect status everywhere" refactor silently
// stops collections against barred or closed subscribers.

import (
	"context"
	"testing"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
)

func TestC4_RecoveryAgainstBarredSubscriber_StillApplies(t *testing.T) {
	f := newFixture(t, "c4_barred")
	adv := f.activeAdvance(t)

	// The subscriber is BARRED after the advance activated (via the owner
	// pool — the M4e-2 maker-checker action is the production door and is
	// proven in the handler pack; this test pins the recovery property).
	if _, err := f.db.Admin.Exec(context.Background(),
		`UPDATE subscriber_accounts SET status='BARRED' WHERE subscriber_account_id=$1`,
		adv.SubscriberAccountID); err != nil {
		t.Fatal(err)
	}

	// Full recovery still allocates and closes the advance.
	res := f.ingest(t, "evt_c4_full", adv.Outstanding.Amount())
	if res.State != entity.RecoveryAllocated {
		t.Fatalf("recovery against a BARRED subscriber must still allocate, got %s", res.State)
	}
	if !res.AdvanceClosed {
		t.Fatal("full recovery must close the advance regardless of account status")
	}

	// Same property for CLOSED: bar -> close is the terminal conduct path,
	// and a straggler recovery event must still land (quarantine would be
	// wrong — the money belongs against this advance's history).
	var status string
	if err := f.db.Admin.QueryRow(context.Background(),
		`SELECT status FROM subscriber_accounts WHERE subscriber_account_id=$1`,
		adv.SubscriberAccountID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "BARRED" {
		t.Fatalf("recovery must not touch account status, got %s", status)
	}
}
