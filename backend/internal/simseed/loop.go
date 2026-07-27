//go:build simseed_loop

// Package simseed loop-mode (varied-user simulator seeder, design
// build/PHASE1_VARIED_USER_SEEDER_DESIGN.md). RunLoop constructs the REAL usecases
// in-process and drives a synthetic cohort through them —
// featureingest.IngestRaw -> scoringrun.Run -> origination.GetOffers/Confirm — against
// a telco-guarded SyntheticAdapter, so the whole lending machine is exercised end to
// end with NO direct INSERT into the money core and NOTHING armed.
//
// Slice 1: one good-payer to ACTIVE (no recovery yet). Prod-safety guards run first;
// origination is replay-aware; all writes to the money core go through the usecases.
package simseed

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
	"github.com/ArowuTest/telco-credit-platform/backend/internal/ledger"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/mno"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/featureingest"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/origination"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/scoringrun"
)

// LoopPlan is a deterministic run request. BusinessDay is the single temporal anchor
// (as_of); a re-run with the same (Seed, BusinessDay) reuses the booked advances.
type LoopPlan struct {
	Seed        string
	BusinessDay string // YYYY-MM-DD, Lagos
	Repeat      int    // members per profile (default 1)
	ProgrammeID string // e.g. "prg_sim_airtime01"
}

// MemberOutcome reports one synthetic subscriber's result.
type MemberOutcome struct {
	Token     string
	Profile   string
	AdvanceID string
	State     string
}

// LoopResult summarises the run.
type LoopResult struct {
	Subscribers int
	Advances    int
	Declined    int
	Members     []MemberOutcome
}

// freshWindow is a conservative sanity floor for the scoring accept window: a
// business-day older than this scores DEGRADED (tier collapses to the starter cap),
// which would make the loop originate at the wrong limit. Slice 2 reads the exact
// governed accept_hours from scoring.policy; here we fail loud on an obviously stale
// day rather than emit confusing wrong-tier offers.
const freshWindow = 40 * time.Hour

// RunLoop drives the synthetic cohort through the real usecases.
func RunLoop(ctx context.Context, appPool *pgxpool.Pool, plan LoopPlan) (LoopResult, error) {
	var res LoopResult

	// §5.1 prod-safety guards — the literal first statements, ungated by any env.
	if err := VerifySyntheticOnly(ctx, appPool); err != nil {
		return res, err
	}
	var bypass bool
	if err := appPool.QueryRow(ctx, `SELECT rolbypassrls FROM pg_roles WHERE rolname = current_user`).Scan(&bypass); err != nil {
		return res, fmt.Errorf("simseed loop: cannot verify current role (fail-closed): %w", err)
	}
	if bypass {
		return res, fmt.Errorf("simseed loop: current_user has BYPASSRLS — refusing (RLS %s binding not guaranteed)", SyntheticTelco)
	}
	if plan.BusinessDay == "" || plan.ProgrammeID == "" {
		return res, fmt.Errorf("simseed loop: BusinessDay and ProgrammeID are required")
	}
	repeat := plan.Repeat
	if repeat <= 0 {
		repeat = 1
	}
	loopSeed := plan.Seed + "|loop" // disjoint from any plain-seed SeedRecoveryDay cohort (design R4)

	lagos, err := time.LoadLocation("Africa/Lagos")
	if err != nil {
		return res, err
	}
	d, err := time.ParseInLocation("2006-01-02", plan.BusinessDay, lagos)
	if err != nil {
		return res, fmt.Errorf("simseed loop: BusinessDay must be YYYY-MM-DD: %w", err)
	}
	asOf := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, lagos).UTC()
	if time.Since(asOf) > freshWindow {
		return res, fmt.Errorf("simseed loop: business-day %s (as_of %s) is older than the fresh-scoring window — use a recent day so profiles score at their intended tier", plan.BusinessDay, asOf.Format(time.RFC3339))
	}

	count := len(profiles) * repeat
	log := slog.Default()

	// Cohort (Seeder-A, idempotent; RLS-bound to SIM_NG).
	if _, err := SeedCohort(ctx, appPool, CohortPlan{Seed: loopSeed, Count: count}); err != nil {
		return res, err
	}
	res.Subscribers = count

	// Wire the REAL usecases in-process (the walking_skeleton newStack template),
	// swapping the HTTP simulator for the telco-guarded SyntheticAdapter.
	appCfg := configsvc.New(appPool)
	led := ledger.New(appCfg)
	adapter := &SyntheticAdapter{Seed: loopSeed, ByToken: map[string]mno.Outcome{}, ByAdv: map[string]mno.Outcome{}}
	orig := origination.New(appPool, appCfg, led, adapter, log)
	fi := featureingest.New(appPool, appCfg, log)
	sr := scoringrun.New(appPool, appCfg, log)

	// One feature file for the whole cohort; map each borrower's telco outcome.
	file := fFile{TelcoID: SyntheticTelco, AsOf: asOf, Rows: make([]fRow, 0, count)}
	memProfile := make([]profile, count)
	memToken := make([]string, count)
	for i := 0; i < count; i++ {
		p := profiles[i%len(profiles)]
		token := msisdnToken(loopSeed, i)
		memProfile[i], memToken[i] = p, token
		file.Rows = append(file.Rows, p.toRow(token))
		if p.originates {
			adapter.ByToken[token] = p.telcoOutcome
		}
	}
	raw, err := json.Marshal(file)
	if err != nil {
		return res, err
	}

	// 1. Feature ingest (real usecase; wraps its own tenant tx).
	sum, err := fi.IngestRaw(ctx, SyntheticTelco, "simseed:loop-feature-file", raw)
	if err != nil {
		return res, fmt.Errorf("simseed loop: featureingest: %w", err)
	}
	if sum.Quarantined > 0 {
		return res, fmt.Errorf("simseed loop: %d feature row(s) quarantined — profile values are invalid", sum.Quarantined)
	}

	// 2. Score (real usecase) -> is_current decisions.
	if _, err := sr.Run(ctx, SyntheticTelco, plan.ProgrammeID, sum.FeatureFileID); err != nil {
		return res, fmt.Errorf("simseed loop: scoringrun: %w", err)
	}

	// 3. Originate per member, replay-aware, tenant-scoped.
	tctx := platform.WithTenant(ctx, SyntheticTelco)
	for i := 0; i < count; i++ {
		p, token := memProfile[i], memToken[i]
		mo := MemberOutcome{Token: token, Profile: p.name}
		if !p.originates {
			res.Declined++
			mo.State = "DECLINED"
			res.Members = append(res.Members, mo)
			continue
		}
		idem := stableID("idem", loopSeed, fmt.Sprintf("adv|%s|%06d", plan.BusinessDay, i))
		adv, found, err := getAdvanceByIdem(tctx, appPool, idem)
		if err != nil {
			return res, err
		}
		if !found {
			views, err := orig.GetOffers(tctx, plan.ProgrammeID, token)
			if err != nil {
				return res, fmt.Errorf("simseed loop: GetOffers %s: %w", token, err)
			}
			if len(views) == 0 {
				return res, fmt.Errorf("simseed loop: no offers for %s (profile %s) — expected an eligible ladder", token, p.name)
			}
			top := topRung(views)
			cr, err := orig.Confirm(tctx, origination.ConfirmCmd{
				ProgrammeID: plan.ProgrammeID, OfferID: top.Offer.OfferID, MSISDNToken: token,
				IdemKey: idem, CorrelationID: stableID("corr", loopSeed, idem),
				DisclosureRef: top.Disclosure.DisclosureSnapshotID, Channel: "USSD",
				SessionID: stableID("sess", loopSeed, idem), AcceptedAt: time.Now().UTC(),
			})
			if err != nil {
				return res, fmt.Errorf("simseed loop: Confirm %s: %w", token, err)
			}
			adv = cr.Advance
		}
		adapter.ByAdv[adv.AdvanceID] = p.telcoOutcome
		mo.AdvanceID, mo.State = adv.AdvanceID, string(adv.State)
		res.Advances++
		res.Members = append(res.Members, mo)
	}
	return res, nil
}

// getAdvanceByIdem reads a booked advance by its deterministic idem key (replay
// pre-check, design D1). Returns found=false on ErrNotFound — never re-selects from
// GetOffers on a replay.
func getAdvanceByIdem(ctx context.Context, pool *pgxpool.Pool, idem string) (entity.Advance, bool, error) {
	var adv entity.Advance
	err := repo.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		a, e := (repo.Advances{}).GetByIdemKey(ctx, tx, idem)
		if e != nil {
			return e
		}
		adv = a
		return nil
	})
	if errors.Is(err, repo.ErrNotFound) {
		return entity.Advance{}, false, nil
	}
	if err != nil {
		return entity.Advance{}, false, err
	}
	return adv, true, nil
}

// topRung picks the highest-face-value offer the ladder presents (already filtered
// to <= the decision's MaxFaceValue by origination). Deterministic tie-break on id.
func topRung(views []origination.OfferView) origination.OfferView {
	best := views[0]
	for _, v := range views[1:] {
		if a, b := v.Offer.FaceValue.Amount(), best.Offer.FaceValue.Amount(); a > b || (a == b && v.Offer.OfferID < best.Offer.OfferID) {
			best = v
		}
	}
	return best
}
