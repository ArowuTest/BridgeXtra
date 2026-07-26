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

// ingestNIN lands a one-row valid feature file (13 weeks + activity_days_30d in
// range, so it is not quarantined) for a token, at a given as_of, optionally
// carrying a nin_verified field (pass "" to omit it).
func ingestNIN(t *testing.T, svc *Service, source, asOf, token, ninField string) {
	t.Helper()
	const wk13 = `[10000,10000,10000,10000,10000,10000,10000,10000,10000,10000,10000,10000,10000]`
	raw := []byte(`{"telco_id":"SIM_NG","as_of":"` + asOf + `","rows":[` +
		`{"msisdn_token":"` + token + `","tenure_days":400,"activity_days_30d":15,"active_days_90d":90,"weekly_recharge_minor":` + wk13 + `,"currency":"NGN","quality_flags":[]` + ninField + `}]}`)
	if _, err := svc.IngestRaw(context.Background(), "SIM_NG", source, raw); err != nil {
		t.Fatal(err)
	}
}

// Build 2: MTN's nin_verified flag rides the feature feed and is upserted IN PLACE
// per subscriber — true, then false (updated, no history), then untouched when a
// later cut omits it. The raw NIN is never sent or stored (only the boolean).
func TestIngest_NINVerified_UpsertedInPlace(t *testing.T) {
	svc, db, _ := setup(t, "ftr_nin")
	const day = "2026-07-20T00:00:00Z"

	ingestNIN(t, svc, "test:nin-true", day, "tok_nin_1", `,"nin_verified":true`)
	if v := ninOf(t, db, "tok_nin_1"); v == nil || !*v {
		t.Fatalf("nin_verified must be true after a true cut, got %v", v)
	}

	ingestNIN(t, svc, "test:nin-false", day, "tok_nin_1", `,"nin_verified":false`)
	if v := ninOf(t, db, "tok_nin_1"); v == nil || *v {
		t.Fatalf("nin_verified must be updated in place to false (same-day cut), got %v", v)
	}

	ingestNIN(t, svc, "test:nin-absent", day, "tok_nin_1", ``)
	if v := ninOf(t, db, "tok_nin_1"); v == nil || *v {
		t.Fatalf("a cut that omits nin_verified must leave the stored flag untouched (false), got %v", v)
	}

	// Raw NIN is never persisted: the ONLY nin-related columns are the boolean flag
	// and its as_of date basis — never a raw NIN.
	var cols int
	if err := db.Admin.QueryRow(context.Background(), `
		SELECT count(*) FROM information_schema.columns
		WHERE table_name='subscriber_accounts' AND column_name LIKE '%nin%'`).Scan(&cols); err != nil {
		t.Fatal(err)
	}
	if cols != 2 {
		t.Fatalf("subscriber_accounts must have exactly two nin-related columns (nin_verified boolean + nin_verified_as_of), got %d", cols)
	}
	var dtype string
	if err := db.Admin.QueryRow(context.Background(), `
		SELECT data_type FROM information_schema.columns
		WHERE table_name='subscriber_accounts' AND column_name='nin_verified'`).Scan(&dtype); err != nil {
		t.Fatal(err)
	}
	if dtype != "boolean" {
		t.Fatalf("nin_verified must be boolean (only the flag, never the raw NIN), got %q", dtype)
	}
}

// Build 2 follow-on — MONOTONICITY: only a same-or-newer feed cut may change the
// flag, so an out-of-order/reprocessed OLDER cut cannot silently un-revoke identity.
func TestIngest_NINVerified_Monotonic(t *testing.T) {
	svc, db, _ := setup(t, "ftr_nin_mono")
	const t1 = "2026-07-10T00:00:00Z" // older
	const t2 = "2026-07-20T00:00:00Z" // newer

	// Revocation preserved: a NEWER false, then an OLDER true, must stay false.
	ingestNIN(t, svc, "rev-false-t2", t2, "tok_rev", `,"nin_verified":false`)
	if v := ninOf(t, db, "tok_rev"); v == nil || *v {
		t.Fatalf("want false after the T2 revocation, got %v", v)
	}
	ingestNIN(t, svc, "rev-true-t1-old", t1, "tok_rev", `,"nin_verified":true`)
	if v := ninOf(t, db, "tok_rev"); v == nil || *v {
		t.Fatalf("an OLDER true cut must NOT un-revoke a newer revocation; want false, got %v", v)
	}

	// Newer revocation applies: true@T1 then false@T2 -> false.
	ingestNIN(t, svc, "ok-true-t1", t1, "tok_ok", `,"nin_verified":true`)
	if v := ninOf(t, db, "tok_ok"); v == nil || !*v {
		t.Fatalf("want true after the T1 verification, got %v", v)
	}
	ingestNIN(t, svc, "ok-false-t2", t2, "tok_ok", `,"nin_verified":false`)
	if v := ninOf(t, db, "tok_ok"); v == nil || *v {
		t.Fatalf("a NEWER false cut must revoke; want false, got %v", v)
	}
}
