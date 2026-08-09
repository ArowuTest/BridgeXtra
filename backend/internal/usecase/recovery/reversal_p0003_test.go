package recovery_test

// BX-P0-003: recovery REVERSAL idempotency. Reverse claimed no idempotency on
// reversal_source_event_id, so a duplicate PARTIAL reversal double-booked: a second
// negative recovery_allocation (the allocations have no uniqueness backstop) while the
// RECOVERY_REVERSED journal deduped on its business-event-key — outstanding clawed back
// twice, ledger once, book != ledger (INV-016). The fix mirrors the forward R-P0-2 claim
// inside applyReversal, covering every apply path (immediate / auto-apply / RetryParked).

import (
	"context"
	"errors"
	"testing"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/invariants"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/recovery"
)

func negativeAllocCount(t *testing.T, f *fixture) int {
	t.Helper()
	var n int
	if err := f.db.Admin.QueryRow(context.Background(),
		`SELECT count(*) FROM recovery_allocations WHERE amount_minor < 0`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The KILLER test: an exact partial-reversal retry replays, never double-claws-back.
func TestBXP0003_PartialReversalExactRetry_NoDoubleClawback(t *testing.T) {
	f := newFixture(t, "rev_p0003")
	adv := f.activeAdvance(t) // outstanding 10000

	// Partial recovery 6000 -> outstanding 4000, PARTIALLY_RECOVERED.
	if res := f.ingest(t, "src-p3", 6_000); res.State != entity.RecoveryAllocated {
		t.Fatalf("recovery must allocate: %+v", res)
	}
	if _, o := f.advanceRow(t, adv.AdvanceID); o != 4_000 {
		t.Fatalf("after 6000 recovery, outstanding must be 4000, got %d", o)
	}

	// A PARTIAL reversal of 2000 APPLIES -> outstanding 6000.
	if rev := f.reverse(t, "rvsl-p3", "src-p3", 2_000); rev.Parked || rev.Replayed {
		t.Fatalf("first partial reversal must APPLY, got %+v", rev)
	}
	if _, o := f.advanceRow(t, adv.AdvanceID); o != 6_000 {
		t.Fatalf("after a 2000 reversal, outstanding must be 6000, got %d", o)
	}
	negBefore := negativeAllocCount(t, f)

	// THE KILLER: the EXACT same reversal again (telco retry) must REPLAY.
	again := f.reverse(t, "rvsl-p3", "src-p3", 2_000)
	if !again.Replayed {
		t.Fatalf("an exact reversal retry must replay, got %+v", again)
	}
	if _, o := f.advanceRow(t, adv.AdvanceID); o != 6_000 {
		t.Fatalf("a replayed reversal must NOT claw back twice — outstanding must stay 6000, got %d", o)
	}
	if negAfter := negativeAllocCount(t, f); negAfter != negBefore {
		t.Fatalf("a replayed reversal must write NO new negative allocation: before=%d after=%d", negBefore, negAfter)
	}
	assertBalancedBook(t, f)
	// INV-016 (book == ledger) and every other invariant hold after the duplicate partial.
	violations, err := (&invariants.Checker{Pool: f.db.Worker}).Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range violations {
		t.Errorf("invariant violation after duplicate partial reversal: %s", v)
	}
}

// A reversal id reused with a DIFFERENT payload (amount) is refused, not silently replayed.
func TestBXP0003_DivergentReversal_Refused(t *testing.T) {
	f := newFixture(t, "rev_p3_div")
	adv := f.activeAdvance(t)
	f.ingest(t, "src-p3d", 6_000)
	f.reverse(t, "rvsl-p3d", "src-p3d", 2_000) // first: applies, outstanding 6000

	_, err := f.rec.Reverse(tenantCtx(), recovery.ReverseCmd{
		ReversalSourceEventID: "rvsl-p3d", OriginalSourceEventID: "src-p3d",
		Amount: entity.MustMoney(1_500, entity.NGN), CorrelationID: "cor-div",
	})
	if !errors.Is(err, recovery.ErrDivergentReversal) {
		t.Fatalf("a reversal id reused with a different amount must be refused (ErrDivergentReversal), got %v", err)
	}
	// The refused divergent reversal applied nothing — outstanding still 6000.
	if _, o := f.advanceRow(t, adv.AdvanceID); o != 6_000 {
		t.Fatalf("a refused divergent reversal must apply nothing: outstanding=%d, want 6000", o)
	}
}
