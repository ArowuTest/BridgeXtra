package settlement_test

// M3e pack: statements derive FROM THE LEDGER with exact share partition and
// the single rounding site; finalised statements reproduce bit-exactly
// (EDG-027); a tampered ledger makes reproduction SCREAM.

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
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/origination"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/recovery"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/settlement"
	"github.com/ArowuTest/telco-credit-platform/simulator/sim"
)

func tenantCtx() context.Context { return platform.WithTenant(context.Background(), "SIM_NG") }

func setup(t *testing.T, suffix string) (*settlement.Service, *origination.Service, *recovery.Service, *testutil.DB) {
	t.Helper()
	db := testutil.MustSetup(t, suffix)
	simulator := sim.New(slog.Default(), "set-test", 0)
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
	return settlement.New(db.App, appCfg, slog.Default()),
		origination.New(db.App, appCfg, led, mno.NewHTTPAdapter(appCfg), slog.Default()),
		recovery.New(db.App, appCfg, led, slog.Default()), db
}

func TestM3E_Settlement_LedgerDerived_Reproducible(t *testing.T) {
	set, orig, rec, db := setup(t, "settle")
	ctx := context.Background()
	periodStart := time.Now().UTC().Add(-1 * time.Hour)

	// The money story: ₦50 advance (fee 500, disbursed 4500) fully recovered.
	offers, err := orig.GetOffers(tenantCtx(), "prg_sim_airtime01", "tok_sim_0001")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := orig.Confirm(tenantCtx(), origination.ConfirmCmd{
		ProgrammeID: "prg_sim_airtime01", OfferID: offers[0].OfferID, MSISDNToken: "tok_sim_0001",
		IdemKey: "set-adv-1", CorrelationID: "cor-set-1",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := rec.Ingest(tenantCtx(), recovery.IngestCmd{
		SourceEventID: "set-src-1", MSISDNToken: "tok_sim_0001",
		Amount: entity.MustMoney(5_000, entity.NGN), OccurredAt: time.Now().UTC(),
		CorrelationID: "cor-set-2",
	}); err != nil {
		t.Fatal(err)
	}

	// Period closes NOW — the "later activity" below posts after this
	// boundary and must not disturb the statement.
	periodEnd := time.Now().UTC()
	st, err := set.Generate(ctx, "SIM_NG", "prg_sim_airtime01", periodStart, periodEnd)
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]int64{
		"PRINCIPAL_DISBURSED":            4_500,
		"FEE_INCOME_TOTAL":               500,
		"RECOVERED_TOTAL":                5_000,
		"RECOVERY_REVERSED_TOTAL":        0,
		"WRITEOFF_EXPENSE_TOTAL":         0,
		"WRITEOFF_RECOVERY_INCOME_TOTAL": 0,
		"TELCO_SHARE":                    125, // 25% of 500 (PercentBps, exact)
		"PLATFORM_SHARE":                 375, // 500 - 125: exact partition
		"TAX_VAT":                        38,  // 7.5% of 500 = 37.5 -> HALF-UP 38
	}
	got := map[string]int64{}
	for _, l := range st.Lines {
		got[l.Code] = l.Amount.Amount()
	}
	for code, amt := range want {
		if got[code] != amt {
			t.Errorf("line %s = %d, want %d", code, got[code], amt)
		}
	}
	// Share partition is EXACT: telco + platform == fee income, always.
	if got["TELCO_SHARE"]+got["PLATFORM_SHARE"] != got["FEE_INCOME_TOTAL"] {
		t.Fatal("share partition lost money")
	}

	// Finalise, then reproduce bit-exactly (EDG-027).
	if err := set.Finalise(ctx, "SIM_NG", st.StatementID); err != nil {
		t.Fatal(err)
	}
	if err := set.VerifyReproducible(ctx, "SIM_NG", st.StatementID); err != nil {
		t.Fatalf("finalised statement must reproduce from the ledger: %v", err)
	}

	// Activity AFTER the period boundary must not disturb reproduction.
	offers2, err := orig.GetOffers(tenantCtx(), "prg_sim_airtime01", "tok_sim_0001")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := orig.Confirm(tenantCtx(), origination.ConfirmCmd{
		ProgrammeID: "prg_sim_airtime01", OfferID: offers2[0].OfferID, MSISDNToken: "tok_sim_0001",
		IdemKey: "set-adv-2", CorrelationID: "cor-set-3",
	}); err != nil {
		t.Fatal(err)
	}
	if err := set.VerifyReproducible(ctx, "SIM_NG", st.StatementID); err != nil {
		t.Fatalf("post-period activity must not disturb the statement: %v", err)
	}

	// A tampered LEDGER makes reproduction SCREAM: an admin edit to a
	// journal entry (no runtime role can) breaks the hash equality.
	if _, err := db.Admin.Exec(ctx, `
		UPDATE journal_entries SET credit_minor = credit_minor + 1
		WHERE account_code = 'FEE_INCOME' AND credit_minor = 500`); err != nil {
		t.Fatal(err)
	}
	err = set.VerifyReproducible(ctx, "SIM_NG", st.StatementID)
	if !errors.Is(err, settlement.ErrNotReproducible) {
		t.Fatalf("tampered ledger must fail with ErrNotReproducible, got %v", err)
	}
}

func TestM3E_DuplicatePeriod_Refused(t *testing.T) {
	set, _, _, _ := setup(t, "settle_dup")
	ctx := context.Background()
	start := time.Now().UTC().Add(-2 * time.Hour)
	end := time.Now().UTC().Add(-1 * time.Hour)

	if _, err := set.Generate(ctx, "SIM_NG", "prg_sim_airtime01", start, end); err != nil {
		t.Fatal(err)
	}
	if _, err := set.Generate(ctx, "SIM_NG", "prg_sim_airtime01", start, end); err == nil {
		t.Fatal("duplicate period statement must be refused by the schema")
	}
}
