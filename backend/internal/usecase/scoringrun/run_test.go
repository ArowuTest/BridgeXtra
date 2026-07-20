package scoringrun

// M2c(2) pack: batch scoring over a REAL ingested feature file — decisions
// become current with full replay pins; re-running the same inputs is a
// no-op replay; a second file moves tiers at most one step up.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/featureingest"
	"github.com/ArowuTest/telco-credit-platform/simulator/sim"
)

const (
	telcoID     = "SIM_NG"
	programmeID = "prg_sim_airtime01"
)

func setup(t *testing.T, suffix string) (*Service, *featureingest.Service, *testutil.DB) {
	t.Helper()
	db := testutil.MustSetup(t, suffix)
	simulator := httptest.NewServer(sim.New(slog.Default(), "m2c", time.Second).Handler())
	t.Cleanup(simulator.Close)

	svcCfg := configsvc.New(db.Worker)
	ctx := context.Background()
	content := fmt.Sprintf(`{"fulfilment_url":%q,"request_timeout_ms":3000,"retry_budget":0,"circuit_error_threshold_pct":50,"circuit_min_requests":20,"circuit_cooldown_seconds":30,"max_weekly_recharge_minor":100000000}`, simulator.URL)
	c, err := svcCfg.CreateDraft(ctx, "telco.adapter", "telco:"+telcoID, "alice", "test sim", []byte(content))
	if err != nil {
		t.Fatal(err)
	}
	if err := svcCfg.Submit(ctx, c.ConfigVersionID, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := svcCfg.Approve(ctx, c.ConfigVersionID, "bob"); err != nil {
		t.Fatal(err)
	}
	if err := svcCfg.Activate(ctx, c.ConfigVersionID, "bob", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	appCfg := configsvc.New(db.App)
	return New(db.App, appCfg, slog.Default()),
		featureingest.New(db.App, appCfg, slog.Default()), db
}

func TestScoringRun_EndToEnd_ThenIdempotentReplay(t *testing.T) {
	svc, ingest, db := setup(t, "score_run")
	ctx := context.Background()

	file, err := ingest.Run(ctx, telcoID)
	if err != nil {
		t.Fatal(err)
	}

	res, err := svc.Run(ctx, telcoID, programmeID, file.FeatureFileID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Resumed || res.Scored != 100 || res.Skipped != 0 {
		t.Fatalf("first run: %+v", res)
	}

	// Every scored decision is current, carries replay pins, and its stored
	// doc round-trips (spot-checked in bulk here; bit-exactness proven by
	// the replay pack in M2d).
	tctx := platform.WithTenant(ctx, telcoID)
	var current, withPins, ineligible int
	err = repo.WithTenantTx(tctx, db.App, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM decision_snapshots WHERE scoring_run_id = $1 AND is_current`,
			res.Run.ScoringRunID).Scan(&current); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM decision_snapshots
			WHERE scoring_run_id = $1 AND decision_doc IS NOT NULL
			  AND decision_hash IS NOT NULL AND scored_at IS NOT NULL`,
			res.Run.ScoringRunID).Scan(&withPins); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
			SELECT count(*) FROM decision_snapshots
			WHERE scoring_run_id = $1 AND (decision_doc->>'eligible') = 'false'`,
			res.Run.ScoringRunID).Scan(&ineligible)
	})
	if err != nil {
		t.Fatal(err)
	}
	if current != 100 || withPins != 100 {
		t.Fatalf("want 100 current decisions with pins, got current=%d pins=%d", current, withPins)
	}
	t.Logf("scored=100 (ineligible=%d)", ineligible)

	// Same file + same policy again: the run is a recorded replay, zero new
	// decisions (control run row is UNIQUE on the inputs).
	res2, err := svc.Run(ctx, telcoID, programmeID, file.FeatureFileID)
	if err != nil {
		t.Fatal(err)
	}
	if !res2.Resumed || res2.Run.ScoringRunID != res.Run.ScoringRunID {
		t.Fatalf("second run must resolve to the original run: %+v", res2)
	}
	var decisions int
	if err := repo.WithTenantTx(tctx, db.App, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM decision_snapshots WHERE scoring_run_id = $1`,
			res.Run.ScoringRunID).Scan(&decisions)
	}); err != nil {
		t.Fatal(err)
	}
	if decisions != 100 {
		t.Fatalf("replay must not add decisions: %d", decisions)
	}
}

func TestScoringRun_SecondCycle_OneTierUpEnforced(t *testing.T) {
	svc, ingest, db := setup(t, "score_cycle")
	ctx := context.Background()

	// Cycle 1: last week's cut.
	file1, err := ingestAsOf(t, ingest, "2026-07-10")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Run(ctx, telcoID, programmeID, file1); err != nil {
		t.Fatal(err)
	}

	// Cycle 2: a later cut of the same subscriber base.
	file2, err := ingestAsOf(t, ingest, "2026-07-17")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Run(ctx, telcoID, programmeID, file2); err != nil {
		t.Fatal(err)
	}

	// Movement audit: no subscriber's tier index rose by more than one
	// between their two decisions (V2-SCR-007), enforced ACROSS runs.
	tctx := platform.WithTenant(ctx, telcoID)
	tierIdx := map[string]int{"TIER_01": 0, "TIER_02": 1, "TIER_03": 2, "TIER_04": 3}
	violations := 0
	err = repo.WithTenantTx(tctx, db.App, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT prev.tier_code, cur.tier_code
			FROM decision_snapshots cur
			JOIN decision_snapshots prev
			  ON prev.subscriber_account_id = cur.subscriber_account_id
			 AND prev.tier_code <> 'SEED' AND NOT prev.is_current
			WHERE cur.is_current AND cur.tier_code <> 'SEED' AND cur.tier_code <> ''`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var prevTier, curTier string
			if err := rows.Scan(&prevTier, &curTier); err != nil {
				return err
			}
			p, okP := tierIdx[prevTier]
			c, okC := tierIdx[curTier]
			if okP && okC && c > p+1 {
				violations++
			}
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	if violations != 0 {
		t.Fatalf("V2-SCR-007: %d subscribers jumped more than one tier between cycles", violations)
	}
}

// ingestAsOf pulls a dated file through the ingest service's raw path so two
// cycles have distinct content hashes and as_of cuts.
func ingestAsOf(t *testing.T, ingest *featureingest.Service, asOf string) (string, error) {
	t.Helper()
	ctx := context.Background()
	cv, err := ingest.Config.ActiveAt(ctx, "telco.adapter", "telco:"+telcoID, time.Now().UTC())
	if err != nil {
		return "", err
	}
	var ac struct {
		FulfilmentURL string `json:"fulfilment_url"`
	}
	if err := json.Unmarshal(cv.Content, &ac); err != nil {
		return "", err
	}
	raw := testutil.HTTPGet(t, fmt.Sprintf("%s/v1/telcos/%s/feature-file?count=60&as_of=%s", ac.FulfilmentURL, telcoID, asOf))
	sum, err := ingest.IngestRaw(ctx, telcoID, "test:"+asOf, raw)
	if err != nil {
		return "", err
	}
	return sum.FeatureFileID, nil
}
