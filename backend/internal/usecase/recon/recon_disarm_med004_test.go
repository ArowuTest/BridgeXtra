package recon

// BX-MED-004: a manual money-door control must be durably auditable. Disarming a telco's RECOVERY
// layer turns off a live money-safety pipeline; the arm path records proposer/approver/reason, but
// disarm previously only logged to stdout and DELETE-and-forgot. Now it REQUIRES a reason and
// writes an audit_events row atomically with the disarm.

import (
	"context"
	"strings"
	"testing"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
)

func TestBXMED004_DisarmRecovery_RequiresReasonAndAudits(t *testing.T) {
	svc, db := newArmingSvc(t, "med004_disarm")
	ctx := context.Background()

	// Arm SIM_NG so there is a live money-door to close.
	prop := ArmProposal{
		TelcoID: "SIM_NG", ProposedBy: "alice", Reason: "go-live",
		ConfirmedFeedReversalBasis: "NET_SAME_DAY", ConfirmedBusinessDateBasis: "OCCURRED_AT_LAGOS_DATE",
	}
	id, err := svc.ProposeArmRecovery(ctx, prop)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ApproveArmRecovery(ctx, id, "bob"); err != nil {
		t.Fatal(err)
	}
	if armed, _ := svc.Arming.IsLayerArmed(ctx, "SIM_NG", repo.ReconLayerRecovery); !armed {
		t.Fatal("setup: SIM_NG RECOVERY must be armed")
	}

	// A disarm without a reason is REFUSED — a money-door control must record WHY. Mutation proof:
	// drop the actor/reason guard in DisarmRecovery and this returns nil — RED.
	if err := svc.DisarmRecovery(ctx, "SIM_NG", "ops", ""); err == nil || !strings.Contains(err.Error(), "BX-MED-004") {
		t.Fatalf("disarm without a reason must be refused (BX-MED-004), got %v", err)
	}

	// A disarm with actor+reason succeeds AND leaves a durable audit trail.
	const reason = "telco offboarded, closing recovery"
	if err := svc.DisarmRecovery(ctx, "SIM_NG", "ops", reason); err != nil {
		t.Fatalf("disarm with actor+reason: %v", err)
	}
	if armed, _ := svc.Arming.IsLayerArmed(ctx, "SIM_NG", repo.ReconLayerRecovery); armed {
		t.Fatal("SIM_NG RECOVERY must be disarmed after the disarm")
	}
	// The durable governance record. Mutation proof: drop the Audit.Insert in DisarmRecovery and
	// this count is 0 — RED. (Atomic with the disarm: a rolled-back disarm writes no record.)
	var n int
	if err := db.Admin.QueryRow(ctx, `
		SELECT count(*) FROM audit_events
		WHERE action='recon.recovery.disarmed' AND actor='ops' AND telco_id='SIM_NG'
		  AND reason=$1 AND target_id='recovery:SIM_NG'`, reason).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("BX-MED-004: a disarm must write exactly one durable audit_events row, got %d", n)
	}
}
