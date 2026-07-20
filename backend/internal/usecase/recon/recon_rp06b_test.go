package recon

// R-P0-6 Slice B (AUD-P2-010 / R-P2-5): input dedup + canonical-item-per-
// match-key + duplicate-source classification. A telco success record reported
// twice for one fulfilment must NOT be silently double-counted into a second
// MATCHED — the first is the canonical classification, the repeat is a
// BREAK_DUPLICATE_TELCO_RECORD, and the DB enforces exactly one canonical item
// per (run, match_key).

import (
	"context"
	"testing"
)

func TestRP06B_DuplicateTelcoRecord_OneCanonicalPlusBreak(t *testing.T) {
	// Same key twice — the second is a duplicate source record.
	txns := []telcoTransaction{matchTxn("adv_ok", 5_000), matchTxn("adv_ok", 5_000)}
	f := newReconFixture(t, "rp06b_dup", txns)
	f.seedConfirmedAdvance(t, "adv_ok", 5_000, "NGN", "TR-adv_ok")
	ctx := context.Background()

	sum, err := f.svc.RunFulfilment(ctx, "SIM_NG", "prg_sim_airtime01")
	if err != nil {
		t.Fatal(err)
	}
	if sum.Matched != 1 || sum.DuplicateTelco != 1 {
		t.Fatalf("a duplicate must yield ONE canonical MATCHED + one duplicate break, got matched=%d dup=%d", sum.Matched, sum.DuplicateTelco)
	}
	if f.statusCount(t, "MATCHED") != 1 {
		t.Fatalf("the duplicate must NOT create a second MATCHED, got %d", f.statusCount(t, "MATCHED"))
	}
	if f.statusCount(t, "BREAK_DUPLICATE_TELCO_RECORD") != 1 {
		t.Fatalf("the repeat must be classified as a duplicate break, got %d", f.statusCount(t, "BREAK_DUPLICATE_TELCO_RECORD"))
	}
	// Provenance: the source control total reflects the RAW feed (both SUCCESS
	// records), even though the match dedups — the duplicate surfaces as a break,
	// it is never hidden by silently collapsing the total.
	if sum.SourceControlTotalMinor != 10_000 {
		t.Fatalf("source control total must reflect the raw feed (2x5000), got %d", sum.SourceControlTotalMinor)
	}
}

// The one-canonical-per-(run,match_key) invariant is enforced by the DB, not
// just the code: a second canonical item for the same key is rejected, while a
// duplicate-classified item for the same key is allowed.
func TestRP06B_CanonicalUniqueness_FailClosed(t *testing.T) {
	f := newReconFixture(t, "rp06b_uq", []telcoTransaction{matchTxn("adv_ok", 5_000)})
	f.seedConfirmedAdvance(t, "adv_ok", 5_000, "NGN", "TR-adv_ok")
	ctx := context.Background()

	sum, err := f.svc.RunFulfilment(ctx, "SIM_NG", "prg_sim_airtime01")
	if err != nil {
		t.Fatal(err)
	}
	// A second CANONICAL (non-duplicate) item for the same (run, match_key) must
	// violate the partial unique index.
	if _, err := f.db.Admin.Exec(ctx, `
		INSERT INTO recon_items (recon_item_id, run_id, telco_id, item_type, status, match_key)
		VALUES ('rci_dup2', $1, 'SIM_NG', 'FULFILMENT', 'MATCHED', 'adv_ok')`, sum.RunID); err == nil {
		t.Fatal("a second canonical item for the same (run, match_key) must be rejected")
	}
	// A duplicate-classified item for the same key IS allowed (excluded from the index).
	if _, err := f.db.Admin.Exec(ctx, `
		INSERT INTO recon_items (recon_item_id, run_id, telco_id, item_type, status, match_key)
		VALUES ('rci_dup3', $1, 'SIM_NG', 'FULFILMENT', 'BREAK_DUPLICATE_TELCO_RECORD', 'adv_ok')`, sum.RunID); err != nil {
		t.Fatalf("a duplicate-classified item for the same key must be allowed: %v", err)
	}
}

func TestRP06B_MatchKeyPopulated(t *testing.T) {
	f := newReconFixture(t, "rp06b_mk", []telcoTransaction{matchTxn("adv_ok", 5_000)})
	f.seedConfirmedAdvance(t, "adv_ok", 5_000, "NGN", "TR-adv_ok")
	ctx := context.Background()

	sum, err := f.svc.RunFulfilment(ctx, "SIM_NG", "prg_sim_airtime01")
	if err != nil {
		t.Fatal(err)
	}
	var mk string
	if err := f.db.Admin.QueryRow(ctx,
		`SELECT match_key FROM recon_items WHERE run_id=$1 AND status='MATCHED'`, sum.RunID).Scan(&mk); err != nil {
		t.Fatal(err)
	}
	if mk != "adv_ok" {
		t.Fatalf("match_key must be the logical key, got %q", mk)
	}
}

// A duplicate of a BREAK (a phantom key reported twice, no platform advance)
// still yields one canonical break + one duplicate break.
func TestRP06B_DuplicateOfBreak(t *testing.T) {
	txns := []telcoTransaction{matchTxn("ghost", 5_000), matchTxn("ghost", 5_000)}
	f := newReconFixture(t, "rp06b_dupbreak", txns) // no advance seeded for "ghost"
	ctx := context.Background()

	sum, err := f.svc.RunFulfilment(ctx, "SIM_NG", "prg_sim_airtime01")
	if err != nil {
		t.Fatal(err)
	}
	if sum.MissingPlatform != 1 || sum.DuplicateTelco != 1 {
		t.Fatalf("a repeated phantom key must be one missing-platform break + one duplicate, got missing=%d dup=%d", sum.MissingPlatform, sum.DuplicateTelco)
	}
}
