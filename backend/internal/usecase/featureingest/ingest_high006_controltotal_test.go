package featureingest

// BX-HIGH-006 (feed integrity, feasible half): the feature feed carries a control total. A
// declared row_count that does not match the actual rows is a truncated/corrupted/partial
// cut and is refused. For a REAL (non-synthetic) telco the control total is MANDATORY. The
// full partner-SIGNED manifest (cryptographic provenance) remains gated on the MTN contract
// (BX-HIGH-015). Auth + tenant-binding were the earlier half of HIGH-006.

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestBXHIGH006_ControlTotalMismatchRefused(t *testing.T) {
	svc, _, _ := setup(t, "feat_h006_ctmismatch")
	ctx := context.Background()
	asOf := time.Now().UTC().Format(time.RFC3339)

	// row_count declares 5 but only 1 row is present — a truncated/corrupted cut. Refused
	// BEFORE any materialization. Mutation proof: drop the control-total check and this feed
	// ingests a partial cut silently.
	raw := []byte(`{"telco_id":"SIM_NG","as_of":"` + asOf + `","row_count":5,"rows":[{"msisdn_token":"tok_ct"}]}`)
	if _, err := svc.IngestRaw(ctx, "SIM_NG", "h006-ct-mismatch", raw); err == nil || !strings.Contains(err.Error(), "control total") {
		t.Fatalf("a feed whose declared row_count != actual rows must be refused (BX-HIGH-006), got: %v", err)
	}
}

func TestBXHIGH006_RealTelcoMustDeclareControlTotal(t *testing.T) {
	svc, db, _ := setup(t, "feat_h006_realct")
	ctx := context.Background()
	asOf := time.Now().UTC().Format(time.RFC3339)

	// A REAL (non-synthetic) telco, ACTIVE. is_synthetic defaults false.
	if _, err := db.Admin.Exec(ctx,
		`INSERT INTO telcos (telco_id, name, country, status) VALUES ('REAL_NG','Real Telco','NG','ACTIVE')`); err != nil {
		t.Fatal(err)
	}

	// No row_count -> an unverifiable cut from a real telco -> refused. Mutation proof: make
	// the mandatory branch synthetic-blind and this feed is accepted.
	raw := []byte(`{"telco_id":"REAL_NG","as_of":"` + asOf + `","rows":[]}`)
	if _, err := svc.IngestRaw(ctx, "REAL_NG", "h006-real-noct", raw); err == nil || !strings.Contains(err.Error(), "control total") {
		t.Fatalf("a real telco's feed without a control total must be refused (BX-HIGH-006), got: %v", err)
	}

	// With a matching control total (row_count 0 == 0 rows) it passes the integrity gate.
	ok := []byte(`{"telco_id":"REAL_NG","as_of":"` + asOf + `","row_count":0,"rows":[]}`)
	if _, err := svc.IngestRaw(ctx, "REAL_NG", "h006-real-ct-ok", ok); err != nil && strings.Contains(err.Error(), "control total") {
		t.Fatalf("a real telco's feed WITH a matching control total must pass the integrity gate, got: %v", err)
	}
}
