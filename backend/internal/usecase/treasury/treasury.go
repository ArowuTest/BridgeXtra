// Package treasury is the M3d guardrail engine (V1-TRE, EDG-024/025).
//
// Evaluation happens INSIDE the confirm transaction, after the funding-pool
// row is locked — the pool lock is the serialization point, so concurrent
// confirms measure sequentially and the cap cannot be raced past. A breach
// aborts the confirm; the TRIP (evidence + programme suspension, fail
// closed) is recorded in a separate transaction that survives the abort.
// Re-arming is a two-person decision, schema-enforced.
package treasury

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
)

// BreachError reports a guardrail breach detected inside a confirm
// transaction. The confirm aborts; the caller records the trip out-of-band.
type BreachError struct {
	Guardrail string // DAILY_DISBURSED | OPEN_EXPOSURE
	Measured  entity.Money
	Limit     entity.Money
}

func (b *BreachError) Error() string {
	return fmt.Sprintf("treasury guardrail %s breached: measured %s, limit %s", b.Guardrail, b.Measured, b.Limit)
}

// ErrProgrammeSuspended: lending is stopped on this programme (a guardrail
// tripped, or an operator suspended it). Customer-safe at the boundary.
var ErrProgrammeSuspended = errors.New("treasury: programme suspended — lending stopped")

type guardrailCfg struct {
	MaxDailyDisbursedMinor      int64  `json:"max_daily_disbursed_minor"`
	MaxOpenExposureBpsCommitted int64  `json:"max_open_exposure_bps_of_committed"`
	TripAction                  string `json:"trip_action"`
	Rearm                       string `json:"rearm"`
}

type Service struct {
	Pool   *pgxpool.Pool // tcp_app
	Config *configsvc.Service
	Log    *slog.Logger

	trips repo.GuardrailTrips
	audit repo.Audit
}

func New(pool *pgxpool.Pool, cfg *configsvc.Service, log *slog.Logger) *Service {
	return &Service{Pool: pool, Config: cfg, Log: log}
}

// EvaluateInTx measures both guardrails INSIDE the caller's transaction
// (call with the pool row already locked). includeDisbursed is the amount
// the in-flight confirm adds to today's total. Returns *BreachError on
// breach; nil when lending may proceed.
func (s *Service) EvaluateInTx(ctx context.Context, tx pgx.Tx, programmeID, poolID string, includeDisbursed entity.Money) error {
	cv, err := s.Config.ActiveAt(ctx, "treasury.guardrails", "programme:"+programmeID, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("treasury.guardrails config: %w", err)
	}
	var gc guardrailCfg
	if err := json.Unmarshal(cv.Content, &gc); err != nil {
		return err
	}
	cur := includeDisbursed.Currency()

	// DAILY_DISBURSED: today's originations that reached the money path
	// (everything not declined/failed), plus the in-flight confirm.
	var todayMinor int64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(disbursed_minor),0) FROM advances
		WHERE programme_id = $1
		  AND accepted_at >= date_trunc('day', now())
		  AND state NOT IN ('DECLINED','FULFILMENT_FAILED')`, programmeID).Scan(&todayMinor); err != nil {
		return err
	}
	today, err := entity.NewMoney(todayMinor, cur)
	if err != nil {
		return err
	}
	limit, err := entity.NewMoney(gc.MaxDailyDisbursedMinor, cur)
	if err != nil {
		return err
	}
	if c, err := today.Cmp(limit); err != nil {
		return err
	} else if c > 0 {
		return &BreachError{Guardrail: "DAILY_DISBURSED", Measured: today, Limit: limit}
	}

	// OPEN_EXPOSURE: reserved+utilised vs bps of committed, straight off the
	// locked pool row.
	var reserved, utilised, committed int64
	if err := tx.QueryRow(ctx, `
		SELECT reserved_minor, utilised_minor, committed_minor
		FROM funding_pools WHERE pool_id = $1`, poolID).Scan(&reserved, &utilised, &committed); err != nil {
		return err
	}
	open, err := entity.NewMoney(reserved+utilised, cur)
	if err != nil {
		return err
	}
	committedM, err := entity.NewMoney(committed, cur)
	if err != nil {
		return err
	}
	expLimit, err := committedM.PercentBps(gc.MaxOpenExposureBpsCommitted)
	if err != nil {
		return err
	}
	if c, err := open.Cmp(expLimit); err != nil {
		return err
	} else if c > 0 {
		return &BreachError{Guardrail: "OPEN_EXPOSURE", Measured: open, Limit: expLimit}
	}
	return nil
}

// RecordTrip persists the trip evidence AND suspends the programme (fail
// closed) in its own transaction — it must survive the aborted confirm.
// Concurrent detections converge on one open trip per (programme, guardrail).
func (s *Service) RecordTrip(ctx context.Context, telcoID, programmeID string, breach *BreachError) error {
	tctx := platform.WithTenant(ctx, telcoID)
	return repo.WithTenantTx(tctx, s.Pool, func(tx pgx.Tx) error {
		created, err := s.trips.Insert(ctx, tx, repo.GuardrailTrip{
			TripID: platform.NewID("trp"), TelcoID: telcoID, ProgrammeID: programmeID,
			Guardrail: breach.Guardrail, Measured: breach.Measured, Limit: breach.Limit,
		})
		if err != nil {
			return err
		}
		if err := (repo.Programmes{}).SetStatus(ctx, tx, programmeID, entity.ProgrammeActive, entity.ProgrammeSuspended); err != nil &&
			!errors.Is(err, repo.ErrNotFound) {
			// Already suspended by a concurrent trip: converged, fine.
			return err
		}
		if created {
			s.Log.Error("GUARDRAIL TRIPPED — programme suspended (fail closed)",
				"programme", programmeID, "guardrail", breach.Guardrail,
				"measured", breach.Measured.String(), "limit", breach.Limit.String())
		}
		return s.audit.Insert(ctx, tx, entity.AuditEvent{
			ID: platform.NewID("aud"), TelcoID: telcoID, Actor: "system:guardrail",
			Action: "guardrail.tripped", TargetType: "programme", TargetID: programmeID,
			Reason: breach.Error(),
		})
	})
}

// RequestRearm is the maker step on an open trip.
func (s *Service) RequestRearm(ctx context.Context, telcoID, tripID, actor, reason string) error {
	if actor == "" || reason == "" {
		return fmt.Errorf("actor and reason are required")
	}
	tctx := platform.WithTenant(ctx, telcoID)
	return repo.WithTenantTx(tctx, s.Pool, func(tx pgx.Tx) error {
		if err := s.trips.RequestRearm(ctx, tx, tripID, actor); err != nil {
			return err
		}
		return s.audit.Insert(ctx, tx, entity.AuditEvent{
			ID: platform.NewID("aud"), TelcoID: telcoID, Actor: actor,
			Action: "guardrail.rearm_requested", TargetType: "guardrail_trip", TargetID: tripID,
			Reason: reason,
		})
	})
}

// ApproveRearm is the checker step (distinct actor, schema-enforced): the
// trip closes and the programme resumes lending.
func (s *Service) ApproveRearm(ctx context.Context, telcoID, tripID, approver string) error {
	if approver == "" {
		return fmt.Errorf("approver is required")
	}
	tctx := platform.WithTenant(ctx, telcoID)
	return repo.WithTenantTx(tctx, s.Pool, func(tx pgx.Tx) error {
		trip, err := s.trips.ApproveRearm(ctx, tx, tripID, approver)
		if err != nil {
			return err
		}
		// Resume ONLY when no other guardrail still holds the programme.
		open, err := s.trips.CountOpenForProgramme(ctx, tx, trip.ProgrammeID)
		if err != nil {
			return err
		}
		if open == 0 {
			if err := (repo.Programmes{}).SetStatus(ctx, tx, trip.ProgrammeID, entity.ProgrammeSuspended, entity.ProgrammeActive); err != nil {
				return err
			}
		}
		s.Log.Warn("guardrail re-armed (two-person decision)",
			"trip", tripID, "programme", trip.ProgrammeID, "approved_by", approver, "still_open", open)
		return s.audit.Insert(ctx, tx, entity.AuditEvent{
			ID: platform.NewID("aud"), TelcoID: telcoID, Actor: approver,
			Action: "guardrail.rearmed", TargetType: "guardrail_trip", TargetID: tripID,
			Reason: "re-arm approved",
		})
	})
}
