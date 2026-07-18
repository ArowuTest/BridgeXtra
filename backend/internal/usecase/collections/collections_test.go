package collections_test

// M3c pack: config-driven delinquency classification (set-based), the
// maker-checker write-off journey (request gate -> self-approval refused ->
// distinct approval crystallises loss + balanced journal + pool release),
// and EDG-021 through the REAL path: write-off first, then a recovery that
// books as income.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/ledger"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/mno"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/collections"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/origination"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/recovery"
	"github.com/ArowuTest/telco-credit-platform/simulator/sim"
)

type fixture struct {
	db   *testutil.DB
	orig *origination.Service
	rec  *recovery.Service
	col  *collections.Service
}

func tenantCtx() context.Context { return platform.WithTenant(context.Background(), "SIM_NG") }

func newFixture(t *testing.T, suffix string) *fixture {
	t.Helper()
	db := testutil.MustSetup(t, suffix)
	simulator := sim.New(slog.Default(), "col-test", 0)
	srv := httptest.NewServer(simulator.Handler())
	t.Cleanup(srv.Close)

	cfgW := configsvc.New(db.Worker)
	ctx := context.Background()
	content := fmt.Sprintf(`{"fulfilment_url":%q,"request_timeout_ms":2000,"retry_budget":0,"circuit_error_threshold_pct":50,"circuit_min_requests":20,"max_weekly_recharge_minor":100000000}`, srv.URL)
	c, err := cfgW.CreateDraft(ctx, "telco.adapter", "telco:SIM_NG", "alice", "test sim", []byte(content))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfgW.Submit(ctx, c.ConfigVersionID, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := cfgW.Approve(ctx, c.ConfigVersionID, "bob"); err != nil {
		t.Fatal(err)
	}
	if err := cfgW.Activate(ctx, c.ConfigVersionID, "bob", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	appCfg := configsvc.New(db.App)
	led := ledger.New(appCfg)
	return &fixture{
		db:   db,
		orig: origination.New(db.App, appCfg, led, mno.NewHTTPAdapter(appCfg), slog.Default()),
		rec:  recovery.New(db.App, appCfg, led, slog.Default()),
		col:  collections.New(db.App, appCfg, led, slog.Default()),
	}
}

func (f *fixture) activeAdvance(t *testing.T) entity.Advance {
	t.Helper()
	offers, err := f.orig.GetOffers(tenantCtx(), "prg_sim_airtime01", "tok_sim_0001")
	if err != nil {
		t.Fatal(err)
	}
	res, err := f.orig.Confirm(tenantCtx(), origination.ConfirmCmd{
		ProgrammeID: "prg_sim_airtime01", OfferID: offers[0].OfferID, MSISDNToken: "tok_sim_0001",
		IdemKey: "col-adv-1", CorrelationID: "cor-col-adv",
	})
	if err != nil {
		t.Fatal(err)
	}
	return res.Advance
}

// backdate makes the advance N days old (classification input).
func (f *fixture) backdate(t *testing.T, advanceID string, days int) {
	t.Helper()
	if _, err := f.db.Admin.Exec(context.Background(),
		fmt.Sprintf(`UPDATE advances SET activated_at = now() - interval '%d days' WHERE advance_id = $1`, days),
		advanceID); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) bucketOf(t *testing.T, advanceID string) string {
	t.Helper()
	var b string
	if err := f.db.Admin.QueryRow(context.Background(),
		`SELECT COALESCE(delinquency_bucket,'') FROM advances WHERE advance_id = $1`, advanceID).Scan(&b); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestM3C_Classification_LadderFromConfig(t *testing.T) {
	f := newFixture(t, "col_classify")
	adv := f.activeAdvance(t)

	// Fresh advance: CURRENT.
	if _, err := f.col.Classify(tenantCtx(), "SIM_NG", "prg_sim_airtime01"); err != nil {
		t.Fatal(err)
	}
	if b := f.bucketOf(t, adv.AdvanceID); b != "CURRENT" {
		t.Fatalf("fresh advance must be CURRENT, got %q", b)
	}

	// Age it through the ladder: 10 days -> DPD_8_30; 95 days -> DPD_90_PLUS.
	f.backdate(t, adv.AdvanceID, 10)
	changed, err := f.col.Classify(tenantCtx(), "SIM_NG", "prg_sim_airtime01")
	if err != nil {
		t.Fatal(err)
	}
	if changed != 1 || f.bucketOf(t, adv.AdvanceID) != "DPD_8_30" {
		t.Fatalf("10-day advance must move to DPD_8_30 (changed=%d bucket=%s)", changed, f.bucketOf(t, adv.AdvanceID))
	}

	f.backdate(t, adv.AdvanceID, 95)
	if _, err := f.col.Classify(tenantCtx(), "SIM_NG", "prg_sim_airtime01"); err != nil {
		t.Fatal(err)
	}
	if b := f.bucketOf(t, adv.AdvanceID); b != "DPD_90_PLUS" {
		t.Fatalf("95-day advance must be DPD_90_PLUS, got %q", b)
	}

	// Re-run with no age change: zero rows touched (stable, idempotent).
	changed, err = f.col.Classify(tenantCtx(), "SIM_NG", "prg_sim_airtime01")
	if err != nil {
		t.Fatal(err)
	}
	if changed != 0 {
		t.Fatalf("stable re-classification must touch nothing, changed=%d", changed)
	}
}

func TestM3C_WriteOff_FullMakerCheckerJourney(t *testing.T) {
	f := newFixture(t, "col_writeoff")
	adv := f.activeAdvance(t)
	ctx := context.Background()

	// Below the policy minimum: request refused.
	f.backdate(t, adv.AdvanceID, 10)
	if _, err := f.col.Classify(tenantCtx(), "SIM_NG", "prg_sim_airtime01"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.col.RequestWriteOff(tenantCtx(), "SIM_NG", adv.AdvanceID, "alice", "uncollectable"); !errors.Is(err, collections.ErrNotEligible) {
		t.Fatalf("bucket below minimum must refuse (G3 gate): %v", err)
	}

	// Age past the minimum, request opens.
	f.backdate(t, adv.AdvanceID, 100)
	if _, err := f.col.Classify(tenantCtx(), "SIM_NG", "prg_sim_airtime01"); err != nil {
		t.Fatal(err)
	}
	wo, err := f.col.RequestWriteOff(tenantCtx(), "SIM_NG", adv.AdvanceID, "alice", "uncollectable 100d")
	if err != nil {
		t.Fatal(err)
	}
	if wo.Fee.Amount() != 500 || wo.Principal.Amount() != 4_500 {
		t.Fatalf("split must itemise the obligation: fee=%d principal=%d", wo.Fee.Amount(), wo.Principal.Amount())
	}
	// Duplicate request refused (schema-arbitered).
	if _, err := f.col.RequestWriteOff(tenantCtx(), "SIM_NG", adv.AdvanceID, "alice", "again"); !errors.Is(err, collections.ErrAlreadyExists) {
		t.Fatalf("second request must refuse: %v", err)
	}

	// Maker cannot approve their own request — the SCHEMA says no.
	if err := f.col.ApproveWriteOff(tenantCtx(), "SIM_NG", wo.WriteOffID, "alice", "cor-wo-1"); !errors.Is(err, collections.ErrSelfApproval) {
		t.Fatalf("self-approval must be refused by the schema: %v", err)
	}

	// Distinct approver: the loss crystallises atomically.
	if err := f.col.ApproveWriteOff(tenantCtx(), "SIM_NG", wo.WriteOffID, "bob", "cor-wo-1"); err != nil {
		t.Fatal(err)
	}

	var state string
	var outstanding, utilised int64
	if err := f.db.Admin.QueryRow(ctx, `
		SELECT a.state, a.outstanding_minor, p.utilised_minor
		FROM advances a JOIN funding_pools p ON p.pool_id = a.funding_pool_id
		WHERE a.advance_id = $1`, adv.AdvanceID).Scan(&state, &outstanding, &utilised); err != nil {
		t.Fatal(err)
	}
	if state != "WRITTEN_OFF" || outstanding != 0 || utilised != 0 {
		t.Fatalf("crystallised loss: state=%s outstanding=%d utilised=%d", state, outstanding, utilised)
	}
	var expense int64
	if err := f.db.Admin.QueryRow(ctx, `
		SELECT COALESCE(SUM(debit_minor),0) FROM journal_entries WHERE account_code='WRITE_OFF_EXPENSE'`).Scan(&expense); err != nil {
		t.Fatal(err)
	}
	if expense != 5_000 {
		t.Fatalf("loss must be recognised in the books: %d", expense)
	}
	var woState string
	if err := f.db.Admin.QueryRow(ctx,
		`SELECT state FROM write_offs WHERE write_off_id=$1`, wo.WriteOffID).Scan(&woState); err != nil {
		t.Fatal(err)
	}
	if woState != "POSTED" {
		t.Fatalf("evidence must be POSTED, got %s", woState)
	}

	// EDG-021 through the REAL path: a later recovery books as income.
	res, err := f.rec.Ingest(tenantCtx(), recovery.IngestCmd{
		SourceEventID: "src-after-wo", MSISDNToken: "tok_sim_0001",
		Amount: entity.MustMoney(2_000, entity.NGN), OccurredAt: time.Now().UTC(),
		CorrelationID: "cor-after-wo",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.State != entity.RecoveryAllocated || res.Applied.Amount() != 2_000 {
		t.Fatalf("post-write-off recovery must book as income: %+v", res)
	}
	var income int64
	if err := f.db.Admin.QueryRow(ctx, `
		SELECT COALESCE(SUM(credit_minor),0) FROM journal_entries WHERE account_code='WRITEOFF_RECOVERY_INCOME'`).Scan(&income); err != nil {
		t.Fatal(err)
	}
	if income != 2_000 {
		t.Fatalf("income must be recognised: %d", income)
	}

	// The whole journey leaves balanced books.
	var unbalanced int
	if err := f.db.Admin.QueryRow(ctx, `
		SELECT count(*) FROM (
			SELECT journal_id FROM journal_entries GROUP BY journal_id, currency
			HAVING SUM(debit_minor) <> SUM(credit_minor)) x`).Scan(&unbalanced); err != nil {
		t.Fatal(err)
	}
	if unbalanced != 0 {
		t.Fatal("INV-004 violated across the write-off journey")
	}
}
