package operatormgmt_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/operatormgmt"
)

func newSvc(t *testing.T, suffix string) (*operatormgmt.Service, *repo.Admins, *testutil.DB, context.Context) {
	t.Helper()
	db := testutil.MustSetup(t, suffix)
	return operatormgmt.New(db.App, slog.Default()), &repo.Admins{Pool: db.App}, db, context.Background()
}

// Create is four-eyes: the proposer cannot approve; a DISTINCT admin can, and the
// resulting credential authenticates with the one-time key at the proposed grant.
func TestOperatorMgmt_FourEyesCreate(t *testing.T) {
	svc, admins, _, ctx := newSvc(t, "opmgmt_4eyes")

	req, err := svc.ProposeCreate(ctx, "op_new", "OPS", "*", "onboard ops lead", "admin_a")
	if err != nil {
		t.Fatalf("propose: %v", err)
	}

	// self-approve refused (four-eyes).
	if _, err := svc.ApproveCreate(ctx, req.RequestID, "admin_a"); !errors.Is(err, operatormgmt.ErrSelfApprove) {
		t.Fatalf("self-approve must be ErrSelfApprove, got %v", err)
	}

	// distinct approver provisions and returns the one-time key.
	key, err := svc.ApproveCreate(ctx, req.RequestID, "admin_b")
	if err != nil || key == "" {
		t.Fatalf("distinct approve should return a key: key=%q err=%v", key, err)
	}

	// the new operator authenticates at exactly the proposed role/scope.
	actor, role, scope, err := admins.ResolveCredentialWithRole(ctx, key)
	if err != nil {
		t.Fatalf("new operator should authenticate: %v", err)
	}
	if actor != "op_new" || role != "OPS" || scope != "*" {
		t.Fatalf("resolved grant mismatch: actor=%s role=%s scope=%s", actor, role, scope)
	}
}

// Revoke is single-actor and ends authentication immediately (the M4A-F1 status
// re-check); a second revoke is an idempotent no-op.
func TestOperatorMgmt_RevokeEndsAuth(t *testing.T) {
	svc, admins, _, ctx := newSvc(t, "opmgmt_revoke")

	req, err := svc.ProposeCreate(ctx, "op_rev", "FINANCE", "telco:SIM_NG", "onboard", "admin_a")
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	key, err := svc.ApproveCreate(ctx, req.RequestID, "admin_b")
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if _, _, _, err := admins.ResolveCredentialWithRole(ctx, key); err != nil {
		t.Fatalf("operator should authenticate before revoke: %v", err)
	}

	// single-actor revoke.
	if err := svc.Revoke(ctx, "op_rev", "admin_c", "offboarding"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	// authentication now fails — the credential is REVOKED.
	if _, _, _, err := admins.ResolveCredentialWithRole(ctx, key); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("revoked key must not authenticate, got %v", err)
	}
	// idempotent second revoke.
	if err := svc.Revoke(ctx, "op_rev", "admin_c", "again"); !errors.Is(err, operatormgmt.ErrNotActive) {
		t.Fatalf("second revoke must be ErrNotActive, got %v", err)
	}
}

// A proposal for an actor identity that already exists is refused (identities are
// permanent — a revoked actor is retired, never re-provisioned).
func TestOperatorMgmt_ProposeDuplicateActor(t *testing.T) {
	svc, _, _, ctx := newSvc(t, "opmgmt_dup")

	req, err := svc.ProposeCreate(ctx, "op_dup", "SUPPORT", "*", "first", "admin_a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApproveCreate(ctx, req.RequestID, "admin_b"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ProposeCreate(ctx, "op_dup", "ADMIN", "*", "second", "admin_a"); !errors.Is(err, operatormgmt.ErrBadRequest) {
		t.Fatalf("re-proposing an existing actor must be ErrBadRequest, got %v", err)
	}
}

// Reject closes a pending request (distinct actor) without provisioning; the
// actor is never created, so the identity remains free.
func TestOperatorMgmt_Reject(t *testing.T) {
	svc, admins, _, ctx := newSvc(t, "opmgmt_reject")

	req, err := svc.ProposeCreate(ctx, "op_rej", "RISK", "*", "maybe", "admin_a")
	if err != nil {
		t.Fatal(err)
	}
	// self-reject refused (all decisions are two-actor).
	if err := svc.RejectCreate(ctx, req.RequestID, "admin_a", "withdraw"); !errors.Is(err, operatormgmt.ErrSelfApprove) {
		t.Fatalf("self-reject must be ErrSelfApprove, got %v", err)
	}
	if err := svc.RejectCreate(ctx, req.RequestID, "admin_b", "not needed"); err != nil {
		t.Fatalf("distinct reject: %v", err)
	}
	if exists, err := admins.ExistsByActor(ctx, admins.Pool, "op_rej"); err != nil || exists {
		t.Fatalf("rejected actor must not exist (exists=%v err=%v)", exists, err)
	}
}
