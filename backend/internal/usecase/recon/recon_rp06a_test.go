package recon

// R-P0-6 Slice A: a reconciliation run is now an immutable, self-verifying
// statement. It writes a recon_runs header carrying the source + platform
// manifests (record counts, monetary control totals, source hash) and the
// outcome counts; a rerun SUPERSEDES the prior run so exactly one live
// reconciliation of a scope exists; items are FK-linked to their header. The
// source hash is order-independent and change-sensitive, so a partial or
// altered feed is detectable at the run level.

import (
	"context"
	"testing"
	"time"
)

// A fixed explicit window that spans all the backdated test data — used with
// ReconcilePeriod to re-reconcile the SAME period (so supersession /
// completeness behave as a same-period re-run, distinct from the incremental
// next-period path of RunFulfilment).
var (
	winStart = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	winEnd   = time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
)

// matchTxn builds a telco SUCCESS record credited an hour ago, so it sits
// comfortably inside the reconciliation window [epoch, now-lag).
func matchTxn(advID string, minor int64) telcoTransaction {
	return telcoTransaction{
		PlatformRequestID: advID, TelcoReference: "TR-" + advID, FaceValueMinor: minor,
		Currency: "NGN", Status: "SUCCESS", CreditedAt: reconPast(),
	}
}

func reconPast() time.Time { return time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC) }

func TestRP06A_RunHeaderRecordsManifest(t *testing.T) {
	f := newReconFixture(t, "rp06a_hdr", []telcoTransaction{matchTxn("adv_ok", 5_000)})
	f.seedConfirmedAdvance(t, "adv_ok", 5_000, "NGN", "TR-adv_ok")
	ctx := context.Background()

	sum, err := f.svc.RunFulfilment(ctx, "SIM_NG", "prg_sim_airtime01")
	if err != nil {
		t.Fatal(err)
	}
	if sum.Matched != 1 {
		t.Fatalf("want 1 matched, got %+v", sum)
	}

	var layer, state, srcHash string
	var srcCount, srcTotal, platCount, platTotal, matched, breaks int64
	var supersededBy *string
	if err := f.db.Admin.QueryRow(ctx, `
		SELECT layer, state, source_record_count, source_control_total_minor, source_hash,
		       platform_record_count, platform_control_total_minor, matched_count, break_count, superseded_by
		FROM recon_runs WHERE run_id = $1`, sum.RunID).
		Scan(&layer, &state, &srcCount, &srcTotal, &srcHash, &platCount, &platTotal, &matched, &breaks, &supersededBy); err != nil {
		t.Fatalf("run header must exist: %v", err)
	}
	if layer != "FULFILMENT" || state != "ACTIVE" || supersededBy != nil {
		t.Fatalf("header state wrong: layer=%s state=%s superseded_by=%v", layer, state, supersededBy)
	}
	if srcCount != 1 || srcTotal != 5_000 || platCount != 1 || platTotal != 5_000 {
		t.Fatalf("manifest wrong: src=%d/%d plat=%d/%d", srcCount, srcTotal, platCount, platTotal)
	}
	if matched != 1 || breaks != 0 {
		t.Fatalf("outcome counts wrong: matched=%d breaks=%d", matched, breaks)
	}
	if srcHash == "" || srcHash != sum.SourceHash {
		t.Fatalf("source hash must be recorded and match the summary: %q vs %q", srcHash, sum.SourceHash)
	}
	if sum.SourceControlTotalMinor != 5_000 || sum.PlatformControlTotalMinor != 5_000 {
		t.Fatalf("summary control totals wrong: %+v", sum)
	}

	var itemsForRun int
	if err := f.db.Admin.QueryRow(ctx, `SELECT count(*) FROM recon_items WHERE run_id=$1`, sum.RunID).Scan(&itemsForRun); err != nil {
		t.Fatal(err)
	}
	if itemsForRun != 1 {
		t.Fatalf("the run's items must be FK-linked to its header, got %d", itemsForRun)
	}
}

func TestRP06A_RerunSupersedesPriorRun(t *testing.T) {
	f := newReconFixture(t, "rp06a_super", []telcoTransaction{matchTxn("adv_ok", 5_000)})
	f.seedConfirmedAdvance(t, "adv_ok", 5_000, "NGN", "TR-adv_ok")
	ctx := context.Background()

	// Two re-reconciles of the SAME period: the second supersedes the first.
	sum1, err := f.svc.ReconcilePeriod(ctx, "SIM_NG", "prg_sim_airtime01", winStart, winEnd)
	if err != nil {
		t.Fatal(err)
	}
	sum2, err := f.svc.ReconcilePeriod(ctx, "SIM_NG", "prg_sim_airtime01", winStart, winEnd)
	if err != nil {
		t.Fatal(err)
	}

	var active, total int
	if err := f.db.Admin.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM recon_runs WHERE state='ACTIVE'),
		       (SELECT count(*) FROM recon_runs)`).Scan(&active, &total); err != nil {
		t.Fatal(err)
	}
	if active != 1 || total != 2 {
		t.Fatalf("a same-period re-reconcile must supersede, not accumulate: active=%d total=%d", active, total)
	}

	var state string
	var by *string
	if err := f.db.Admin.QueryRow(ctx, `SELECT state, superseded_by FROM recon_runs WHERE run_id=$1`, sum1.RunID).Scan(&state, &by); err != nil {
		t.Fatal(err)
	}
	if state != "SUPERSEDED" || by == nil || *by != sum2.RunID {
		t.Fatalf("the prior run must be SUPERSEDED by the new one: state=%s by=%v want=%s", state, by, sum2.RunID)
	}
}

// The one-active-run invariant is enforced by the DB, not just the code: a
// second ACTIVE run for the same scope is rejected, and a superseded run is
// immutable.
func TestRP06A_OneActivePerScope_FailClosed(t *testing.T) {
	f := newReconFixture(t, "rp06a_guard", []telcoTransaction{matchTxn("adv_ok", 5_000)})
	f.seedConfirmedAdvance(t, "adv_ok", 5_000, "NGN", "TR-adv_ok")
	ctx := context.Background()

	sum1, err := f.svc.ReconcilePeriod(ctx, "SIM_NG", "prg_sim_airtime01", winStart, winEnd)
	if err != nil {
		t.Fatal(err)
	}
	// A second ACTIVE run for the same (telco, programme, layer, period_start)
	// violates the partial unique index — two live reconciliations of one period
	// can never coexist. (period_start must match sum1's = winStart.)
	if _, err := f.db.Admin.Exec(ctx, `
		INSERT INTO recon_runs (run_id, telco_id, programme_id, layer, period_start, period_end,
		  source_record_count, source_control_total_minor, source_hash,
		  platform_record_count, platform_control_total_minor, created_by)
		VALUES ('run_dup','SIM_NG','prg_sim_airtime01','FULFILMENT', '2020-01-01T00:00:00Z'::timestamptz, now(),
		  0,0,'h',0,0,'test')`); err == nil {
		t.Fatal("a second ACTIVE run for the same period must be rejected by the unique index")
	}

	// After a same-period re-reconcile supersedes sum1, sum1 is immutable.
	if _, err := f.svc.ReconcilePeriod(ctx, "SIM_NG", "prg_sim_airtime01", winStart, winEnd); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(ctx, `
		UPDATE recon_runs SET state='SUPERSEDED', superseded_by='run_dup' WHERE run_id=$1`, sum1.RunID); err == nil {
		t.Fatal("an already-superseded run must not be re-superseded (immutable)")
	}
}

// The source manifest hash is order-independent (the same set hashes the same
// regardless of feed order) and change-sensitive (any altered amount changes
// it) — the property that makes a partial or tampered feed detectable.
func TestRP06A_SourceManifest_OrderIndependentAndChangeSensitive(t *testing.T) {
	const ceiling = 1_000_000
	a := []telcoTransaction{matchTxn("x", 100), matchTxn("y", 200)}
	reordered := []telcoTransaction{a[1], a[0]}
	c1, tot1, h1, err1 := sourceManifest(a, ceiling)
	c2, tot2, h2, err2 := sourceManifest(reordered, ceiling)
	if err1 != nil || err2 != nil {
		t.Fatalf("unexpected error: %v %v", err1, err2)
	}
	if h1 != h2 {
		t.Fatalf("hash must be order-independent: %s vs %s", h1, h2)
	}
	if c1 != 2 || c2 != 2 || tot1 != 300 || tot2 != 300 {
		t.Fatalf("manifest counts/totals wrong: %d/%d %d/%d", c1, tot1, c2, tot2)
	}
	altered := []telcoTransaction{matchTxn("x", 101), matchTxn("y", 200)}
	if _, _, h3, _ := sourceManifest(altered, ceiling); h3 == h1 {
		t.Fatal("an altered source amount must change the manifest hash")
	}
	// A non-SUCCESS record is in the count + hash but NOT the credit control total.
	withFailed := []telcoTransaction{matchTxn("x", 100), {PlatformRequestID: "z", FaceValueMinor: 999, Currency: "NGN", Status: "FAILED"}}
	cF, totF, _, _ := sourceManifest(withFailed, ceiling)
	if cF != 2 || totF != 100 {
		t.Fatalf("failed records count but carry no credit: count=%d total=%d", cF, totF)
	}
}
