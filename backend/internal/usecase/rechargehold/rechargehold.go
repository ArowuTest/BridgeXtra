// Package rechargehold is the governed (maker-checker) release flow for
// blast-radius-HELD webhook recharges (Phase 1 S2.3). A held event is money
// that has NOT been ingested; releasing it is a two-actor decision that feeds
// it to the recovery core, and rejecting it is the safe single-actor direction.
//
// Crash-safety (the reviewer-verified ordering): recovery.Ingest manages its
// own transaction, so release cannot be single-tx with the state flip. The
// order is guards -> INGEST (idempotent per source_event_id) -> atomic
// claim-update that re-checks the guards. A crash between ingest and claim
// leaves HELD-but-ingested; retrying the approval replays the ingest (byte-
// exact, Replayed=true) and completes the transition — money is never lost and
// never doubled. The residual approve-vs-reject sub-second race is
// audit-visible and recon-caught (same accepted class as the daily-ceiling
// TOCTOU).
package rechargehold

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/recovery"
)

var (
	// ErrNotActionable — the hold is missing, already decided, or not in the
	// state the action requires.
	ErrNotActionable = errors.New("rechargehold: hold is not actionable")
	// ErrSameActor — the approver must be a different operator than the maker.
	ErrSameActor = errors.New("rechargehold: approver must differ from requester (four-eyes)")
)

type Service struct {
	Pool     *pgxpool.Pool // tcp_app (tenant tx)
	Recovery *recovery.Service
	Log      *slog.Logger

	held  repo.HeldRecharge
	audit repo.Audit
}

func New(pool *pgxpool.Pool, rec *recovery.Service, log *slog.Logger) *Service {
	return &Service{Pool: pool, Recovery: rec, Log: log}
}

// ListOpen returns the telco's reviewable HELD queue.
func (s *Service) ListOpen(ctx context.Context, telcoID string, limit int) ([]repo.HeldRow, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	tctx := platform.WithTenant(ctx, telcoID)
	var out []repo.HeldRow
	err := repo.WithTenantTx(tctx, s.Pool, func(tx pgx.Tx) error {
		var e error
		out, e = s.held.ListOpen(ctx, tx, limit)
		return e
	})
	return out, err
}

// RequestRelease is the MAKER action: nominate an open hold for release. A
// second request, or a decided hold, is refused.
func (s *Service) RequestRelease(ctx context.Context, telcoID, heldID, actor, reason string) error {
	if actor == "" || reason == "" {
		return fmt.Errorf("actor and reason are required")
	}
	tctx := platform.WithTenant(ctx, telcoID)
	return repo.WithTenantTx(tctx, s.Pool, func(tx pgx.Tx) error {
		ok, err := s.held.RequestRelease(ctx, tx, heldID, actor)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%w: not open or already requested", ErrNotActionable)
		}
		return s.audit.Insert(ctx, tx, entity.AuditEvent{
			ID: platform.NewID("aud"), TelcoID: telcoID, Actor: actor,
			Action: "recharge_hold.release_requested", TargetType: "held_recharge",
			TargetID: heldID, Reason: reason,
		})
	})
}

// ApproveRelease is the CHECKER action: a DISTINCT operator approves, the held
// event is fed to the recovery money core, and the hold closes RELEASED.
// Idempotent: retrying after a crash (or a concurrent duplicate approval)
// replays the ingest byte-exact and converges on RELEASED.
func (s *Service) ApproveRelease(ctx context.Context, telcoID, heldID, approver string) (recovery.IngestResult, error) {
	if approver == "" {
		return recovery.IngestResult{}, fmt.Errorf("approver is required")
	}
	tctx := platform.WithTenant(ctx, telcoID)

	// Pre-guards (fail fast, nothing ingested on refusal).
	var row repo.HeldRow
	if err := repo.WithTenantTx(tctx, s.Pool, func(tx pgx.Tx) error {
		var e error
		row, e = s.held.Get(ctx, tx, heldID)
		return e
	}); err != nil {
		return recovery.IngestResult{}, fmt.Errorf("%w: %v", ErrNotActionable, err)
	}
	switch {
	case row.Status == "RELEASED":
		// Already decided — idempotent success path continues below via replay.
	case row.Status != "HELD":
		return recovery.IngestResult{}, fmt.Errorf("%w: status %s", ErrNotActionable, row.Status)
	case row.RequestedBy == "":
		return recovery.IngestResult{}, fmt.Errorf("%w: release not requested", ErrNotActionable)
	case row.RequestedBy == approver:
		return recovery.IngestResult{}, ErrSameActor
	}

	amount, err := entity.NewMoney(row.AmountMinor, entity.Currency(row.Currency))
	if err != nil {
		return recovery.IngestResult{}, fmt.Errorf("held amount invalid: %w", err)
	}

	// INGEST FIRST (idempotent per source_event_id): a crash after this point
	// leaves HELD-but-ingested, and a retried approval converges safely.
	res, err := s.Recovery.Ingest(tctx, recovery.IngestCmd{
		SourceEventID: row.SourceEventID, // already "wh:"-namespaced by the webhook
		MSISDNToken:   row.MSISDNToken,
		Amount:        amount,
		OccurredAt:    row.OccurredAt,
		CorrelationID: correlationOr(ctx, "rel-"+heldID),
	})
	if err != nil {
		return recovery.IngestResult{}, fmt.Errorf("release ingest: %w", err)
	}

	// Atomic claim re-checking every guard; 0 rows after a successful ingest
	// means either a concurrent approval won (RELEASED — idempotent success) or
	// a concurrent reject crossed the ingest (loud inconsistency, surfaced).
	err = repo.WithTenantTx(tctx, s.Pool, func(tx pgx.Tx) error {
		ok, e := s.held.ClaimReleased(ctx, tx, heldID, approver)
		if e != nil {
			return e
		}
		if !ok {
			cur, e := s.held.Get(ctx, tx, heldID)
			if e != nil {
				return e
			}
			if cur.Status == "RELEASED" {
				return nil // concurrent/retried approval already closed it
			}
			s.Log.Error("HELD release inconsistency: event ingested but hold not releasable — reconcile",
				"telco", telcoID, "held", heldID, "status", cur.Status)
			return fmt.Errorf("%w: status %s changed during release (event ingested — reconcile)", ErrNotActionable, cur.Status)
		}
		return s.audit.Insert(ctx, tx, entity.AuditEvent{
			ID: platform.NewID("aud"), TelcoID: telcoID, Actor: approver,
			Action: "recharge_hold.released", TargetType: "held_recharge",
			TargetID: heldID, Reason: "maker-checker release; recovery " + res.RecoveryEventID,
		})
	})
	if err != nil {
		return res, err
	}
	s.Log.Warn("HELD recharge released (maker-checker) — ingested into recovery",
		"telco", telcoID, "held", heldID, "approver", approver,
		"recovery_event", res.RecoveryEventID, "replayed", res.Replayed)
	return res, nil
}

// Reject closes an open hold WITHOUT ingesting — the safe direction, single
// actor (a maker may withdraw their own request), fully audited.
func (s *Service) Reject(ctx context.Context, telcoID, heldID, actor, reason string) error {
	if actor == "" || reason == "" {
		return fmt.Errorf("actor and reason are required")
	}
	tctx := platform.WithTenant(ctx, telcoID)
	return repo.WithTenantTx(tctx, s.Pool, func(tx pgx.Tx) error {
		ok, err := s.held.MarkRejected(ctx, tx, heldID)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%w: not open", ErrNotActionable)
		}
		return s.audit.Insert(ctx, tx, entity.AuditEvent{
			ID: platform.NewID("aud"), TelcoID: telcoID, Actor: actor,
			Action: "recharge_hold.rejected", TargetType: "held_recharge",
			TargetID: heldID, Reason: reason,
		})
	})
}

// correlationOr uses the ambient correlation id when present, else a
// deterministic release-scoped one (recovery requires it non-empty).
func correlationOr(ctx context.Context, fallback string) string {
	if c := platform.CorrelationFrom(ctx); c != "" {
		return c
	}
	if len(fallback) > 64 {
		fallback = fallback[:64]
	}
	return fallback
}
