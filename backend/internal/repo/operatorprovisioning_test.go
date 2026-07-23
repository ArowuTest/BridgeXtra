package repo_test

// Gate — governed operator provisioning (v1), DB layer. The security property is
// STRUCTURAL write-once: the app role may create a credential and revoke it
// (status), but the database itself refuses any change to role or scope, so a
// privilege change is impossible except by revoke-and-recreate. Plus the
// two-actor create rule and one-open convergence, enforced by the schema.

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
)

func isPermDenied(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "42501" // insufficient_privilege
}

// Reviewer-required: tcp_app can INSERT a credential and UPDATE(status) to revoke,
// but MUST be permission-denied on any UPDATE of role or scope. Write-once is
// enforced by the GRANT set (0047), not by convention.
func TestOperatorProvisioning_TcpAppCannotUpdateRoleScope(t *testing.T) {
	db := testutil.MustSetup(t, "opprov_grants")
	ctx := context.Background()

	// Granted: INSERT (the create path).
	if _, err := db.App.Exec(ctx,
		`INSERT INTO admin_credentials (admin_id, actor, key_hash, role, scope) VALUES ($1,$2,$3,$4,$5)`,
		"adm_g", "op_grant", []byte{0x01, 0x02, 0x03}, "SUPPORT", "*"); err != nil {
		t.Fatalf("tcp_app INSERT admin_credentials must be granted (create path): %v", err)
	}
	// Granted: UPDATE(status) (the revoke path).
	if _, err := db.App.Exec(ctx,
		`UPDATE admin_credentials SET status='REVOKED' WHERE actor='op_grant'`); err != nil {
		t.Fatalf("tcp_app UPDATE(status) must be granted (revoke path): %v", err)
	}
	// DENIED: role — privilege escalation by in-place edit is physically impossible.
	if _, err := db.App.Exec(ctx,
		`UPDATE admin_credentials SET role='ADMIN' WHERE actor='op_grant'`); !isPermDenied(err) {
		t.Fatalf("tcp_app UPDATE(role) MUST be permission-denied, got %v", err)
	}
	// DENIED: scope.
	if _, err := db.App.Exec(ctx,
		`UPDATE admin_credentials SET scope='telco:X' WHERE actor='op_grant'`); !isPermDenied(err) {
		t.Fatalf("tcp_app UPDATE(scope) MUST be permission-denied, got %v", err)
	}
}

// Pre-pen-test hardening (0048): with the role DEFAULT dropped, a credential
// INSERT that OMITS role fails (NOT NULL, no default) — the dead role-less path
// can no longer silently mint an ACTIVE ADMIN. Every INSERT must name a role.
func TestOperatorProvisioning_RoleIsMandatoryNoDefault(t *testing.T) {
	db := testutil.MustSetup(t, "opprov_norole")
	ctx := context.Background()
	_, err := db.App.Exec(ctx,
		`INSERT INTO admin_credentials (admin_id, actor, key_hash, scope) VALUES ($1,$2,$3,$4)`,
		"adm_nr", "op_norole", []byte{0x09, 0x08, 0x07}, "*")
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != "23502" { // not_null_violation
		t.Fatalf("role-omitting INSERT must fail NOT NULL (23502) with no ADMIN default, got %v", err)
	}
}

// The two-actor create rule and one-open convergence are schema-enforced.
func TestOperatorProvisioning_TwoActorAndConvergence(t *testing.T) {
	db := testutil.MustSetup(t, "opprov_2actor")
	ctx := context.Background()
	reqs := repo.OperatorRequests{}

	// maker proposes a create.
	if err := repo.WithPlatformTx(ctx, db.App, func(tx pgx.Tx) error {
		return reqs.Insert(ctx, tx, repo.OperatorRequest{
			RequestID: "req1", Actor: "new_op", Role: "OPS", Scope: "*", Reason: "onboard", RequestedBy: "admin_a"})
	}); err != nil {
		t.Fatalf("maker insert: %v", err)
	}

	// a second open proposal for the SAME actor converges to a single open request.
	err := repo.WithPlatformTx(ctx, db.App, func(tx pgx.Tx) error {
		return reqs.Insert(ctx, tx, repo.OperatorRequest{
			RequestID: "req2", Actor: "new_op", Role: "SUPPORT", Scope: "*", Reason: "dup", RequestedBy: "admin_b"})
	})
	if !errors.Is(err, repo.ErrOpenRequestExists) {
		t.Fatalf("want ErrOpenRequestExists, got %v", err)
	}

	// self-approve is refused by the schema CHECK (approver must differ).
	err = repo.WithPlatformTx(ctx, db.App, func(tx pgx.Tx) error {
		if _, e := reqs.ClaimRequestedByID(ctx, tx, "req1"); e != nil {
			return e
		}
		return reqs.Decide(ctx, tx, "req1", "admin_a", "APPLIED") // same as requested_by
	})
	if !errors.Is(err, repo.ErrSelfApproveOperator) {
		t.Fatalf("want ErrSelfApproveOperator, got %v", err)
	}

	// a DISTINCT approver applies.
	if err := repo.WithPlatformTx(ctx, db.App, func(tx pgx.Tx) error {
		if _, e := reqs.ClaimRequestedByID(ctx, tx, "req1"); e != nil {
			return e
		}
		return reqs.Decide(ctx, tx, "req1", "admin_b", "APPLIED")
	}); err != nil {
		t.Fatalf("distinct approver should apply: %v", err)
	}
}

// Revoke deactivates via UPDATE(status) and is idempotent; the row is kept
// (REVOKED) so the actor identity is retired, not reused.
func TestOperatorProvisioning_RevokeCredential(t *testing.T) {
	db := testutil.MustSetup(t, "opprov_revoke")
	ctx := context.Background()
	admins := &repo.Admins{Pool: db.App}

	if err := repo.WithPlatformTx(ctx, db.App, func(tx pgx.Tx) error {
		return admins.CreateWithRoleTx(ctx, tx, "adm_r", "op_rev", "key-op-rev-000001", "FINANCE", "*")
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	var ok bool
	if err := repo.WithPlatformTx(ctx, db.App, func(tx pgx.Tx) error {
		var e error
		ok, e = admins.RevokeCredential(ctx, tx, "op_rev")
		return e
	}); err != nil || !ok {
		t.Fatalf("revoke should succeed ok=true (ok=%v err=%v)", ok, err)
	}

	// idempotent: a second revoke is a no-op (ok=false).
	if err := repo.WithPlatformTx(ctx, db.App, func(tx pgx.Tx) error {
		var e error
		ok, e = admins.RevokeCredential(ctx, tx, "op_rev")
		return e
	}); err != nil || ok {
		t.Fatalf("second revoke should be no-op ok=false (ok=%v err=%v)", ok, err)
	}

	var status string
	if err := db.App.QueryRow(ctx, `SELECT status FROM admin_credentials WHERE actor='op_rev'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "REVOKED" {
		t.Fatalf("want REVOKED, got %s", status)
	}
}
