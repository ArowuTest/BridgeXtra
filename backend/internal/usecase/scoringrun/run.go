// Package scoringrun executes batch decisioning (M2c): every subscriber in a
// feature file is scored through the pure engine against ONE pinned policy
// version and ONE run clock, and the results become current decisions.
//
// Determinism contract (BC-4): the run stores everything the engine saw —
// feature snapshot id, policy version id, prior tier, scored_at — plus the
// canonical decision document and its hash. Replay is recomputation from
// those pins, never from "current" state.
//
// Batching: pages of subjects, one transaction per page. A crashed run
// resumes: the run row is UNIQUE per (file, policy, programme), and subjects
// whose current decision already points at this run are skipped by the
// per-subscriber (run, subscriber) dedup below.
package scoringrun

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/scoring"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
)

const pageSize = 500

type Service struct {
	Pool   *pgxpool.Pool // tcp_app
	Config *configsvc.Service
	Log    *slog.Logger
}

func New(pool *pgxpool.Pool, cfg *configsvc.Service, log *slog.Logger) *Service {
	return &Service{Pool: pool, Config: cfg, Log: log}
}

// Result reports one run's control totals.
type Result struct {
	Run     entity.ScoringRun
	Scored  int
	Skipped int
	Resumed bool
}

// Run scores every subject in the feature file for one programme.
func (s *Service) Run(ctx context.Context, telcoID, programmeID, featureFileID string) (Result, error) {
	// Pin the policy ONCE for the whole run (V1-CFG-007).
	cv, err := s.Config.ActiveAt(ctx, "scoring.policy", "programme:"+programmeID, time.Now().UTC())
	if err != nil {
		return Result{}, fmt.Errorf("scoring.policy config: %w", err)
	}
	var policy scoring.Policy
	if err := json.Unmarshal(cv.Content, &policy); err != nil {
		return Result{}, fmt.Errorf("scoring.policy parse: %w", err)
	}

	tctx := platform.WithTenant(ctx, telcoID)
	var res Result

	// Create-or-resume the run row.
	err = repo.WithTenantTx(tctx, s.Pool, func(tx pgx.Tx) error {
		run, err := (repo.ScoringRuns{}).Insert(ctx, tx, entity.ScoringRun{
			ScoringRunID: platform.NewID("run"), TelcoID: telcoID, ProgrammeID: programmeID,
			FeatureFileID: featureFileID, PolicyVersionID: cv.ConfigVersionID,
		})
		if err != nil && err != repo.ErrDuplicateRun {
			return err
		}
		res.Run = run
		res.Resumed = err == repo.ErrDuplicateRun
		return nil
	})
	if err != nil {
		return res, err
	}
	if res.Resumed && res.Run.Status != "RUNNING" {
		s.Log.Info("scoring run already completed for these inputs — replay, not re-score",
			"run", res.Run.ScoringRunID, "status", res.Run.Status)
		return res, nil
	}

	// The run clock: one timestamp for the whole run, stored on every
	// decision. Resume reuses the ORIGINAL clock so late pages hash under
	// the same inputs as early ones.
	scoredAt := res.Run.StartedAt.UTC()

	after := ""
	for {
		var page []repo.SnapshotSubject
		err := repo.WithTenantTx(tctx, s.Pool, func(tx pgx.Tx) error {
			var err error
			page, err = (repo.FeatureSnapshots{}).ListSubjectsByFile(ctx, tx, featureFileID, after, pageSize)
			if err != nil {
				return err
			}
			scored, skipped := 0, 0
			for _, subj := range page {
				outcome, err := s.scoreOne(ctx, tx, subj, policy, cv.ConfigVersionID, res.Run, scoredAt)
				if err != nil {
					return fmt.Errorf("subscriber %s: %w", subj.Snapshot.SubscriberAccountID, err)
				}
				if outcome {
					scored++
				} else {
					skipped++
				}
			}
			if len(page) > 0 {
				res.Scored += scored
				res.Skipped += skipped
				return (repo.ScoringRuns{}).Progress(ctx, tx, res.Run.ScoringRunID, scored, skipped)
			}
			return nil
		})
		if err != nil {
			return res, err
		}
		if len(page) < pageSize {
			break
		}
		after = page[len(page)-1].Snapshot.FeatureSnapshotID
	}

	err = repo.WithTenantTx(tctx, s.Pool, func(tx pgx.Tx) error {
		return (repo.ScoringRuns{}).Complete(ctx, tx, res.Run.ScoringRunID, "COMPLETED")
	})
	if err != nil {
		return res, err
	}
	s.Log.Info("scoring run completed", "run", res.Run.ScoringRunID,
		"scored", res.Scored, "skipped", res.Skipped, "policy", cv.ConfigVersionID)
	return res, nil
}

// scoreOne scores a single subject inside the page transaction. Returns
// true when a decision was written, false when skipped (already scored by
// this run — resume path — or contract-violating snapshot, logged loudly).
func (s *Service) scoreOne(ctx context.Context, tx pgx.Tx, subj repo.SnapshotSubject,
	policy scoring.Policy, policyVersionID string, run entity.ScoringRun, scoredAt time.Time) (bool, error) {

	// Resume dedup: if the subscriber's current decision already came from
	// this run, this page was committed before a crash — skip.
	cur, err := (repo.Decisions{}).GetCurrent(ctx, tx, subj.Snapshot.SubscriberAccountID)
	if err == nil && cur.ScoringRunID == run.ScoringRunID {
		return false, nil
	}

	var feats scoring.Features
	if err := json.Unmarshal(subj.Snapshot.Features, &feats); err != nil {
		s.Log.Error("stored features do not parse — skipping subject, investigate the store",
			"snapshot", subj.Snapshot.FeatureSnapshotID, "err", err)
		return false, nil
	}
	var qual scoring.Quality
	if err := json.Unmarshal(subj.Snapshot.Quality, &qual); err != nil {
		s.Log.Error("stored quality does not parse — skipping subject",
			"snapshot", subj.Snapshot.FeatureSnapshotID, "err", err)
		return false, nil
	}

	priorTier := subj.PriorTierCode
	if priorTier == "SEED" {
		priorTier = "" // seeds carry no scored tier; cold-start rules apply
	}
	in := scoring.Input{
		Features: feats, Quality: qual,
		FeatureContentHash: subj.Snapshot.ContentHash,
		Policy:             policy, PolicyVersionID: policyVersionID,
		SubscriberStatus: subj.SubscriberStatus,
		PriorTierCode:    priorTier,
		FeatureAsOf:      subj.Snapshot.AsOf, ScoredAt: scoredAt,
	}
	dec, err := in.Score()
	if err != nil {
		s.Log.Error("engine rejected inputs — skipping subject", "snapshot",
			subj.Snapshot.FeatureSnapshotID, "err", err)
		return false, nil
	}
	doc, err := dec.CanonicalJSON()
	if err != nil {
		return false, err
	}
	docHash := sha256.Sum256(doc)

	faceMinor := dec.MaxFaceMinor
	if !dec.Eligible {
		faceMinor = 0 // ineligible decisions carry face 0 (0009 CHECK ties this to the doc)
	}
	face, err := entity.NewMoney(faceMinor, entity.Currency(dec.Currency))
	if err != nil {
		return false, fmt.Errorf("decision money invalid: %w", err)
	}
	validUntil, err := time.Parse(time.RFC3339, dec.ValidUntil)
	if err != nil {
		return false, fmt.Errorf("engine emitted unparseable valid_until: %w", err)
	}
	reasons, err := json.Marshal(dec.ReasonCodes)
	if err != nil {
		return false, err
	}

	return true, (repo.Decisions{}).InsertScored(ctx, tx, entity.DecisionSnapshot{
		DecisionSnapshotID:  platform.NewID("dec"),
		TelcoID:             subj.Snapshot.TelcoID,
		SubscriberAccountID: subj.Snapshot.SubscriberAccountID,
		MaxFaceValue:        face,
		ConfigVersionID:     policyVersionID,
		TierCode:            dec.TierCode,
		ReasonCodes:         reasons,
		FeatureSnapshotID:   subj.Snapshot.FeatureSnapshotID,
		ScoringRunID:        run.ScoringRunID,
		ValidUntil:          &validUntil,
		DecisionHash:        hex.EncodeToString(docHash[:]),
		DecisionDoc:         doc,
		PriorTierCode:       priorTier,
		ScoredAt:            &scoredAt,
	})
}
