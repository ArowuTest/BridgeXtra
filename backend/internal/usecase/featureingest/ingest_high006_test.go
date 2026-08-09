package featureingest

// BX-HIGH-006: the feature feed must be tenant-bound. The file's self-declared
// telco_id was parsed but never compared to the authenticated telco, so a feed (or a
// misrouted / compromised endpoint) claiming another telco's id would land under the
// wrong tenant. (The authenticated FETCH — partner auth applied via the mno adapter,
// exercised end-to-end by TestIngest_EndToEnd with auth=none — is the companion half.)

import (
	"context"
	"testing"
)

// Mutation proof: remove the file.TelcoID == telcoID check in IngestRaw and this
// cross-tenant feed lands snapshots under the wrong tenant.
func TestBXHIGH006_TenantMismatch_Refused(t *testing.T) {
	svc, db, _ := setup(t, "ftr_h006")
	const wk13 = `[10000,10000,10000,10000,10000,10000,10000,10000,10000,10000,10000,10000,10000]`
	// The file CLAIMS OTHER_NG but we ingest it for SIM_NG (the authenticated telco).
	raw := []byte(`{"telco_id":"OTHER_NG","as_of":"2026-07-20T00:00:00Z","rows":[` +
		`{"msisdn_token":"tok_h006","tenure_days":400,"activity_days_30d":15,"active_days_90d":90,"weekly_recharge_minor":` + wk13 + `,"currency":"NGN","quality_flags":[]}]}`)
	if _, err := svc.IngestRaw(context.Background(), "SIM_NG", "h006-mismatch", raw); err == nil {
		t.Fatal("a feature file whose telco_id != the authenticated telco must be refused (BX-HIGH-006)")
	}
	var rows int
	if err := db.Admin.QueryRow(context.Background(), `SELECT count(*) FROM feature_snapshots`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("a refused cross-tenant feed must write no snapshots, got %d", rows)
	}
}
