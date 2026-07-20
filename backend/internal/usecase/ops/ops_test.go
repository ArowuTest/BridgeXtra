package ops_test

// M3f pack: break lifecycle with append-only action log and aged-break
// alerting from governed config; complaint lifecycle with schema-required
// resolution; bureau batch staged with a reproducible hash and STRUCTURAL
// dormancy (the schema refuses any state but STAGED).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/ops"
)

func tenantCtx() context.Context { return platform.WithTenant(context.Background(), "SIM_NG") }

func setup(t *testing.T, suffix string) (*ops.Service, *testutil.DB) {
	t.Helper()
	db := testutil.MustSetup(t, suffix)
	return ops.New(db.App, configsvc.New(db.App), slog.Default()), db
}

// seedBreak inserts an aged unresolved break directly (the recon engine is
// proven elsewhere; this pack exercises the WORKFLOW on its output).
func seedBreak(t *testing.T, db *testutil.DB, id string, ageHours int) {
	t.Helper()
	// R-P0-6: recon_items are FK-linked to a run header; seed it once.
	if _, err := db.Admin.Exec(context.Background(), `
		INSERT INTO recon_runs (run_id, telco_id, programme_id, layer, period_start, period_end,
		  source_record_count, source_control_total_minor, source_hash,
		  platform_record_count, platform_control_total_minor, created_by)
		VALUES ('run_test','SIM_NG','prg_sim_airtime01','FULFILMENT', to_timestamp(0), now(), 0,0,'seed',0,0,'test')
		ON CONFLICT (run_id) DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Admin.Exec(context.Background(), fmt.Sprintf(`
		INSERT INTO recon_items (recon_item_id, run_id, telco_id, item_type, status, detail, created_at)
		VALUES ($1, 'run_test', 'SIM_NG', 'FULFILMENT', 'BREAK_MISSING_TELCO', '{}', now() - interval '%d hours')`, ageHours),
		id); err != nil {
		t.Fatal(err)
	}
}

func TestM3F_BreakWorkflow_TwoActorResolution(t *testing.T) {
	svc, db := setup(t, "ops_breaks")
	seedBreak(t, db, "rci_b1", 1)
	ctx := context.Background()

	if err := svc.BreakAction(ctx, "SIM_NG", "rci_b1", "ASSIGN", "carol", "investigating missing telco record"); err != nil {
		t.Fatal(err)
	}
	if err := svc.BreakAction(ctx, "SIM_NG", "rci_b1", "NOTE", "carol", "telco confirms outage window 02:00-02:15"); err != nil {
		t.Fatal(err)
	}
	// R-P0-6 Slice E1: single-actor RESOLVE is retired.
	if err := svc.BreakAction(ctx, "SIM_NG", "rci_b1", "RESOLVE", "carol", "one-shot"); err == nil {
		t.Fatal("single-actor RESOLVE must be refused (two-person decision)")
	}
	// Maker proposes.
	if err := svc.BreakAction(ctx, "SIM_NG", "rci_b1", "PROPOSE_RESOLVE", "carol", "telco replayed missing CDR; amounts reconcile"); err != nil {
		t.Fatal(err)
	}
	// The maker cannot approve their own proposal (four-eyes).
	if err := svc.BreakAction(ctx, "SIM_NG", "rci_b1", "APPROVE_RESOLVE", "carol", "self approve"); !errors.Is(err, repo.ErrSelfApproveResolution) {
		t.Fatalf("self-approval must be refused by four-eyes, got %v", err)
	}
	// A distinct checker approves — the break clears.
	if err := svc.BreakAction(ctx, "SIM_NG", "rci_b1", "APPROVE_RESOLVE", "dave", "verified against telco statement"); err != nil {
		t.Fatal(err)
	}
	// Proposing again: no longer an open break.
	if err := svc.BreakAction(ctx, "SIM_NG", "rci_b1", "PROPOSE_RESOLVE", "carol", "again"); err == nil {
		t.Fatal("proposing a resolution for a resolved break must refuse")
	}

	var resolution, proposedBy, resolvedBy string
	if err := db.Admin.QueryRow(ctx, `
		SELECT resolution, resolution_proposed_by, resolved_by
		FROM recon_items WHERE recon_item_id='rci_b1'`).Scan(&resolution, &proposedBy, &resolvedBy); err != nil {
		t.Fatal(err)
	}
	if resolution == "" || proposedBy != "carol" || resolvedBy != "dave" {
		t.Fatalf("break must close with a two-actor trail: resolution=%q proposed_by=%q resolved_by=%q", resolution, proposedBy, resolvedBy)
	}
	var actions int
	if err := db.Admin.QueryRow(ctx,
		`SELECT count(*) FROM recon_break_actions WHERE recon_item_id='rci_b1'`).Scan(&actions); err != nil {
		t.Fatal(err)
	}
	// ASSIGN + NOTE + PROPOSE_RESOLVE + APPROVE_RESOLVE(dave); the refused actions
	// rolled back and logged nothing.
	if actions != 4 {
		t.Fatalf("only successful actions are logged, got %d", actions)
	}
}

func TestM3F_AgedBreaks_AlertFromGovernedThreshold(t *testing.T) {
	svc, db := setup(t, "ops_aged")
	seedBreak(t, db, "rci_old", 100) // older than any sane threshold
	seedBreak(t, db, "rci_new", 1)

	aged, err := svc.AgedBreaks(tenantCtx(), "SIM_NG", "prg_sim_airtime01")
	if err != nil {
		t.Fatal(err)
	}
	if len(aged) != 1 || aged[0].ReconItemID != "rci_old" {
		t.Fatalf("exactly the aged break must alert: %+v", aged)
	}
}

func TestM3F_Complaint_ResolutionRequiredToClose(t *testing.T) {
	svc, db := setup(t, "ops_complaints")
	ctx := context.Background()

	c, err := svc.OpenComplaint(ctx, "SIM_NG", "tok_sim_0001", "", "USSD", "DISPUTED_RECOVERY",
		"customer says recharge was garnished twice")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ProgressComplaint(ctx, "SIM_NG", c.ComplaintID, "OPEN", "IN_REVIEW", "carol", ""); err != nil {
		t.Fatal(err)
	}
	// Closing WITHOUT a resolution: the schema refuses.
	if err := svc.ProgressComplaint(ctx, "SIM_NG", c.ComplaintID, "IN_REVIEW", "RESOLVED", "carol", ""); err == nil {
		t.Fatal("closing without a resolution must be refused by the schema")
	}
	if err := svc.ProgressComplaint(ctx, "SIM_NG", c.ComplaintID, "IN_REVIEW", "RESOLVED", "carol",
		"single garnish confirmed via ledger; customer refunded goodwill airtime"); err != nil {
		t.Fatal(err)
	}
	var state string
	var resolvedAt *time.Time
	if err := db.Admin.QueryRow(ctx,
		`SELECT state, resolved_at FROM complaints WHERE complaint_id=$1`, c.ComplaintID).
		Scan(&state, &resolvedAt); err != nil {
		t.Fatal(err)
	}
	if state != "RESOLVED" || resolvedAt == nil {
		t.Fatalf("complaint must close with timestamp: %s/%v", state, resolvedAt)
	}
}

func TestM3F_Bureau_StagedReproducible_StructurallyDormant(t *testing.T) {
	svc, db := setup(t, "ops_bureau")
	ctx := context.Background()
	start := time.Now().UTC().Add(-24 * time.Hour)
	end := time.Now().UTC().Add(time.Hour)

	// The seeded advance-free book stages an EMPTY batch honestly.
	batch, err := svc.ProduceBureauBatch(ctx, "SIM_NG", start, end)
	if err != nil {
		t.Fatal(err)
	}
	if batch.FileHash == "" {
		t.Fatal("staged batch must carry its file hash")
	}

	// The file is DERIVABLE: regenerate and compare hashes.
	file, err := svc.RegenerateBureauFile(ctx, "SIM_NG", batch.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(file)
	if hex.EncodeToString(sum[:]) != batch.FileHash {
		t.Fatal("regenerated bureau file must match the staged hash")
	}

	// STRUCTURAL dormancy: no state but STAGED exists — even an admin
	// cannot mark a batch sent until licensing ships that migration.
	if _, err := db.Admin.Exec(ctx,
		`UPDATE bureau_export_batches SET state='SENT' WHERE batch_id=$1`, batch.BatchID); err == nil {
		t.Fatal("the schema must refuse any bureau state except STAGED (dormant by construction)")
	}

	// Duplicate period refused by schema.
	if _, err := svc.ProduceBureauBatch(ctx, "SIM_NG", start, end); err == nil {
		t.Fatal("duplicate bureau period must be refused")
	}
}
