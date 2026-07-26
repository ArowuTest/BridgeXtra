package recon

// S3-C3 four-eyes arming — the money-door-opening path. Verifies distinct-actor
// arming (SetLive is its only production caller), mock-only-on-synthetic (the
// circularity guard the validator can't do), the confirmed-basis precondition,
// armed!=live, one-pending, and single-actor disarm.

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
)

// The arming Service runs as tcp_worker: SetLiveArmed INSERTs recon_layer_arming
// and the arm-request table is worker-owned (both non-RLS control registries).
func newArmingSvc(t *testing.T, suffix string) (*Service, *testutil.DB) {
	t.Helper()
	db := testutil.MustSetup(t, suffix)
	return New(db.Worker, configsvc.New(db.Worker), slog.Default()), db
}

func TestS3C3_Arming_FourEyesAndGuards(t *testing.T) {
	svc, _ := newArmingSvc(t, "s3c3_arm")
	ctx := context.Background()
	// SIM_NG is synthetic (mig 0053) with a mock telco.recovery_feed (mig 0053) and
	// status ACTIVE (mig 0001) — a valid arm target.
	prop := ArmProposal{
		TelcoID: "SIM_NG", ProposedBy: "alice", Reason: "go-live",
		ConfirmedFeedReversalBasis: "NET_SAME_DAY", ConfirmedBusinessDateBasis: "OCCURRED_AT_LAGOS_DATE",
	}

	// I11: the confirmed feed basis is a hard precondition.
	noBasis := prop
	noBasis.ConfirmedFeedReversalBasis = ""
	if _, err := svc.ProposeArmRecovery(ctx, noBasis); !errors.Is(err, ErrArmBasisRequired) {
		t.Fatalf("arm without a reversal basis must fail ErrArmBasisRequired, got %v", err)
	}

	id, err := svc.ProposeArmRecovery(ctx, prop)
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	// One PENDING per telco/layer: a second propose is refused.
	if _, err := svc.ProposeArmRecovery(ctx, prop); !errors.Is(err, ErrArmAlreadyPending) {
		t.Fatalf("a second pending propose must fail ErrArmAlreadyPending, got %v", err)
	}
	// Two-person rule: the proposer cannot approve their own request.
	if err := svc.ApproveArmRecovery(ctx, id, "alice"); !errors.Is(err, ErrArmSelfApprove) {
		t.Fatalf("self-approve must fail ErrArmSelfApprove, got %v", err)
	}

	// A DISTINCT checker arms the layer.
	if err := svc.ApproveArmRecovery(ctx, id, "bob"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if armed, _ := svc.Arming.IsLayerArmed(ctx, "SIM_NG", repo.ReconLayerRecovery); !armed {
		t.Fatal("SIM_NG RECOVERY must be armed after a distinct-actor approve")
	}
	// Armed but NOT live — last_recon_at is NULL until the first confirmed recon.
	if live, _ := svc.Arming.IsLayerLive(ctx, "SIM_NG", repo.ReconLayerRecovery); live {
		t.Fatal("an armed layer must NOT be live until its first confirmed recon")
	}

	// Disarm (single-actor) closes it.
	if err := svc.DisarmRecovery(ctx, "SIM_NG", "carol", "incident"); err != nil {
		t.Fatalf("disarm: %v", err)
	}
	if armed, _ := svc.Arming.IsLayerArmed(ctx, "SIM_NG", repo.ReconLayerRecovery); armed {
		t.Fatal("SIM_NG must be disarmed after disarm")
	}
}

// I10: a mock feed may NOT arm a non-synthetic (real) telco — the circularity guard.
func TestS3C3_Arming_MockOnRealTelcoRefused(t *testing.T) {
	svc, db := newArmingSvc(t, "s3c3_mockreal")
	ctx := context.Background()
	if _, err := db.Admin.Exec(ctx,
		`INSERT INTO telcos (telco_id, name, country, status, is_synthetic) VALUES ('REAL_NG','Real NG','NG','ACTIVE',false)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Admin.Exec(ctx, `
		WITH t AS (SELECT '{"source":"mock","expected_currency":"NGN","business_date_basis":"occurred_at_lagos_date"}'::text AS c)
		INSERT INTO config_versions (config_version_id, domain, scope, version_no, state, content, content_hash,
		  effective_from, created_by, approved_by, reason)
		SELECT 'cfg_realng_feed','telco.recovery_feed','telco:REAL_NG',1,'ACTIVE',
		  t.c::jsonb, encode(sha256(t.c::bytea),'hex'), now(), 'seed:builder', 'seed:reviewer', 'test'
		FROM t`); err != nil {
		t.Fatal(err)
	}
	prop := ArmProposal{
		TelcoID: "REAL_NG", ProposedBy: "alice", Reason: "go-live",
		ConfirmedFeedReversalBasis: "GROSS", ConfirmedBusinessDateBasis: "OCCURRED_AT_LAGOS_DATE",
	}
	if _, err := svc.ProposeArmRecovery(ctx, prop); !errors.Is(err, ErrArmMockOnReal) {
		t.Fatalf("a mock feed on a non-synthetic telco must fail ErrArmMockOnReal, got %v", err)
	}
}
