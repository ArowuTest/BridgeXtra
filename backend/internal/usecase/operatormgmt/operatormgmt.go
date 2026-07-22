// Package operatormgmt is the governed operator-provisioning surface (v1):
// CREATE (four-eyes / maker-checker) and REVOKE (single-actor). It mirrors the
// configsvc maker-checker discipline — platform-scope transactions, a DISTINCT
// approver enforced in code AND by the schema, an append-only audit row per
// governance step. Write-once is enforced by the DB grant (migration 0047): the
// only mutations are create (INSERT) and revoke (UPDATE status), so a privilege
// change is impossible except by revoke-and-recreate, which fires the M4A-F1
// kill-switch. v1 deliberately has NO in-place role/scope change and NO
// escalation classifier (post-pen-test v2 with an operators identity model).
package operatormgmt

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
)

var (
	// ErrSelfApprove: the approver of a create request must differ from its
	// proposer (four-eyes). Backed by the schema CHECK.
	ErrSelfApprove = errors.New("operatormgmt: a create request cannot be approved by its proposer")
	// ErrBadRequest: malformed proposal (bad role/scope, missing field, or an
	// actor identity that already exists).
	ErrBadRequest = errors.New("operatormgmt: invalid operator request")
	// ErrNotActive: revoke target is absent or already revoked (idempotent no-op).
	ErrNotActive = errors.New("operatormgmt: operator not found or already revoked")
)

var (
	validRoles = map[string]bool{"ADMIN": true, "RISK": true, "FINANCE": true, "OPS": true, "SUPPORT": true}
	scopeRe    = regexp.MustCompile(`^(\*|global|programme:[A-Za-z0-9_]+|telco:[A-Za-z0-9_]+)$`)
)

// Service runs on a platform-role pool (operator credentials are platform data).
type Service struct {
	Pool   *pgxpool.Pool
	admins *repo.Admins
	reqs   repo.OperatorRequests
	audit  repo.Audit
	log    *slog.Logger
}

func New(pool *pgxpool.Pool, log *slog.Logger) *Service {
	return &Service{Pool: pool, admins: &repo.Admins{Pool: pool}, log: log}
}

// ProposeCreate is the MAKER step: records a REQUESTED create for a DISTINCT
// admin to approve. Creating an operator is a grant from nothing — always
// four-eyes. Rejects an actor identity that already exists (permanent identity).
func (s *Service) ProposeCreate(ctx context.Context, actor, role, scope, reason, requestedBy string) (repo.OperatorRequest, error) {
	if actor == "" || reason == "" || requestedBy == "" {
		return repo.OperatorRequest{}, fmt.Errorf("%w: actor, reason and requester are required", ErrBadRequest)
	}
	if !validRoles[role] {
		return repo.OperatorRequest{}, fmt.Errorf("%w: role %q", ErrBadRequest, role)
	}
	if !scopeRe.MatchString(scope) {
		return repo.OperatorRequest{}, fmt.Errorf("%w: scope %q", ErrBadRequest, scope)
	}
	r := repo.OperatorRequest{
		RequestID: platform.NewID("opr"), Actor: actor, Role: role, Scope: scope,
		Reason: reason, RequestedBy: requestedBy,
	}
	err := repo.WithPlatformTx(ctx, s.Pool, func(tx pgx.Tx) error {
		exists, err := s.admins.ExistsByActor(ctx, tx, actor)
		if err != nil {
			return err
		}
		if exists {
			return fmt.Errorf("%w: operator %q already exists", ErrBadRequest, actor)
		}
		if err := s.reqs.Insert(ctx, tx, r); err != nil {
			return err
		}
		return s.audit.Insert(ctx, tx, entity.AuditEvent{
			ID: platform.NewID("aud"), Actor: requestedBy, Action: "operator.create_requested",
			TargetType: "operator", TargetID: actor, Reason: reason,
			Detail: detail(map[string]string{"request_id": r.RequestID, "role": role, "scope": scope}),
		})
	})
	if err != nil {
		return repo.OperatorRequest{}, err
	}
	return r, nil
}

// ApproveCreate is the CHECKER step: a DISTINCT admin approves; the credential is
// created and the one-time plaintext key returned (stored hash-only — it is never
// persisted or logged in plaintext, and can never be retrieved again).
func (s *Service) ApproveCreate(ctx context.Context, requestID, approver string) (string, error) {
	if approver == "" {
		return "", fmt.Errorf("%w: approver required", ErrBadRequest)
	}
	key, err := genKey()
	if err != nil {
		return "", err
	}
	err = repo.WithPlatformTx(ctx, s.Pool, func(tx pgx.Tx) error {
		req, err := s.reqs.ClaimRequestedByID(ctx, tx, requestID)
		if err != nil {
			return err
		}
		if req.RequestedBy == approver { // schema CHECK backs this too
			return ErrSelfApprove
		}
		if err := s.admins.CreateWithRoleTx(ctx, tx, platform.NewID("adm"), req.Actor, key, req.Role, req.Scope); err != nil {
			return err
		}
		if err := s.reqs.Decide(ctx, tx, requestID, approver, "APPLIED"); err != nil {
			return err
		}
		return s.audit.Insert(ctx, tx, entity.AuditEvent{
			ID: platform.NewID("aud"), Actor: approver, Action: "operator.created",
			TargetType: "operator", TargetID: req.Actor, Reason: req.Reason,
			Detail: detail(map[string]string{"request_id": requestID, "role": req.Role, "scope": req.Scope, "requested_by": req.RequestedBy}),
		})
	})
	if err != nil {
		return "", err
	}
	return key, nil
}

// RejectCreate closes a pending request without provisioning. Also a distinct
// second actor (the schema forbids self-decision) — consistent with the two-actor
// record; a maker who wants to undo asks another admin to reject.
func (s *Service) RejectCreate(ctx context.Context, requestID, approver, reason string) error {
	if approver == "" {
		return fmt.Errorf("%w: approver required", ErrBadRequest)
	}
	return repo.WithPlatformTx(ctx, s.Pool, func(tx pgx.Tx) error {
		req, err := s.reqs.ClaimRequestedByID(ctx, tx, requestID)
		if err != nil {
			return err
		}
		if req.RequestedBy == approver {
			return ErrSelfApprove
		}
		if err := s.reqs.Decide(ctx, tx, requestID, approver, "REJECTED"); err != nil {
			return err
		}
		return s.audit.Insert(ctx, tx, entity.AuditEvent{
			ID: platform.NewID("aud"), Actor: approver, Action: "operator.create_rejected",
			TargetType: "operator", TargetID: req.Actor, Reason: reason,
			Detail: detail(map[string]string{"request_id": requestID}),
		})
	})
}

// Revoke deactivates an operator — SINGLE-actor (reducing access is never gated).
// The credential row is kept (status REVOKED) so the actor identity is retired,
// and the M4A-F1 kill-switch ends the operator's live sessions immediately.
func (s *Service) Revoke(ctx context.Context, actor, actingAdmin, reason string) error {
	if actor == "" || actingAdmin == "" || reason == "" {
		return fmt.Errorf("%w: actor, acting-admin and reason are required", ErrBadRequest)
	}
	return repo.WithPlatformTx(ctx, s.Pool, func(tx pgx.Tx) error {
		ok, err := s.admins.RevokeCredential(ctx, tx, actor)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNotActive
		}
		return s.audit.Insert(ctx, tx, entity.AuditEvent{
			ID: platform.NewID("aud"), Actor: actingAdmin, Action: "operator.revoked",
			TargetType: "operator", TargetID: actor, Reason: reason,
		})
	})
}

// ListOperators / ListOpenRequests back the admin console (reads).
func (s *Service) ListOperators(ctx context.Context) ([]repo.Operator, error) {
	return s.admins.ListOperators(ctx, s.Pool)
}

func (s *Service) ListOpenRequests(ctx context.Context) ([]repo.OperatorRequest, error) {
	return s.reqs.ListOpen(ctx, s.Pool)
}

// genKey mints a strong random access key (crypto/rand). Returned exactly once;
// stored only as a sha256 hash by CreateWithRoleTx.
func genKey() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("operatormgmt: key generation: %w", err)
	}
	return "op-" + hex.EncodeToString(b), nil
}

func detail(v map[string]string) []byte {
	b, _ := json.Marshal(v)
	return b
}
