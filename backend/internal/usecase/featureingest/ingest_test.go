package featureingest

// M2b pack: ingestion runs against the REAL simulator handler over HTTP
// (the canonical contract), a real database, real RLS roles.

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
	"github.com/ArowuTest/telco-credit-platform/simulator/sim"
)

func setup(t *testing.T, suffix string) (*Service, *testutil.DB, *httptest.Server) {
	t.Helper()
	db := testutil.MustSetup(t, suffix)
	simulator := httptest.NewServer(sim.New(slog.Default(), "m2b", time.Second).Handler())
	t.Cleanup(simulator.Close)

	// Point telco.adapter at the test simulator through the governed flow —
	// exactly how the deployed environment was re-pointed.
	svcCfg := configsvc.New(db.Worker)
	ctx := context.Background()
	content := fmt.Sprintf(`{"fulfilment_url":%q,"request_timeout_ms":3000,"retry_budget":0,"circuit_error_threshold_pct":50,"circuit_min_requests":20}`, simulator.URL)
	c, err := svcCfg.CreateDraft(ctx, "telco.adapter", "telco:SIM_NG", "alice", "test sim", []byte(content))
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
	return New(db.App, configsvc.New(db.App), slog.Default()), db, simulator
}

func TestIngest_EndToEnd_ThenDuplicateIsRecordedNoOp(t *testing.T) {
	svc, db, _ := setup(t, "ftr_ingest")
	ctx := context.Background()

	first, err := svc.Run(ctx, "SIM_NG")
	if err != nil {
		t.Fatal(err)
	}
	if first.Duplicate || first.Rows != 100 || first.Written != 100 || first.Quarantined != 0 {
		t.Fatalf("first ingest: %+v", first)
	}

	// Same day's file again: identical bytes -> file-level dedup, zero writes.
	second, err := svc.Run(ctx, "SIM_NG")
	if err != nil {
		t.Fatal(err)
	}
	if !second.Duplicate {
		t.Fatalf("second ingest must be a recorded no-op, got %+v", second)
	}
	if second.FeatureFileID != first.FeatureFileID {
		t.Fatalf("duplicate must resolve to the ORIGINAL file id (%s != %s)",
			second.FeatureFileID, first.FeatureFileID)
	}

	// Feature-store state: 100 snapshots, one file row, subscribers created.
	tctx := platform.WithTenant(ctx, "SIM_NG")
	var snapshots, files, subs int
	err = repo.WithTenantTx(tctx, db.App, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM feature_snapshots").Scan(&snapshots); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM feature_files").Scan(&files); err != nil {
			return err
		}
		return tx.QueryRow(ctx,
			`SELECT count(*) FROM subscriber_accounts WHERE msisdn_token ~ '^tok_sim_[0-9]{4}$'`).Scan(&subs)
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshots != 100 || files != 1 {
		t.Fatalf("want 100 snapshots in 1 file, got %d in %d", snapshots, files)
	}
	if subs < 100 {
		t.Fatalf("feature file must introduce subscriber accounts, got %d", subs)
	}

	// Stored features are canonical integers with quality carried through
	// (SCR-002): the thin-file profile rows carry SHORT_HISTORY.
	var quality []byte
	err = repo.WithTenantTx(tctx, db.App, func(tx pgx.Tx) error {
		sub, err := (repo.Subscribers{}).GetLiveByToken(ctx, tx, "tok_sim_0007")
		if err != nil {
			return err
		}
		snap, err := (repo.FeatureSnapshots{}).LatestForSubscriber(ctx, tx, sub.SubscriberAccountID)
		if err != nil {
			return err
		}
		quality = snap.Quality
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var q struct {
		Flags []string `json:"flags"`
	}
	if err := json.Unmarshal(quality, &q); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range q.Flags {
		if f == "SHORT_HISTORY" {
			found = true
		}
	}
	if !found {
		t.Fatalf("row 7 (thin-file profile) must carry SHORT_HISTORY, got %v", q.Flags)
	}
}

func TestIngest_MalformedRow_QuarantinedNeverSilent(t *testing.T) {
	svc, db, simSrv := setup(t, "ftr_quarantine")
	ctx := context.Background()

	// Fetch the file WITH the injected contract-violating row, ingest raw.
	raw := testutil.HTTPGet(t, simSrv.URL+"/v1/telcos/SIM_NG/feature-file?count=10&malformed=1")
	sum, err := svc.IngestRaw(ctx, "SIM_NG", "test:malformed", raw)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Rows != 11 || sum.Written != 10 || sum.Quarantined != 1 {
		t.Fatalf("10 good + 1 quarantined expected, got %+v", sum)
	}

	// Control totals are ON the file record (a partial ingest is visible).
	tctx := platform.WithTenant(ctx, "SIM_NG")
	var quarantined int
	var status string
	if err := repo.WithTenantTx(tctx, db.App, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT quarantined_rows, status FROM feature_files
			WHERE feature_file_id = $1`, sum.FeatureFileID).Scan(&quarantined, &status)
	}); err != nil {
		t.Fatal(err)
	}
	if quarantined != 1 || status != "INGESTED" {
		t.Fatalf("file record must carry quarantine totals: q=%d status=%s", quarantined, status)
	}
}

func TestIngest_UndatedFileRefused(t *testing.T) {
	svc, _, _ := setup(t, "ftr_undated")
	_, err := svc.IngestRaw(context.Background(), "SIM_NG", "test:undated",
		[]byte(`{"telco_id":"SIM_NG","rows":[]}`))
	if err == nil {
		t.Fatal("an undated data cut must be refused (V2-SCR-002)")
	}
}
