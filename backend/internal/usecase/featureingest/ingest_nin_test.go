package featureingest

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
)

func ninOf(t *testing.T, db *testutil.DB, token string) *bool {
	t.Helper()
	ctx := platform.WithTenant(context.Background(), "SIM_NG")
	var nin *bool
	if err := repo.WithTenantTx(ctx, db.App, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT nin_verified FROM subscriber_accounts WHERE msisdn_token=$1 AND effective_to IS NULL`, token).Scan(&nin)
	}); err != nil {
		t.Fatal(err)
	}
	return nin
}

// Build 2: MTN's nin_verified flag rides the feature feed and is upserted IN PLACE
// per subscriber — true, then false (updated, no history), then untouched when a
// later cut omits it. The raw NIN is never sent or stored (only the boolean).
func TestIngest_NINVerified_UpsertedInPlace(t *testing.T) {
	svc, db, _ := setup(t, "ftr_nin")
	ctx := context.Background()
	// A row that passes validateRow: exactly 13 weekly values + activity_days_30d in
	// [0,30] + active_days_90d in [0,90] — otherwise it quarantines and the
	// subscriber (and its nin flag) is never written.
	const wk13 = `[10000,10000,10000,10000,10000,10000,10000,10000,10000,10000,10000,10000,10000]`
	ingest := func(source, ninField string) {
		raw := []byte(`{"telco_id":"SIM_NG","as_of":"2026-07-20T00:00:00Z","rows":[` +
			`{"msisdn_token":"tok_nin_1","tenure_days":400,"activity_days_30d":15,"active_days_90d":90,"weekly_recharge_minor":` + wk13 + `,"currency":"NGN","quality_flags":[]` + ninField + `}]}`)
		if _, err := svc.IngestRaw(ctx, "SIM_NG", source, raw); err != nil {
			t.Fatal(err)
		}
	}

	ingest("test:nin-true", `,"nin_verified":true`)
	if v := ninOf(t, db, "tok_nin_1"); v == nil || !*v {
		t.Fatalf("nin_verified must be true after a true cut, got %v", v)
	}

	ingest("test:nin-false", `,"nin_verified":false`)
	if v := ninOf(t, db, "tok_nin_1"); v == nil || *v {
		t.Fatalf("nin_verified must be updated in place to false, got %v", v)
	}

	ingest("test:nin-absent", ``)
	if v := ninOf(t, db, "tok_nin_1"); v == nil || *v {
		t.Fatalf("a cut that omits nin_verified must leave the stored flag untouched (false), got %v", v)
	}

	// Raw NIN is never persisted: the ONLY nin-related column is the boolean flag.
	var cols int
	if err := db.Admin.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_name='subscriber_accounts' AND column_name LIKE '%nin%'`).Scan(&cols); err != nil {
		t.Fatal(err)
	}
	if cols != 1 {
		t.Fatalf("subscriber_accounts must have exactly one nin-related column (nin_verified boolean), got %d", cols)
	}
	var dtype string
	if err := db.Admin.QueryRow(ctx, `
		SELECT data_type FROM information_schema.columns
		WHERE table_name='subscriber_accounts' AND column_name='nin_verified'`).Scan(&dtype); err != nil {
		t.Fatal(err)
	}
	if dtype != "boolean" {
		t.Fatalf("nin_verified must be boolean (only the flag, never the raw NIN), got %q", dtype)
	}
}
