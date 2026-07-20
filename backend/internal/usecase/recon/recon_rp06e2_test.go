package recon

// R-P0-6 Slice E2: the signed, reproducible evidence pack. A pack is a canonical
// statement of a run (manifests + outcome + break resolutions) carrying a content
// hash that recomputes from the persisted state — reproducible and tamper-evident.

import (
	"context"
	"testing"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"

	"github.com/jackc/pgx/v5"
)

// The pack recomputes to the same hash from the same state (reproducible) and a
// change to the run's persisted outcome changes the hash (tamper-evident). The
// count and control total are surfaced as the distinct populations they are.
func TestRP06E2_EvidencePack_ReproducibleAndTamperEvident(t *testing.T) {
	// A SUCCESS (matched) and a FAILED record: 2 records counted, only the SUCCESS
	// amount in the control total — different populations. adv_missing has no telco
	// record → a break the pack carries.
	f := newReconFixture(t, "rp06e2_pack", []telcoTransaction{matchTxn("adv_ok", 5_000), failedTxn("adv_f", 9_999)})
	f.seedConfirmedAdvance(t, "adv_ok", 5_000, "NGN", "TR-adv_ok")
	f.seedConfirmedAdvance(t, "adv_missing", 5_000, "NGN", "TR-adv_missing")
	ctx := context.Background()

	sum, err := f.svc.ReconcilePeriod(ctx, "SIM_NG", "prg_sim_airtime01", winStart, winEnd)
	if err != nil {
		t.Fatal(err)
	}

	pack, err := f.svc.EvidencePack(ctx, "SIM_NG", sum.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if pack.PackHash == "" {
		t.Fatal("the pack must carry a content hash")
	}
	if pack.Source.RecordCount != 2 || pack.Source.ControlTotalMinor != 5_000 {
		t.Fatalf("record count (ALL) and control total (SUCCESS-only) are distinct populations: %+v", pack.Source)
	}
	if pack.PopulationNote == "" {
		t.Fatal("the pack must state the count-vs-control-total population distinction")
	}
	if len(pack.Breaks) != 1 {
		t.Fatalf("the pack must carry the run's break, got %d", len(pack.Breaks))
	}

	// Reproducible: a second build yields the same hash, and Verify confirms it.
	again, err := f.svc.EvidencePack(ctx, "SIM_NG", sum.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if again.PackHash != pack.PackHash {
		t.Fatalf("the pack must be reproducible: %s vs %s", again.PackHash, pack.PackHash)
	}
	ok, err := f.svc.VerifyEvidencePack(ctx, "SIM_NG", sum.RunID, pack.PackHash)
	if err != nil || !ok {
		t.Fatalf("Verify must accept the genuine hash: ok=%v err=%v", ok, err)
	}

	// Tamper-evident: injecting a fabricated resolution onto the break changes the
	// pack, so the archived hash no longer verifies. (The run header itself is
	// immutable — its supersede-once trigger blocks any non-supersede UPDATE — so
	// the item log is the tamper surface the pack must cover.)
	if _, err := f.db.Admin.Exec(ctx,
		`UPDATE recon_items SET resolution = 'INJECTED' WHERE run_id=$1 AND status LIKE 'BREAK_%'`, sum.RunID); err != nil {
		t.Fatal(err)
	}
	ok, err = f.svc.VerifyEvidencePack(ctx, "SIM_NG", sum.RunID, pack.PackHash)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("a tampered run must not verify against the original pack hash")
	}
}

// The pack captures the two-actor resolution of a break, and the hash reflects
// it (resolving a break changes the pack).
func TestRP06E2_EvidencePack_CapturesTwoActorResolution(t *testing.T) {
	// An advance with no telco record → BREAK_MISSING_TELCO.
	f := newReconFixture(t, "rp06e2_resolve", nil)
	f.seedConfirmedAdvance(t, "adv_missing", 5_000, "NGN", "TR-adv_missing")
	ctx := context.Background()

	sum, err := f.svc.ReconcilePeriod(ctx, "SIM_NG", "prg_sim_airtime01", winStart, winEnd)
	if err != nil {
		t.Fatal(err)
	}
	if sum.MissingTelco != 1 {
		t.Fatalf("setup: want one missing-telco break, got %+v", sum)
	}

	before, err := f.svc.EvidencePack(ctx, "SIM_NG", sum.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Breaks) != 1 || before.Breaks[0].ResolvedBy != "" {
		t.Fatalf("the open break must appear unresolved in the pack, got %+v", before.Breaks)
	}
	itemID := before.Breaks[0].ReconItemID

	// Resolve it through the two-actor path (E1).
	tctx := platform.WithTenant(ctx, "SIM_NG")
	if err := repo.WithTenantTx(tctx, f.db.App, func(tx pgx.Tx) error {
		if err := (repo.Breaks{}).Action(ctx, tx, "SIM_NG", itemID, "PROPOSE_RESOLVE", "maker", "telco statement received"); err != nil {
			return err
		}
		return (repo.Breaks{}).Action(ctx, tx, "SIM_NG", itemID, "APPROVE_RESOLVE", "checker", "reconciled off-platform")
	}); err != nil {
		t.Fatal(err)
	}

	after, err := f.svc.EvidencePack(ctx, "SIM_NG", sum.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Breaks[0].ResolutionProposedBy != "maker" || after.Breaks[0].ResolvedBy != "checker" {
		t.Fatalf("the pack must capture the two-actor resolution, got %+v", after.Breaks[0])
	}
	if after.PackHash == before.PackHash {
		t.Fatal("resolving a break must change the pack hash")
	}
}
