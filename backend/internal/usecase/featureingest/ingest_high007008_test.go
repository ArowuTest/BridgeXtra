package featureingest

// BX-HIGH-007 (future-date poisoning) + BX-HIGH-008 (revocation fail-open). The
// nin_verified flag rides the feature feed and is guarded monotonically on its
// as_of. Two defects, fixed together:
//   007: a FUTURE-dated cut was accepted — a far-future "true" then durably blocked
//        every later real revocation (monotonic guard) and scored as FRESH.
//   008: a REVOCATION (false) in a row whose CREDIT features were quarantined was
//        silently dropped — a stale "true" kept the subscriber lending-eligible.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
)

// ingestQuarantinedNIN lands a one-row file whose CREDIT features are invalid
// (activity_days_30d out of range → the row is QUARANTINED) but which carries a
// nin_verified field.
func ingestQuarantinedNIN(t *testing.T, svc *Service, source, asOf, token, ninField string) Summary {
	t.Helper()
	const wk13 = `[10000,10000,10000,10000,10000,10000,10000,10000,10000,10000,10000,10000,10000]`
	raw := []byte(`{"telco_id":"SIM_NG","as_of":"` + asOf + `","rows":[` +
		`{"msisdn_token":"` + token + `","tenure_days":400,"activity_days_30d":999,"active_days_90d":90,"weekly_recharge_minor":` + wk13 + `,"currency":"NGN","quality_flags":[]` + ninField + `}]}`)
	sum, err := svc.IngestRaw(context.Background(), "SIM_NG", source, raw)
	if err != nil {
		t.Fatal(err)
	}
	return sum
}

// BX-HIGH-008: a REVOCATION carried by a credit-quarantined row must STILL land.
// Mutation proof: drop the quarantine-revocation path in IngestRaw and this stays true.
func TestBXHIGH008_QuarantinedRow_RevocationStillLands(t *testing.T) {
	svc, db, _ := setup(t, "ftr_h008_revoke")
	const t1 = "2026-07-10T00:00:00Z" // older, clean verification
	const t2 = "2026-07-20T00:00:00Z" // newer revocation (credit fields malformed)

	ingestNIN(t, svc, "h008-true", t1, "tok_h008", `,"nin_verified":true`)
	if v := ninOf(t, db, "tok_h008"); v == nil || !*v {
		t.Fatalf("subscriber must be verified after the clean true cut, got %v", v)
	}

	sum := ingestQuarantinedNIN(t, svc, "h008-revoke", t2, "tok_h008", `,"nin_verified":false`)
	if sum.Quarantined != 1 || sum.Written != 0 {
		t.Fatalf("the credit row must be quarantined (quarantined=1, written=0), got q=%d w=%d", sum.Quarantined, sum.Written)
	}
	if v := ninOf(t, db, "tok_h008"); v == nil || *v {
		t.Fatalf("a revocation from a credit-quarantined row must land false, got %v", v)
	}
}

// BX-HIGH-008 asymmetry: a VERIFICATION (true) from a quarantined row must NOT
// grant eligibility — only a clean row may verify (fail-closed). The dangerous
// direction stays closed even as the safe direction (revocation) is opened.
func TestBXHIGH008_QuarantinedRow_VerificationNotHonoured(t *testing.T) {
	svc, db, _ := setup(t, "ftr_h008_verify")
	const day = "2026-07-20T00:00:00Z"

	sum := ingestQuarantinedNIN(t, svc, "h008-verify", day, "tok_h008v", `,"nin_verified":true`)
	if sum.Quarantined != 1 {
		t.Fatalf("the credit row must be quarantined, got q=%d", sum.Quarantined)
	}
	// The subscriber is either not created (quarantined rows are not ensured) or
	// exists unverified — never verified from this row.
	var verified int
	if err := db.Admin.QueryRow(context.Background(),
		`SELECT count(*) FROM subscriber_accounts WHERE msisdn_token='tok_h008v' AND nin_verified = true AND effective_to IS NULL`).Scan(&verified); err != nil {
		t.Fatal(err)
	}
	if verified != 0 {
		t.Fatalf("a true from a quarantined row must NOT verify any subscriber (fail-closed), got %d verified", verified)
	}
}

// BX-HIGH-007: a FUTURE-dated file is refused outright (default zero skew), and the
// refusal poisons nothing — a later present-day revocation still applies. Mutation
// proof: remove the clamp and the future file ingests (err == nil).
func TestBXHIGH007_FutureDatedFile_Refused(t *testing.T) {
	svc, db, _ := setup(t, "ftr_h007")

	// A clean, past-dated verification establishes the subscriber.
	ingestNIN(t, svc, "h007-past", time.Now().UTC().Add(-24*time.Hour).Format(time.RFC3339), "tok_h007", `,"nin_verified":true`)

	// A far-future cut carrying true — must be refused (would otherwise store a
	// future nin_verified_as_of that blocks every later revocation).
	future := time.Now().UTC().Add(48 * time.Hour).Format(time.RFC3339)
	const wk13 = `[10000,10000,10000,10000,10000,10000,10000,10000,10000,10000,10000,10000,10000]`
	raw := []byte(`{"telco_id":"SIM_NG","as_of":"` + future + `","rows":[` +
		`{"msisdn_token":"tok_h007","tenure_days":400,"activity_days_30d":15,"active_days_90d":90,"weekly_recharge_minor":` + wk13 + `,"currency":"NGN","quality_flags":[],"nin_verified":true}]}`)
	if _, err := svc.IngestRaw(context.Background(), "SIM_NG", "h007-future", raw); err == nil {
		t.Fatal("a future-dated feature file must be refused (BX-HIGH-007), got nil error")
	}

	// No future nin_verified_as_of was stored.
	var maxAsOf *time.Time
	if err := db.Admin.QueryRow(context.Background(),
		`SELECT max(nin_verified_as_of) FROM subscriber_accounts WHERE msisdn_token='tok_h007'`).Scan(&maxAsOf); err != nil {
		t.Fatal(err)
	}
	if maxAsOf != nil && maxAsOf.After(time.Now().UTC()) {
		t.Fatalf("a refused future cut must not set a future nin_verified_as_of, got %v", maxAsOf)
	}

	// A present-day revocation still applies — it was not blocked by a poisoned future as_of.
	ingestNIN(t, svc, "h007-revoke", time.Now().UTC().Format(time.RFC3339), "tok_h007", `,"nin_verified":false`)
	if v := ninOf(t, db, "tok_h007"); v == nil || *v {
		t.Fatalf("a present-day revocation must apply (not blocked by a poisoned future as_of), got %v", v)
	}
}

// BX-HIGH-007: the future skew is GOVERNED config — a telco may configure a
// tolerance, and an as_of within it is accepted. Proves the clamp reads config
// (not a hardcoded 0) and that the validator accepts the new field.
func TestBXHIGH007_GovernedSkew_AllowsWithinTolerance(t *testing.T) {
	svc, db, sim := setup(t, "ftr_h007_skew")

	// Supersede telco.adapter with a generous 2h future skew, through the governed flow.
	cfgW := configsvc.New(db.Worker)
	ctx := context.Background()
	content := fmt.Sprintf(`{"fulfilment_url":%q,"request_timeout_ms":3000,"retry_budget":0,"circuit_error_threshold_pct":50,"circuit_min_requests":20,"circuit_cooldown_seconds":30,"max_weekly_recharge_minor":100000000,"feature_as_of_max_future_skew_seconds":7200}`, sim.URL)
	c, err := cfgW.CreateDraft(ctx, "telco.adapter", "telco:SIM_NG", "alice", "add feature skew", []byte(content))
	if err != nil {
		t.Fatalf("draft: %v", err)
	}
	for _, step := range []func() error{
		func() error { return cfgW.Submit(ctx, c.ConfigVersionID, "alice") },
		func() error { return cfgW.Approve(ctx, c.ConfigVersionID, "bob") },
		func() error { return cfgW.Activate(ctx, c.ConfigVersionID, "bob", time.Now().UTC()) },
	} {
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}

	// An as_of 1h in the future is now WITHIN the 2h governed skew — accepted + applied.
	within := time.Now().UTC().Add(1 * time.Hour).Format(time.RFC3339)
	ingestNIN(t, svc, "h007-within", within, "tok_skew", `,"nin_verified":true`)
	if v := ninOf(t, db, "tok_skew"); v == nil || !*v {
		t.Fatalf("an as_of within the governed skew must be accepted + applied, got %v", v)
	}
}
