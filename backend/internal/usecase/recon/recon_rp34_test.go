package recon

// R-P0-3/4 adversarial pack. Reconciliation must compare CURRENCY before
// amount (NGN 1,000 != USD 1,000) and must never feed an out-of-range or
// malformed external telco amount into the numeric compare (overflow-safe:
// MinInt64/MaxInt64/negative all become data-integrity breaks, never a
// panic, never a false MATCHED). These are white-box tests in-package so they
// can drive the telco records through a stub endpoint.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform/egress"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
)

type reconFixture struct {
	db  *testutil.DB
	svc *Service
}

// newReconFixture stands up a recon service whose telco.adapter points at a
// stub serving the given telco transactions, and seeds the advances the
// platform side will read. The full FK chain is seeded via the admin pool.
func newReconFixture(t *testing.T, suffix string, txns []telcoTransaction) *reconFixture {
	t.Helper()
	db := testutil.MustSetup(t, suffix)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(txns)
	}))
	t.Cleanup(srv.Close)
	return newReconFixtureURL(t, db, srv.URL)
}

// newReconFixtureURL points telco.adapter at an arbitrary base URL (used by
// the SSRF test to aim recon at the metadata address).
func newReconFixtureURL(t *testing.T, db *testutil.DB, baseURL string) *reconFixture {
	t.Helper()
	ctx := context.Background()
	cfg := configsvc.New(db.Worker)
	content := fmt.Sprintf(`{"fulfilment_url":%q,"request_timeout_ms":2000,"retry_budget":0,"circuit_error_threshold_pct":50,"circuit_min_requests":20,"circuit_cooldown_seconds":30,"max_weekly_recharge_minor":100000000}`, baseURL)
	c, err := cfg.CreateDraft(ctx, "telco.adapter", "telco:SIM_NG", "alice", "recon stub", []byte(content))
	if err != nil {
		t.Fatal(err)
	}
	mustLifecycle(t, cfg, c.ConfigVersionID)

	appCfg := configsvc.New(db.App)
	return &reconFixture{db: db, svc: New(db.App, appCfg, slog.Default())}
}

func mustLifecycle(t *testing.T, cfg *configsvc.Service, id string) {
	t.Helper()
	ctx := context.Background()
	if err := cfg.Submit(ctx, id, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Approve(ctx, id, "bob"); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Activate(ctx, id, "bob", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
}

// seedConfirmedAdvance inserts a money-bearing advance with a CONFIRMED
// fulfilment attempt carrying the telco reference — the platform side of one
// reconciliation row.
func (f *reconFixture) seedConfirmedAdvance(t *testing.T, advID string, faceMinor int64, currency, telcoRef string) {
	t.Helper()
	ctx := context.Background()
	sub := "sub_" + advID
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO subscriber_accounts (subscriber_account_id, telco_id, msisdn_token, status)
		  VALUES ($1,'SIM_NG',$2,'ACTIVE')`, []any{sub, "tok_" + advID}},
		{`INSERT INTO decision_snapshots (decision_snapshot_id, telco_id, subscriber_account_id,
		    max_face_value_minor, currency, config_version_id)
		  VALUES ('dsn_'||$1,'SIM_NG',$1,50000,$2,'cfg_seed_scoring_policy_v3')`, []any{sub, currency}},
		{`INSERT INTO offers (offer_id, telco_id, programme_id, subscriber_account_id, decision_snapshot_id,
		    face_value_minor, fee_minor, disbursed_minor, repayment_minor, currency, fee_model,
		    product_config_version_id, state, expires_at)
		  VALUES ('ofr_'||$1,'SIM_NG','prg_sim_airtime01',$2,'dsn_'||$2,$3,0,$3,$3,$4,
		    'DEDUCTED_UPFRONT','cfg_seed_product_airtime_v1','ACCEPTED', now()+interval '1 day')`,
			[]any{advID, sub, faceMinor, currency}},
		{`INSERT INTO advances (advance_id, telco_id, programme_id, subscriber_account_id, offer_id,
		    funding_pool_id, idempotency_key, correlation_id, state, face_value_minor, fee_minor,
		    disbursed_minor, outstanding_minor, currency)
		  VALUES ($1,'SIM_NG','prg_sim_airtime01',$2,'ofr_'||$1,'pool_sim_01',$1,$1,'ACTIVE',
		    $3,0,$3,$3,$4)`, []any{advID, sub, faceMinor, currency}},
		{`INSERT INTO fulfilment_attempts (attempt_id, advance_id, attempt_no, telco_idempotency_key,
		    state, telco_reference, request_evidence)
		  VALUES ('att_'||$1,$1,1,'tik_'||$1,'CONFIRMED',$2,'{}'::jsonb)`, []any{advID, telcoRef}},
	} {
		if _, err := f.db.Admin.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatalf("seed advance: %v", err)
		}
	}
}

func (f *reconFixture) statusCount(t *testing.T, status string) int {
	t.Helper()
	var n int
	if err := f.db.Admin.QueryRow(context.Background(),
		`SELECT count(*) FROM recon_items WHERE status=$1`, status).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// R-P0-3: identical minor amount, DIFFERENT currency must be a break, never a
// MATCHED.
func TestRP03_CurrencyMismatch_NotMatched(t *testing.T) {
	f := newReconFixture(t, "rp03_ccy", []telcoTransaction{
		{PlatformRequestID: "adv_ccy", TelcoReference: "TR-1", FaceValueMinor: 5_000, Currency: "USD", Status: "SUCCESS"},
	})
	f.seedConfirmedAdvance(t, "adv_ccy", 5_000, "NGN", "TR-1")

	sum, err := f.svc.RunFulfilment(context.Background(), "SIM_NG", "prg_sim_airtime01")
	if err != nil {
		t.Fatal(err)
	}
	if sum.Matched != 0 || sum.CurrencyMismatch != 1 {
		t.Fatalf("NGN vs USD at the same amount must be a currency break, got %+v", sum)
	}
	if f.statusCount(t, "BREAK_CURRENCY_MISMATCH") != 1 {
		t.Fatal("a BREAK_CURRENCY_MISMATCH item must be recorded with both currencies")
	}
}

// R-P0-4: MinInt64/MaxInt64/negative telco amounts must be MALFORMED breaks —
// never a panic, never fed to abs64(p-t).
func TestRP04_OverflowAmounts_Malformed(t *testing.T) {
	f := newReconFixture(t, "rp04_ovf", []telcoTransaction{
		{PlatformRequestID: "adv_min", TelcoReference: "TR-min", FaceValueMinor: math.MinInt64, Currency: "NGN", Status: "SUCCESS"},
		{PlatformRequestID: "adv_max", TelcoReference: "TR-max", FaceValueMinor: math.MaxInt64, Currency: "NGN", Status: "SUCCESS"},
		{PlatformRequestID: "adv_neg", TelcoReference: "TR-neg", FaceValueMinor: -1, Currency: "NGN", Status: "SUCCESS"},
	})
	f.seedConfirmedAdvance(t, "adv_min", 5_000, "NGN", "TR-min")
	f.seedConfirmedAdvance(t, "adv_max", 5_000, "NGN", "TR-max")
	f.seedConfirmedAdvance(t, "adv_neg", 5_000, "NGN", "TR-neg")

	sum, err := f.svc.RunFulfilment(context.Background(), "SIM_NG", "prg_sim_airtime01")
	if err != nil {
		t.Fatal(err)
	}
	if sum.Matched != 0 || sum.Malformed != 3 {
		t.Fatalf("extreme int64 amounts must all be malformed breaks, got %+v", sum)
	}
	if f.statusCount(t, "BREAK_MALFORMED_TELCO_RECORD") != 3 {
		t.Fatalf("three malformed breaks expected, got %d", f.statusCount(t, "BREAK_MALFORMED_TELCO_RECORD"))
	}
}

// A malformed CURRENCY (not ISO alpha-3) is also a malformed break.
func TestRP04_MalformedCurrency_Break(t *testing.T) {
	f := newReconFixture(t, "rp04_ccy", []telcoTransaction{
		{PlatformRequestID: "adv_bc", TelcoReference: "TR-bc", FaceValueMinor: 5_000, Currency: "ngn!", Status: "SUCCESS"},
	})
	f.seedConfirmedAdvance(t, "adv_bc", 5_000, "NGN", "TR-bc")
	sum, err := f.svc.RunFulfilment(context.Background(), "SIM_NG", "prg_sim_airtime01")
	if err != nil {
		t.Fatal(err)
	}
	if sum.Malformed != 1 || sum.Matched != 0 {
		t.Fatalf("non-ISO currency must be a malformed break, got %+v", sum)
	}
}

// The happy path still matches when currency and amount agree and are credible.
func TestRP34_SameCurrencyAmount_Matches(t *testing.T) {
	f := newReconFixture(t, "rp34_ok", []telcoTransaction{
		{PlatformRequestID: "adv_ok", TelcoReference: "TR-ok", FaceValueMinor: 5_000, Currency: "NGN", Status: "SUCCESS"},
	})
	f.seedConfirmedAdvance(t, "adv_ok", 5_000, "NGN", "TR-ok")
	sum, err := f.svc.RunFulfilment(context.Background(), "SIM_NG", "prg_sim_airtime01")
	if err != nil {
		t.Fatal(err)
	}
	if sum.Matched != 1 || sum.CurrencyMismatch != 0 || sum.Malformed != 0 || sum.AmountMismatch != 0 {
		t.Fatalf("credible same-currency same-amount must MATCH, got %+v", sum)
	}
}

// R-P0-5: the recon telco-records fetch is the FOURTH outbound door. Pointing
// it at the cloud metadata address must be refused by the shared egress guard.
func TestRP05_ReconSSRF_MetadataRefused(t *testing.T) {
	db := testutil.MustSetup(t, "rp05_ssrf")
	f := newReconFixtureURL(t, db, "http://169.254.169.254")
	_, err := f.svc.RunFulfilment(context.Background(), "SIM_NG", "prg_sim_airtime01")
	if err == nil {
		t.Fatal("recon must refuse to fetch from the metadata address (SSRF guard)")
	}
	if !errors.Is(err, egress.ErrBlocked) {
		t.Fatalf("recon SSRF must be blocked by the egress guard, got %v", err)
	}
}

// Fail-closed: recon.tolerance without max_amount_minor refuses to run.
func TestRP04_MissingCeiling_Refuses(t *testing.T) {
	f := newReconFixture(t, "rp04_floor", nil)
	// Supersede the seeded v2 with a version lacking max_amount_minor is
	// blocked by the validator, so instead delete the active recon config to
	// simulate an unconfigured floor.
	if _, err := f.db.Admin.Exec(context.Background(),
		`DELETE FROM config_versions WHERE domain='recon.tolerance'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.RunFulfilment(context.Background(), "SIM_NG", "prg_sim_airtime01"); err == nil {
		t.Fatal("recon must refuse to run without a governed amount ceiling")
	}
}
