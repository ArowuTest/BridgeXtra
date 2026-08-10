package origination_test

// BX-MED-002: a confirm REPLAY must return the response the ORIGINAL confirm produced — the
// advance's persisted confirm-time state — not the row's since-progressed CURRENT state. An
// UNKNOWN confirm books the advance pending (exposure held); the resolver worker later settles
// it ACTIVE. A telco that retries the confirm under the SAME idempotency key (its first response
// was lost) must still be told what its request originally earned — FULFILMENT_UNKNOWN, poll the
// status route — never a fresh read that now reports ACTIVE. Handing back current state would let
// a lost-response retry silently "upgrade" a still-pending advance to settled in the caller's eyes.

import (
	"context"
	"testing"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/mno"
)

func TestBXMED002_ConfirmReplay_ReturnsOriginalResponse_NotProgressedState(t *testing.T) {
	// Sim holds longer than the adapter's client timeout, so the confirm sees a post-send timeout
	// the adapter conservatively classifies UNKNOWN (INV-009): the advance is booked
	// FULFILMENT_UNKNOWN with exposure held, awaiting the resolver.
	// A "TIMEOUT" token drives the sim's credit-then-hang fault (EDG-005): the recharge IS
	// recorded telco-side, but the sim holds the response past the adapter's client timeout, so
	// the confirm sees a post-send timeout and books FULFILMENT_UNKNOWN.
	f := newFixture(t, "med002_replay", 500*time.Millisecond, 100)
	f.seedSubscriber(t, "sub_med002", "tok_TIMEOUT_med002", 50_000)
	offers := f.offersFor(t, "tok_TIMEOUT_med002")
	cmd := acceptFor(offers[0], "tok_TIMEOUT_med002", "med002-idem", "cor-med002")

	r1, err := f.svc.Confirm(tenantCtx(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	if r1.Advance.State != entity.AdvFulfilmentUnknown {
		t.Fatalf("setup: a post-send timeout must book FULFILMENT_UNKNOWN, got %s", r1.Advance.State)
	}
	origOutstanding := r1.Advance.Outstanding.Amount()

	// The resolver worker later settles the SAME advance ACTIVE (the recharge did land) — the
	// real UNKNOWN->ACTIVE progression, via the exact path the resolver uses.
	var attemptID string
	if err := f.db.Admin.QueryRow(context.Background(),
		`SELECT attempt_id FROM fulfilment_attempts WHERE advance_id=$1`, r1.Advance.AdvanceID).Scan(&attemptID); err != nil {
		t.Fatal(err)
	}
	settled, err := f.svc.ResolveOutcome(tenantCtx(), r1.Advance.AdvanceID, attemptID,
		mno.Result{Outcome: mno.OutcomeConfirmed, TelcoReference: "tref-med002"})
	if err != nil {
		t.Fatal(err)
	}
	if settled.State != entity.AdvActive {
		t.Fatalf("setup: resolver must settle UNKNOWN -> ACTIVE, got %s", settled.State)
	}
	// The row genuinely progressed — prove current-state and the persisted response now diverge,
	// so the assertion below is meaningful (not a coincidence of equal states).
	var dbState string
	if err := f.db.Admin.QueryRow(context.Background(),
		`SELECT state FROM advances WHERE advance_id=$1`, r1.Advance.AdvanceID).Scan(&dbState); err != nil {
		t.Fatal(err)
	}
	if dbState != "ACTIVE" {
		t.Fatalf("setup: advance row must have progressed to ACTIVE, got %s", dbState)
	}

	// The telco never saw r1 (lost response) and retries the SAME confirm under the SAME key.
	r2, err := f.svc.Confirm(tenantCtx(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	if !r2.Replayed || r2.Advance.AdvanceID != r1.Advance.AdvanceID {
		t.Fatalf("retry must replay the original advance, got %+v", r2)
	}
	// The load-bearing assertion (BX-MED-002): the replay carries the ORIGINAL confirm-time
	// state (UNKNOWN), NOT the row's since-progressed ACTIVE. Mutation proof: revert the confirm
	// idem-claim replay path (replayConfirmAdvance -> s.advances.GetByIdemKey) and this returns
	// the current ACTIVE row — RED.
	if r2.Advance.State != entity.AdvFulfilmentUnknown {
		t.Fatalf("BX-MED-002: replay must return the ORIGINAL response state UNKNOWN, got %s (row is now ACTIVE)", r2.Advance.State)
	}
	if got := r2.Advance.Outstanding.Amount(); got != origOutstanding {
		t.Fatalf("replay must carry the original response's outstanding %d, got %d", origOutstanding, got)
	}
}

// A fresh ACTIVE confirm then a duplicate must still replay ACTIVE — the persisted response for a
// clean settlement equals the original outcome. Guards against the fix over-rotating and pinning
// every replay to a pending/stale snapshot.
func TestBXMED002_ConfirmReplay_ActiveStaysActive(t *testing.T) {
	f := newFixture(t, "med002_active", 0, 2_000)
	offers := f.offersFor(t, "tok_sim_0001")
	cmd := acceptFor(offers[0], "tok_sim_0001", "med002-active", "cor-med002-a")

	r1, err := f.svc.Confirm(tenantCtx(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	if r1.Advance.State != entity.AdvActive {
		t.Fatalf("setup: fresh confirm must be ACTIVE, got %s", r1.Advance.State)
	}
	r2, err := f.svc.Confirm(tenantCtx(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	if !r2.Replayed || r2.Advance.State != entity.AdvActive || r2.Advance.AdvanceID != r1.Advance.AdvanceID {
		t.Fatalf("replay of a settled confirm must return the original ACTIVE advance, got %+v", r2)
	}
	if r2.Advance.Outstanding.Amount() != r1.Advance.Outstanding.Amount() {
		t.Fatalf("replay outstanding = %d, want %d", r2.Advance.Outstanding.Amount(), r1.Advance.Outstanding.Amount())
	}
}
