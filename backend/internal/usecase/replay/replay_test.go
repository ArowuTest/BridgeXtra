package replay

// BC-4 pack (V1-CRD-010): a full scoring run replays bit-exactly; mutable
// state changing AFTER scoring cannot break replay (inputs are echoed in the
// doc); a tampered stored document is flagged, never silently accepted.

import (
	"context"
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
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/scoringrun"
	"github.com/ArowuTest/telco-credit-platform/simulator/sim"
)

const telcoID = "SIM_NG"

func scoredRun(t *testing.T, suffix string) (*Service, *testutil.DB, string) {
	t.Helper()
	db := testutil.MustSetup(t, suffix)
	simulator := httptest.NewServer(sim.New(slog.Default(), "m2d", time.Second).Handler())
	t.Cleanup(simulator.Close)

	svcCfg := configsvc.New(db.Worker)
	ctx := context.Background()
	content := fmt.Sprintf(`{"fulfilment_url":%q,"request_timeout_ms":3000,"retry_budget":0,"circuit_error_threshold_pct":50,"circuit_min_requests":20,"max_weekly_recharge_minor":100000000}`, simulator.URL)
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
	file, err := featureingest.New(db.App, appCfg, slog.Default()).Run(ctx, telcoID)
	if err != nil {
		t.Fatal(err)
	}
	res, err := scoringrun.New(db.App, appCfg, slog.Default()).Run(ctx, telcoID, "prg_sim_airtime01", file.FeatureFileID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Scored != 100 {
		t.Fatalf("expected 100 scored, got %+v", res)
	}
	return New(db.App, appCfg, slog.Default()), db, res.Run.ScoringRunID
}

func TestBC4_FullRunReplaysBitExactly(t *testing.T) {
	svc, db, runID := scoredRun(t, "replay_exact")
	ctx := context.Background()

	// Mutable state changes AFTER scoring: bar a subscriber. Replay must
	// still reproduce the original decision (status is echoed in the doc,
	// never re-read from live state).
	tctx := platform.WithTenant(ctx, telcoID)
	if err := repo.WithTenantTx(tctx, db.App, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE subscriber_accounts SET status = 'BARRED'
			WHERE msisdn_token = 'tok_sim_0001'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	res, err := svc.VerifyRun(ctx, telcoID, runID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Checked != 100 || res.Matched != 100 || len(res.Mismatches) != 0 {
		t.Fatalf("BC-4: full run must replay bit-exactly: %+v", res.Mismatches)
	}
}

func TestBC4_TamperedDocumentIsFlagged(t *testing.T) {
	svc, db, runID := scoredRun(t, "replay_tamper")
	ctx := context.Background()

	// Tamper one stored document as admin (no runtime role can — grants).
	var victim string
	if err := db.Admin.QueryRow(ctx, `
		SELECT decision_snapshot_id FROM decision_snapshots
		WHERE scoring_run_id = $1 AND (decision_doc->>'eligible') = 'true'
		LIMIT 1`, runID).Scan(&victim); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Admin.Exec(ctx, `
		UPDATE decision_snapshots
		SET decision_doc = jsonb_set(decision_doc, '{maximum_face_value_minor}', '99999999')
		WHERE decision_snapshot_id = $1`, victim); err != nil {
		t.Fatal(err)
	}

	res, err := svc.VerifyRun(ctx, telcoID, runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Mismatches) != 1 || res.Mismatches[0].DecisionSnapshotID != victim {
		t.Fatalf("exactly the tampered decision must be flagged: %+v", res.Mismatches)
	}
	if res.Matched != 99 {
		t.Fatalf("untampered decisions must still match: %+v", res)
	}
}

func TestBC4_RuntimeRolesCannotAlterDecisions(t *testing.T) {
	_, db, runID := scoredRun(t, "replay_grants")
	ctx := context.Background()

	// The app role must NOT be able to rewrite a decision's history fields —
	// only the is_current flip is part of normal operation.
	tctx := platform.WithTenant(ctx, telcoID)
	err := repo.WithTenantTx(tctx, db.App, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE decision_snapshots SET decision_hash = 'forged'
			WHERE scoring_run_id = $1`, runID)
		return err
	})
	if err == nil {
		t.Fatal("tcp_app must not be able to rewrite decision_hash (append-only history)")
	}
}
