// Package rechargehold is the governed (maker-checker) release flow for
// blast-radius-HELD webhook recharges (Phase 1 S2.3). A held event is money
// that has NOT been ingested; releasing it is a two-actor decision that feeds
// it to the recovery core, and rejecting it is the safe single-actor direction.
//
// Crash-safety + BX-HIGH-002 (CLAIM before ingest): recovery.Ingest manages its own
// transaction, so release cannot be single-tx with the state flip. The order is CLAIM
// (HELD -> RELEASE_IN_PROGRESS, four-eyes, atomic) -> INGEST (idempotent per
// source_event_id) -> FINALISE (RELEASE_IN_PROGRESS -> RELEASED). Claiming FIRST closes the
// approve-vs-reject race: a Reject requires HELD, so once a hold is claimed a reject can
// never win, and money is never booked against a hold that ends REJECTED. A crash between
// claim and finalise leaves RELEASE_IN_PROGRESS; a retried approval by the same checker
// replays the byte-exact ingest and completes the transition — money is never lost, never
// doubled, and never booked against a rejected hold.
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

	// STEP 1 (BX-HIGH-002): atomically CLAIM the hold for release — HELD -> RELEASE_IN_PROGRESS
	// with the four-eyes distinct-approver guard — BEFORE any ingest. Once claimed, a concurrent
	// Reject (which requires HELD) can no longer win, so money is never booked against a hold
	// that ends REJECTED. Crash/retry-safe: an already-RELEASE_IN_PROGRESS hold claimed by THIS
	// approver (a prior attempt) is completed below, and an already-RELEASED hold converges.
	var row repo.HeldRow
	if err := repo.WithTenantTx(tctx, s.Pool, func(tx pgx.Tx) error {
		r, e := s.held.Get(ctx, tx, heldID)
		if e != nil {
			return fmt.Errorf("%w: %v", ErrNotActionable, e)
		}
		row = r
		switch {
		case r.Status == "RELEASED":
			return nil // already released — STEP 2 replays the ingest for a consistent result
		case r.Status == "RELEASE_IN_PROGRESS":
			if r.ApprovedBy != approver {
				return fmt.Errorf("%w: release already in progress by another approver", ErrNotActionable)
			}
			return nil // our own prior claim (crash retry) — complete it below
		case r.Status != "HELD":
			return fmt.Errorf("%w: status %s", ErrNotActionable, r.Status)
		case r.RequestedBy == "":
			return fmt.Errorf("%w: release not requested", ErrNotActionable)
		case r.RequestedBy == approver:
			return ErrSameActor
		}
		// HELD, requested by a distinct maker: claim it now, BEFORE ingest.
		ok, e := s.held.ClaimForRelease(ctx, tx, heldID, approver)
		if e != nil {
			return e
		}
		if !ok {
			// A concurrent reject/approve changed the hold between the read and the claim.
			return fmt.Errorf("%w: hold changed during claim", ErrNotActionable)
		}
		return nil
	}); err != nil {
		return recovery.IngestResult{}, err
	}

	amount, err := entity.NewMoney(row.AmountMinor, entity.Currency(row.Currency))
	if err != nil {
		return recovery.IngestResult{}, fmt.Errorf("held amount invalid: %w", err)
	}

	// STEP 2: ingest (idempotent per source_event_id). The hold is now RELEASE_IN_PROGRESS (or
	// already RELEASED), so a reject can never cross this. A failure here leaves the hold
	// RELEASE_IN_PROGRESS — retryable by a re-approval, never lost, never doubled.
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

	// STEP 3: finalise RELEASE_IN_PROGRESS -> RELEASED + audit. Idempotent when already RELEASED.
	if err := repo.WithTenantTx(tctx, s.Pool, func(tx pgx.Tx) error {
		ok, e := s.held.FinaliseReleased(ctx, tx, heldID, approver)
		if e != nil {
			return e
		}
		if !ok {
			cur, e := s.held.Get(ctx, tx, heldID)
			if e != nil {
				return e
			}
			if cur.Status == "RELEASED" {
				return nil // already finalised (concurrent/retried approval) — idempotent success
			}
			s.Log.Error("HELD finalise inconsistency: event ingested but hold not releasable — reconcile",
				"telco", telcoID, "held", heldID, "status", cur.Status)
			return fmt.Errorf("%w: status %s could not finalise (event ingested — reconcile)", ErrNotActionable, cur.Status)
		}
		return s.audit.Insert(ctx, tx, entity.AuditEvent{
			ID: platform.NewID("aud"), TelcoID: telcoID, Actor: approver,
			Action: "recharge_hold.released", TargetType: "held_recharge",
			TargetID: heldID, Reason: "maker-checker release; recovery " + res.RecoveryEventID,
		})
	}); err != nil {
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
