package recon

// R-P0-6 Slice D1 (EDG-006): a key reported BOTH FAILED and SUCCESS in the same
// window is internally contradictory. The SUCCESS must NOT be reconciled as a
// clean MATCHED — it is flagged BREAK_CONTRADICTORY_TELCO_STATUS for ops.

import (
	"context"
	"testing"
)

func failedTxn(advID string, minor int64) telcoTransaction {
	return telcoTransaction{
		PlatformRequestID: advID, TelcoReference: "TR-fail-" + advID, FaceValueMinor: minor,
		Currency: "NGN", Status: "FAILED", CreditedAt: reconPast(),
	}
}

func TestRP06D1_ContradictoryStatus_NotSilentlyMatched(t *testing.T) {
	// adv_x: the telco reports BOTH a FAILED and a SUCCESS for the same key.
	txns := []telcoTransaction{failedTxn("adv_x", 5_000), matchTxn("adv_x", 5_000)}
	f := newReconFixture(t, "rp06d1_contra", txns)
	f.seedConfirmedAdvance(t, "adv_x", 5_000, "NGN", "TR-adv_x")
	ctx := context.Background()

	sum, err := f.svc.RunFulfilment(ctx, "SIM_NG", "prg_sim_airtime01")
	if err != nil {
		t.Fatal(err)
	}
	if sum.Matched != 0 {
		t.Fatalf("a contradictory SUCCESS must NOT be reconciled as MATCHED, got matched=%d", sum.Matched)
	}
	if sum.Contradictory != 1 {
		t.Fatalf("the FAILED+SUCCESS contradiction must be flagged once, got %d", sum.Contradictory)
	}
	if f.statusCount(t, "BREAK_CONTRADICTORY_TELCO_STATUS") != 1 {
		t.Fatalf("a BREAK_CONTRADICTORY_TELCO_STATUS item must be recorded, got %d", f.statusCount(t, "BREAK_CONTRADICTORY_TELCO_STATUS"))
	}
	if f.statusCount(t, "MATCHED") != 0 {
		t.Fatal("no MATCHED item may exist for the contradictory key")
	}
}

// A clean SUCCESS (no FAILED for the key) still matches — the guard only fires
// on an actual contradiction.
func TestRP06D1_CleanSuccess_StillMatches(t *testing.T) {
	f := newReconFixture(t, "rp06d1_clean", []telcoTransaction{matchTxn("adv_ok", 5_000)})
	f.seedConfirmedAdvance(t, "adv_ok", 5_000, "NGN", "TR-adv_ok")
	ctx := context.Background()

	sum, err := f.svc.RunFulfilment(ctx, "SIM_NG", "prg_sim_airtime01")
	if err != nil {
		t.Fatal(err)
	}
	if sum.Matched != 1 || sum.Contradictory != 0 {
		t.Fatalf("a non-contradictory success must match, got matched=%d contradictory=%d", sum.Matched, sum.Contradictory)
	}
}
