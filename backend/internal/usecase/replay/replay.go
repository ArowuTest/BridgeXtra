// Package replay is the BC-4 verifier (V1-CRD-010 / V2-SCR-011): every
// stored decision is RECOMPUTED from its pinned, immutable inputs — the
// exact feature snapshot, the exact policy version, and the input echoes in
// the canonical document — and the recomputed bytes must equal the stored
// document bit-for-bit (and hash-for-hash).
//
// Nothing here reads "current" state. A decision whose replay diverges is
// evidence of tampering, store corruption, or an unbumped engine change —
// all reportable, none repairable by this package (it only ever reads).
package replay

import (
	"bytes"
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

// Mismatch describes one decision whose replay diverged.
type Mismatch struct {
	DecisionSnapshotID string
	Reason             string
}

// Result reports one verification pass.
type Result struct {
	RunID      string
	Checked    int
	Matched    int
	Mismatches []Mismatch
}

// VerifyRun replays every decision a scoring run produced.
func (s *Service) VerifyRun(ctx context.Context, telcoID, runID string) (Result, error) {
	res := Result{RunID: runID}
	tctx := platform.WithTenant(ctx, telcoID)

	// Policies are cached per version id — a run pins exactly one, but the
	// verifier stays correct if runs ever mix.
	policies := map[string]scoring.Policy{}

	after := ""
	for {
		var page []entity.DecisionSnapshot
		err := repo.WithTenantTx(tctx, s.Pool, func(tx pgx.Tx) error {
			var err error
			page, err = (repo.Decisions{}).ListScoredByRun(ctx, tx, runID, after, pageSize)
			if err != nil {
				return err
			}
			for _, d := range page {
				res.Checked++
				if m := s.verifyOne(ctx, tx, d, policies); m != nil {
					res.Mismatches = append(res.Mismatches, *m)
				} else {
					res.Matched++
				}
			}
			return nil
		})
		if err != nil {
			return res, err
		}
		if len(page) < pageSize {
			break
		}
		after = page[len(page)-1].DecisionSnapshotID
	}
	if res.Checked == 0 {
		return res, fmt.Errorf("scoring run %q has no decisions to verify", runID)
	}
	if len(res.Mismatches) == 0 {
		s.Log.Info("replay verified — every decision reproduces bit-exactly",
			"run", runID, "checked", res.Checked)
	} else {
		s.Log.Error("REPLAY DIVERGENCE — decisions do not reproduce from pinned inputs",
			"run", runID, "checked", res.Checked, "mismatches", len(res.Mismatches))
	}
	return res, nil
}

// verifyOne recomputes one decision. A nil return means bit-exact.
func (s *Service) verifyOne(ctx context.Context, tx pgx.Tx, d entity.DecisionSnapshot,
	policies map[string]scoring.Policy) *Mismatch {

	bad := func(format string, args ...any) *Mismatch {
		return &Mismatch{DecisionSnapshotID: d.DecisionSnapshotID, Reason: fmt.Sprintf(format, args...)}
	}
	if len(d.DecisionDoc) == 0 || d.ScoredAt == nil {
		return bad("missing replay pins (doc/scored_at)")
	}

	// The stored doc supplies the input echoes.
	var doc scoring.Decision
	if err := json.Unmarshal(d.DecisionDoc, &doc); err != nil {
		return bad("stored doc does not parse: %v", err)
	}
	if doc.EngineVersion != scoring.EngineVersion {
		return bad("engine version drift: stored %s, running %s — replay requires the matching engine",
			doc.EngineVersion, scoring.EngineVersion)
	}

	// Pinned feature snapshot (immutable store).
	snap, err := (repo.FeatureSnapshots{}).Get(ctx, tx, d.FeatureSnapshotID)
	if err != nil {
		return bad("pinned feature snapshot unreadable: %v", err)
	}
	var feats scoring.Features
	if err := json.Unmarshal(snap.Features, &feats); err != nil {
		return bad("pinned features do not parse: %v", err)
	}
	var qual scoring.Quality
	if err := json.Unmarshal(snap.Quality, &qual); err != nil {
		return bad("pinned quality does not parse: %v", err)
	}

	// Pinned policy version (immutable after approval).
	policy, ok := policies[d.ConfigVersionID]
	if !ok {
		cv, err := s.Config.GetVersion(ctx, d.ConfigVersionID)
		if err != nil {
			return bad("pinned policy version unreadable: %v", err)
		}
		if err := json.Unmarshal(cv.Content, &policy); err != nil {
			return bad("pinned policy does not parse: %v", err)
		}
		policies[d.ConfigVersionID] = policy
	}

	recomputed, err := scoring.Input{
		Features: feats, Quality: qual,
		FeatureContentHash: snap.ContentHash,
		Policy:             policy, PolicyVersionID: d.ConfigVersionID,
		SubscriberStatus: doc.SubscriberStatus, // echoed input, not current state
		PriorTierCode:    doc.PriorTierCode,
		FeatureAsOf:      snap.AsOf, ScoredAt: d.ScoredAt.UTC(),
	}.Score()
	if err != nil {
		return bad("engine rejected pinned inputs: %v", err)
	}
	got, err := recomputed.CanonicalJSON()
	if err != nil {
		return bad("canonicalise: %v", err)
	}
	// Two independent equality checks:
	// 1. The recomputed hash must equal the hash stored AT SCORING TIME —
	//    this pins the original engine bytes.
	sum := sha256.Sum256(got)
	if hex.EncodeToString(sum[:]) != d.DecisionHash {
		return bad("decision hash diverges — recomputation does not reproduce the original")
	}
	// 2. The stored doc, re-canonicalised through the same struct, must
	//    equal the recomputed bytes — this catches edits to the stored doc
	//    itself (JSONB does not preserve key order, so the comparison goes
	//    through the canonical form, never raw stored bytes).
	storedCanon, err := doc.CanonicalJSON()
	if err != nil {
		return bad("stored doc canonicalise: %v", err)
	}
	if !bytes.Equal(got, storedCanon) {
		return bad("stored document diverges from recomputation")
	}
	// Cross-checks: stored columns must agree with the doc they summarise.
	if doc.TierCode != d.TierCode || doc.PriorTierCode != d.PriorTierCode {
		return bad("stored columns disagree with canonical doc")
	}
	if doc.ScoredAt != d.ScoredAt.UTC().Format(time.RFC3339) {
		return bad("scored_at column disagrees with canonical doc")
	}
	return nil
}
