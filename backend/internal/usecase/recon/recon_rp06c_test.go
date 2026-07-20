package recon

// R-P0-6 Slice C: period / watermark / bounded scope. A run reconciles a
// bounded window [watermark, now-lag) instead of all history; the settling lag
// keeps in-flight records out; distinct periods coexist as separate ACTIVE
// runs; and once reconciled up to now, the incremental run finds nothing.

import (
	"context"
	"testing"
	"time"
)

// The settling lag excludes a telco record credited too recently: its matching
// advance is (correctly) an open BREAK_MISSING_TELCO until the record settles,
// while an older settled record matches.
func TestRP06C_LagExcludesUnsettledTelco(t *testing.T) {
	now := time.Now().UTC()
	txns := []telcoTransaction{
		{PlatformRequestID: "adv_old", TelcoReference: "TR-old", FaceValueMinor: 5_000, Currency: "NGN", Status: "SUCCESS", CreditedAt: now.Add(-time.Hour)},
		{PlatformRequestID: "adv_new", TelcoReference: "TR-new", FaceValueMinor: 5_000, Currency: "NGN", Status: "SUCCESS", CreditedAt: now}, // within the 300s lag
	}
	f := newReconFixture(t, "rp06c_lag", txns)
	f.seedConfirmedAdvance(t, "adv_old", 5_000, "NGN", "TR-old")
	f.seedConfirmedAdvance(t, "adv_new", 5_000, "NGN", "TR-new")
	ctx := context.Background()

	sum, err := f.svc.RunFulfilment(ctx, "SIM_NG", "prg_sim_airtime01")
	if err != nil {
		t.Fatal(err)
	}
	if sum.Matched != 1 {
		t.Fatalf("the settled record must match, got matched=%d", sum.Matched)
	}
	if sum.MissingTelco != 1 {
		t.Fatalf("the unsettled telco record must be excluded, leaving its advance a missing-telco break, got %d", sum.MissingTelco)
	}
}

// Once reconciled up to now (watermark = now), the incremental run finds no
// settled time has elapsed and writes NO run.
func TestRP06C_NothingToReconcileAfterWatermark(t *testing.T) {
	f := newReconFixture(t, "rp06c_nothing", []telcoTransaction{matchTxn("adv_ok", 5_000)})
	f.seedConfirmedAdvance(t, "adv_ok", 5_000, "NGN", "TR-adv_ok")
	ctx := context.Background()

	// Reconcile an explicit window up to NOW → the watermark becomes now.
	if _, err := f.svc.ReconcilePeriod(ctx, "SIM_NG", "prg_sim_airtime01", winStart, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	sum, err := f.svc.RunFulfilment(ctx, "SIM_NG", "prg_sim_airtime01")
	if err != nil {
		t.Fatal(err)
	}
	if !sum.NothingToReconcile {
		t.Fatalf("with the watermark at now, the incremental run must find nothing, got %+v", sum)
	}
	var runs int
	if err := f.db.Admin.QueryRow(ctx, `SELECT count(*) FROM recon_runs`).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 1 {
		t.Fatalf("a nothing-to-reconcile run must write no header, got %d total runs", runs)
	}
}

// Distinct periods coexist as separate ACTIVE runs — one period never
// supersedes another; only a re-reconcile of the SAME period does.
func TestRP06C_DistinctPeriodsCoexist(t *testing.T) {
	f := newReconFixture(t, "rp06c_coexist", []telcoTransaction{})
	ctx := context.Background()

	if _, err := f.svc.ReconcilePeriod(ctx, "SIM_NG", "prg_sim_airtime01",
		time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.ReconcilePeriod(ctx, "SIM_NG", "prg_sim_airtime01",
		time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	var active int
	if err := f.db.Admin.QueryRow(ctx, `SELECT count(*) FROM recon_runs WHERE state='ACTIVE'`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 2 {
		t.Fatalf("distinct periods must coexist as separate ACTIVE runs, got %d", active)
	}
}
