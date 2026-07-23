// Package scoringsched is the durable scoring scheduler (Phase 0 — arm the
// pipeline). On a config-driven cadence it runs featureingest -> scoringrun per
// active telco/programme so fresh decisions are always on file for offers.
//
// Correctness (hardened by an adversarial design pass):
//   - Exactly-once per (programme, cycle): the claim is a lease claim-or-reclaim
//     keyed on a DB-clock cycle grid (repo.ScoringCycles.ClaimOrReclaim), so
//     skewed instances agree on the bucket and a crash/FAILED cycle is rescued
//     by any healthy instance — but a SUCCEEDED/STALE cycle never re-runs. The
//     one-current-decision-per-subscriber unique index is the structural backstop.
//   - Freshness: the effective cadence is clamped so decisions keep at least
//     `headroom` spare cadences before expiry (never a NO_OFFER gap). A cycle
//     that produces no new decisions while the current ones are near expiry is a
//     loud STALE_NO_REFRESH feed incident — never a silent green.
//   - Fail-closed: missing/unreadable config skips the programme (no hardcoded
//     default); arming requires an explicit enabled:true.
//   - Failure isolation: RunDueAll isolates every telco and programme so one
//     failure never aborts its siblings.
package scoringsched

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/featureingest"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/scoringrun"
)

// Service arms and drives the scoring pipeline.
type Service struct {
	appPool    *pgxpool.Pool // tcp_app (RLS) — claims + scoring writes
	workerPool *pgxpool.Pool // tcp_worker (BYPASSRLS) — cross-tenant telco list only
	cfg        *configsvc.Service
	log        *slog.Logger
	instanceID string
	ingest     *featureingest.Service
	scorer     *scoringrun.Service
}

// New builds the scheduler. instanceID (host+pid+uuid) is recorded as the cycle
// claimant so multi-instance reclaims are attributable.
func New(appPool, workerPool *pgxpool.Pool, cfg *configsvc.Service, log *slog.Logger, instanceID string) *Service {
	return &Service{
		appPool: appPool, workerPool: workerPool, cfg: cfg, log: log, instanceID: instanceID,
		ingest: featureingest.New(appPool, cfg, log),
		scorer: scoringrun.New(appPool, cfg, log),
	}
}

// Outcome describes what one RunDueForProgramme call did (for tests and logging).
type Outcome struct {
	Claimed               bool
	SkipReason            string // "disabled" | "config_error" | "already_ran" | "" if claimed
	CycleID               string
	Status                string // terminal status when Claimed (SUCCEEDED/FAILED/STALE_NO_REFRESH)
	EffectiveCadenceHours int
	Clamped               bool
	FeatureFileID         string
	ScoringRunID          string
	Scored                int
}

type scheduleCfg struct {
	Enabled        bool `json:"enabled"`
	CadenceHours   int  `json:"cadence_hours"`
	HeadroomCycles int  `json:"headroom_cycles"`
	LeaseSeconds   int  `json:"lease_seconds"`
	MaxAttempts    int  `json:"max_attempts"`
}

// effectiveCadence clamps the configured cadence so the programme's decisions
// always keep at least `headroom` spare cadences before expiry:
// eff <= decision_valid_hours/(headroom+1), floored at 1h. Running MORE often
// than valid is always safe (scoringrun dedups a no-op replay cheaply); running
// LESS often would open a NO_OFFER gap, so the scheduler never disarms itself.
func effectiveCadence(cadenceHours, validHours, headroomCycles int) (eff int, clamped, floored bool) {
	eff = cadenceHours
	if maxEff := validHours / (headroomCycles + 1); maxEff < eff {
		eff, clamped = maxEff, true
	}
	if eff < 1 {
		eff, floored = 1, true
	}
	return eff, clamped, floored
}

// classifyTerminal decides a claimed cycle's terminal status. New decisions this
// cycle (scored>0) => SUCCEEDED. No new decisions and the current ones are within
// one effective cadence of expiry (or absent) => STALE_NO_REFRESH, a loud feed
// incident. No new decisions but current ones comfortably fresh => SUCCEEDED (the
// feed simply hasn't changed within this decision's validity window).
func classifyTerminal(scored int, freshestValidUntil time.Time, haveDecisions bool, now time.Time, effHours int) string {
	if scored > 0 {
		return entity.CycleSucceeded
	}
	if haveDecisions && freshestValidUntil.After(now.Add(time.Duration(effHours)*time.Hour)) {
		return entity.CycleSucceeded
	}
	return entity.CycleStaleNoRefresh
}

// RunDueForProgramme is the core: read config (fail-closed), clamp the cadence,
// claim-or-reclaim the current cycle, and — if claimed — ingest (bound to one
// file) then score, closing the cycle SUCCEEDED / STALE_NO_REFRESH / FAILED.
// `now` is used for the freshness comparison and logging; the cycle grid itself
// is computed from the database clock inside the claim.
func (s *Service) RunDueForProgramme(ctx context.Context, telcoID, programmeID string, now time.Time) (Outcome, error) {
	tctx := platform.WithTenant(ctx, telcoID)

	// 1. scoring.schedule (programme -> global). Fail-closed: no config => skip.
	sched, err := s.readSchedule(ctx, programmeID)
	if err != nil {
		s.log.Error("scoring.schedule config error — programme NOT armed (fail-closed)",
			"telco", telcoID, "programme", programmeID, "err", err)
		return Outcome{SkipReason: "config_error"}, nil
	}
	if !sched.Enabled {
		return Outcome{SkipReason: "disabled"}, nil
	}

	// 2. decision_valid_hours from scoring.policy (programme -> global). Fail-closed.
	validHours, err := s.readDecisionValidHours(ctx, programmeID)
	if err != nil {
		s.log.Error("scoring.policy config error — programme NOT armed (fail-closed)",
			"telco", telcoID, "programme", programmeID, "err", err)
		return Outcome{SkipReason: "config_error"}, nil
	}

	// 3. Clamp for freshness headroom (never skip on cadence>valid).
	eff, clamped, floored := effectiveCadence(sched.CadenceHours, validHours, sched.HeadroomCycles)
	switch {
	case floored:
		s.log.Error("CONFIG_ERROR scoring cadence: decision_valid_hours too small for headroom — running at 1h floor",
			"telco", telcoID, "programme", programmeID, "valid_hours", validHours, "headroom_cycles", sched.HeadroomCycles)
	case clamped:
		s.log.Warn("CONFIG_CLAMP scoring cadence clamped for freshness headroom",
			"telco", telcoID, "programme", programmeID,
			"configured_cadence_hours", sched.CadenceHours, "effective_cadence_hours", eff, "valid_hours", validHours)
	}
	windowSeconds := eff * 3600

	// 4. Claim-or-reclaim the current cycle.
	var claimed bool
	var cycleID, boundFileID string
	var attempts int
	if err := repo.WithTenantTx(tctx, s.appPool, func(tx pgx.Tx) error {
		var e error
		claimed, cycleID, boundFileID, attempts, e = (repo.ScoringCycles{}).ClaimOrReclaim(
			ctx, tx, telcoID, programmeID, s.instanceID, windowSeconds, eff, sched.LeaseSeconds)
		return e
	}); err != nil {
		return Outcome{}, fmt.Errorf("claim: %w", err)
	}
	if !claimed {
		return Outcome{SkipReason: "already_ran", EffectiveCadenceHours: eff, Clamped: clamped}, nil
	}
	out := Outcome{Claimed: true, CycleID: cycleID, EffectiveCadenceHours: eff, Clamped: clamped}

	// 5. Reclaim ceiling — park the cycle rather than hot-loop a broken pipeline.
	if attempts > sched.MaxAttempts {
		s.failCycle(ctx, tctx, cycleID, "attempts_exhausted")
		s.log.Error("scoring cycle attempts exhausted — parked FAILED",
			"telco", telcoID, "programme", programmeID, "cycle", cycleID, "attempts", attempts, "max", sched.MaxAttempts)
		out.Status = entity.CycleFailed
		return out, nil
	}

	// 6. Ingest, bound to one file (reused on reclaim so scoringrun replays).
	fileID := boundFileID
	if fileID == "" {
		sum, err := s.ingest.Run(ctx, telcoID)
		if err != nil {
			s.failCycle(ctx, tctx, cycleID, "ingest: "+err.Error())
			out.Status = entity.CycleFailed
			return out, fmt.Errorf("ingest %s/%s: %w", telcoID, programmeID, err)
		}
		fileID = sum.FeatureFileID
		if err := repo.WithTenantTx(tctx, s.appPool, func(tx pgx.Tx) error {
			return (repo.ScoringCycles{}).BindFeatureFile(ctx, tx, cycleID, fileID)
		}); err != nil {
			s.failCycle(ctx, tctx, cycleID, "bind_file: "+err.Error())
			out.Status = entity.CycleFailed
			return out, fmt.Errorf("bind feature file: %w", err)
		}
	}
	out.FeatureFileID = fileID

	// 7. Score (idempotent per (file, policy, programme)).
	res, err := s.scorer.Run(ctx, telcoID, programmeID, fileID)
	if err != nil {
		s.failCycle(ctx, tctx, cycleID, "score: "+err.Error())
		out.Status = entity.CycleFailed
		return out, fmt.Errorf("score %s/%s: %w", telcoID, programmeID, err)
	}
	out.ScoringRunID = res.Run.ScoringRunID
	out.Scored = res.Scored

	// 8. Terminal. New decisions written (Scored>0) => SUCCEEDED. Otherwise this
	// cycle produced nothing new; if the current decisions are within one cadence
	// of expiry the upstream feed is not refreshing — a loud STALE_NO_REFRESH
	// incident, never a silent green (decisions will fall to NO_OFFER).
	var vu time.Time
	var have bool
	if res.Scored == 0 {
		if err := repo.WithTenantTx(tctx, s.appPool, func(tx pgx.Tx) error {
			var e error
			vu, have, e = (repo.ScoringCycles{}).FreshestValidUntil(ctx, tx, programmeID)
			return e
		}); err != nil {
			s.failCycle(ctx, tctx, cycleID, "freshness_probe: "+err.Error())
			out.Status = entity.CycleFailed
			return out, fmt.Errorf("freshness probe: %w", err)
		}
	}
	status := classifyTerminal(res.Scored, vu, have, now, eff)

	if err := repo.WithTenantTx(tctx, s.appPool, func(tx pgx.Tx) error {
		if status == entity.CycleStaleNoRefresh {
			return (repo.ScoringCycles{}).MarkStaleNoRefresh(ctx, tx, cycleID, res.Run.ScoringRunID)
		}
		return (repo.ScoringCycles{}).MarkSucceeded(ctx, tx, cycleID, res.Run.ScoringRunID, res.Scored)
	}); err != nil {
		return out, fmt.Errorf("mark terminal: %w", err)
	}
	out.Status = status
	if status == entity.CycleStaleNoRefresh {
		s.log.Error("FEED_STALE scoring cycle — upstream served no new data and decisions are near expiry; offers will fall to NO_OFFER unless the feed refreshes",
			"telco", telcoID, "programme", programmeID, "cycle", cycleID, "run", res.Run.ScoringRunID)
	} else {
		s.log.Info("scoring cycle succeeded",
			"telco", telcoID, "programme", programmeID, "cycle", cycleID,
			"run", res.Run.ScoringRunID, "scored", res.Scored, "effective_cadence_hours", eff)
	}
	return out, nil
}

// RunDueAll sweeps every active telco/programme and runs its due cycle. Three
// isolation layers — the top-level telco list, each telco, and each programme —
// so one tenant's or programme's failure (config error, ingest outage, panic)
// never aborts its siblings. This is the tick the worker loop and the -score
// one-shot both call.
func (s *Service) RunDueAll(ctx context.Context, now time.Time) {
	telcos, err := (&repo.Telcos{Pool: s.workerPool}).ListActive(ctx)
	if err != nil {
		s.log.Error("scoring scheduler: list telcos — tick skipped", "err", err)
		return
	}
	for _, tc := range telcos {
		s.runTelco(ctx, tc.TelcoID, now)
	}
}

func (s *Service) runTelco(ctx context.Context, telcoID string, now time.Time) {
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("scoring scheduler: telco panic recovered", "telco", telcoID, "panic", r)
		}
	}()
	tctx := platform.WithTenant(ctx, telcoID)
	var progs []entity.Programme
	if err := repo.WithTenantTx(tctx, s.appPool, func(tx pgx.Tx) error {
		var e error
		progs, e = (repo.Programmes{}).ListForTenant(tctx, tx)
		return e
	}); err != nil {
		s.log.Error("scoring scheduler: list programmes", "telco", telcoID, "err", err)
		return
	}
	for _, p := range progs {
		if p.Status != entity.ProgrammeActive {
			continue
		}
		s.runProgramme(ctx, telcoID, p.ProgrammeID, now)
	}
}

func (s *Service) runProgramme(ctx context.Context, telcoID, programmeID string, now time.Time) {
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("scoring scheduler: programme panic recovered",
				"telco", telcoID, "programme", programmeID, "panic", r)
		}
	}()
	if _, err := s.RunDueForProgramme(ctx, telcoID, programmeID, now); err != nil {
		s.log.Error("scoring scheduler: programme cycle failed",
			"telco", telcoID, "programme", programmeID, "err", err)
	}
}

func (s *Service) failCycle(ctx context.Context, tctx context.Context, cycleID, msg string) {
	if err := repo.WithTenantTx(tctx, s.appPool, func(tx pgx.Tx) error {
		return (repo.ScoringCycles{}).MarkFailed(ctx, tx, cycleID, msg)
	}); err != nil {
		s.log.Error("could not mark scoring cycle FAILED", "cycle", cycleID, "err", err)
	}
}

func (s *Service) readSchedule(ctx context.Context, programmeID string) (scheduleCfg, error) {
	cv, err := s.cfg.ActiveAt(ctx, "scoring.schedule", "programme:"+programmeID, time.Now().UTC())
	if err != nil {
		return scheduleCfg{}, err
	}
	var c scheduleCfg
	if err := json.Unmarshal(cv.Content, &c); err != nil {
		return scheduleCfg{}, fmt.Errorf("parse scoring.schedule: %w", err)
	}
	if c.CadenceHours < 1 || c.HeadroomCycles < 1 || c.LeaseSeconds < 1 || c.MaxAttempts < 1 {
		return scheduleCfg{}, fmt.Errorf("scoring.schedule out of range: %+v", c)
	}
	return c, nil
}

func (s *Service) readDecisionValidHours(ctx context.Context, programmeID string) (int, error) {
	cv, err := s.cfg.ActiveAt(ctx, "scoring.policy", "programme:"+programmeID, time.Now().UTC())
	if err != nil {
		return 0, err
	}
	var p struct {
		DecisionValidHours int `json:"decision_valid_hours"`
	}
	if err := json.Unmarshal(cv.Content, &p); err != nil {
		return 0, fmt.Errorf("parse scoring.policy: %w", err)
	}
	if p.DecisionValidHours <= 0 {
		return 0, fmt.Errorf("decision_valid_hours must be > 0, got %d", p.DecisionValidHours)
	}
	return p.DecisionValidHours, nil
}
