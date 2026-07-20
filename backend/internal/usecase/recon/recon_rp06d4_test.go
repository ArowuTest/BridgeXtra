package recon

// R-P0-6 Slice D4 (multi-layer). The header / manifest / control-total /
// period-watermark / completeness / override / supersession machinery is
// layer-agnostic: a layer supplies only its name and how to fetch its
// platform-side money records (layerSpec). FULFILMENT is the reference impl and
// the only layer ARMED in production (see build/RECON_LAYER_COVERAGE.md — the
// other layers have no independent telco-side pull source, and fabricating one
// would be a stub). These tests prove the engine is genuinely layer-generic by
// driving a SECOND layer through the exact same code path.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// A layer whose platform side is supplied by the test — exercising the shared
// engine with a non-FULFILMENT layer name and an arbitrary platform set.
func testRecoverySpec(rec map[string]platformRecord) layerSpec {
	return layerSpec{
		name: "RECOVERY",
		fetchPlatform: func(ctx context.Context, tx pgx.Tx, programmeID string, ps, pe time.Time) (map[string]platformRecord, error) {
			return rec, nil
		},
	}
}

func genericTol() toleranceCfg {
	return toleranceCfg{
		MaxAmountMinor: 1_000_000, MinCompletenessRatio: 0.5,
		ReconLagSeconds: 300, RereconcileLookbackSeconds: 604_800,
	}
}

func (f *reconFixture) activeRunID(t *testing.T, layer string, periodStart time.Time) string {
	t.Helper()
	var id string
	if err := f.db.Admin.QueryRow(context.Background(),
		`SELECT run_id FROM recon_runs WHERE state='ACTIVE' AND layer=$1 AND period_start=$2`,
		layer, periodStart).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// Two layers reconcile the SAME programme+period through the shared engine and
// coexist as distinct ACTIVE runs — the framework keys everything on layer, so
// it is genuinely multi-layer rather than hardcoded to FULFILMENT.
func TestRP06D4_MultiLayer_CoexistDistinctLayers(t *testing.T) {
	f := newReconFixture(t, "rp06d4_multi", []telcoTransaction{matchTxn("adv_f", 5_000)})
	f.seedConfirmedAdvance(t, "adv_f", 5_000, "NGN", "TR-adv_f")
	ctx := context.Background()

	// FULFILMENT via the public path.
	if sum, err := f.svc.ReconcilePeriod(ctx, "SIM_NG", "prg_sim_airtime01", winStart, winEnd); err != nil || sum.Matched != 1 {
		t.Fatalf("fulfilment layer: %+v err=%v", sum, err)
	}

	// A SECOND layer ("RECOVERY") driven through the SAME engine with its own
	// layer-supplied platform set and its own telco source.
	recPlat := map[string]platformRecord{
		"rec1": {AdvanceID: "rec1", State: "ACTIVE", FaceValueMinor: 7_000, Currency: "NGN", TelcoReference: "TR-rec1"},
	}
	recTelco := []telcoTransaction{
		{PlatformRequestID: "rec1", TelcoReference: "TR-rec1", FaceValueMinor: 7_000, Currency: "NGN", Status: "SUCCESS", CreditedAt: reconPast()},
	}
	recSum, err := f.svc.reconcileLayer(ctx, testRecoverySpec(recPlat), "SIM_NG", "prg_sim_airtime01", winStart, winEnd, recTelco, genericTol())
	if err != nil {
		t.Fatal(err)
	}
	if recSum.Matched != 1 || recSum.SourceControlTotalMinor != 7_000 || recSum.PlatformControlTotalMinor != 7_000 {
		t.Fatalf("recovery layer must reconcile its own record through the shared engine, got %+v", recSum)
	}

	// Each layer has exactly one ACTIVE run for the same programme+period.
	byLayer := map[string]int{}
	rows, err := f.db.Admin.Query(ctx, `SELECT layer FROM recon_runs WHERE state='ACTIVE' AND period_start=$1`, winStart)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var l string
		if err := rows.Scan(&l); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		byLayer[l]++
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if byLayer["FULFILMENT"] != 1 || byLayer["RECOVERY"] != 1 {
		t.Fatalf("each layer must have exactly one ACTIVE run for the period, got %+v", byLayer)
	}
}

// A re-reconcile of ONE layer supersedes only that layer's run — the other
// layer's ACTIVE run is untouched (supersession is layer-scoped).
func TestRP06D4_ReReconcile_IsLayerScoped(t *testing.T) {
	f := newReconFixture(t, "rp06d4_scoped", []telcoTransaction{matchTxn("adv_f", 5_000)})
	f.seedConfirmedAdvance(t, "adv_f", 5_000, "NGN", "TR-adv_f")
	ctx := context.Background()

	if _, err := f.svc.ReconcilePeriod(ctx, "SIM_NG", "prg_sim_airtime01", winStart, winEnd); err != nil {
		t.Fatal(err)
	}
	fulfilBefore := f.activeRunID(t, "FULFILMENT", winStart)

	recPlat := map[string]platformRecord{
		"rec1": {AdvanceID: "rec1", State: "ACTIVE", FaceValueMinor: 7_000, Currency: "NGN", TelcoReference: "TR-rec1"},
	}
	recTelco := []telcoTransaction{
		{PlatformRequestID: "rec1", TelcoReference: "TR-rec1", FaceValueMinor: 7_000, Currency: "NGN", Status: "SUCCESS", CreditedAt: reconPast()},
	}
	first, err := f.svc.reconcileLayer(ctx, testRecoverySpec(recPlat), "SIM_NG", "prg_sim_airtime01", winStart, winEnd, recTelco, genericTol())
	if err != nil {
		t.Fatal(err)
	}
	// Re-reconcile RECOVERY (a genuine change: the amount corrects to 8,000).
	recPlat["rec1"] = platformRecord{AdvanceID: "rec1", State: "ACTIVE", FaceValueMinor: 8_000, Currency: "NGN", TelcoReference: "TR-rec1"}
	recTelco[0].FaceValueMinor = 8_000
	second, err := f.svc.reconcileLayer(ctx, testRecoverySpec(recPlat), "SIM_NG", "prg_sim_airtime01", winStart, winEnd, recTelco, genericTol())
	if err != nil {
		t.Fatal(err)
	}

	// The RECOVERY run was superseded; the FULFILMENT run is untouched.
	if f.runState(t, first.RunID) != "SUPERSEDED" {
		t.Fatalf("the re-reconciled RECOVERY run must be SUPERSEDED")
	}
	if f.activeRunID(t, "RECOVERY", winStart) != second.RunID {
		t.Fatalf("the new RECOVERY run must be the live one")
	}
	if f.activeRunID(t, "FULFILMENT", winStart) != fulfilBefore {
		t.Fatalf("re-reconciling RECOVERY must NOT touch the FULFILMENT run")
	}
}
