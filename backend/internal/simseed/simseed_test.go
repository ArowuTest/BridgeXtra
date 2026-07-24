package simseed

// Seeder-A adversarial tests: the two non-negotiable properties (prod-safety +
// determinism/idempotency) plus the pure ID stability primitive.

import (
	"context"
	"testing"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
)

// stableID / subscriberID / msisdnToken must be pure functions of (seed, key):
// same inputs → byte-identical output; different inputs → different output.
func TestStableID_Deterministic(t *testing.T) {
	if a, b := stableID("subseed", "s1", "000001"), stableID("subseed", "s1", "000001"); a != b {
		t.Fatalf("stableID not deterministic: %q != %q", a, b)
	}
	if a, b := stableID("subseed", "s1", "000001"), stableID("subseed", "s2", "000001"); a == b {
		t.Fatalf("stableID collided across seeds: %q", a)
	}
	if a, b := subscriberID("s1", 0), subscriberID("s1", 1); a == b {
		t.Fatalf("subscriberID collided across indices: %q", a)
	}
	if a, b := msisdnToken("s1", 0), msisdnToken("s1", 0); a != b {
		t.Fatalf("msisdnToken not deterministic: %q != %q", a, b)
	}
}

// The guard must REFUSE any database that holds a non-synthetic telco.
func TestVerifySyntheticOnly_RefusesForeignTelco(t *testing.T) {
	db := testutil.MustSetup(t, "simseed_guard_refuse")
	ctx := context.Background()
	// Insert a REAL telco (owner pool) — simulating a production/staging DB.
	if _, err := db.Admin.Exec(ctx,
		`INSERT INTO telcos (telco_id, name, country, status) VALUES ('REAL_MTN','Real MTN NG','NG','ACTIVE')`); err != nil {
		t.Fatal(err)
	}
	err := VerifySyntheticOnly(ctx, db.App)
	if err == nil {
		t.Fatal("guard must REFUSE a DB holding a real telco")
	}
	// The refusal must name the offending telco (operator diagnosis).
	if got := err.Error(); !contains(got, "REAL_MTN") {
		t.Fatalf("refusal must name the foreign telco, got: %s", got)
	}
}

// The guard must PASS a purely-synthetic database (SIM_NG only, from migrations).
func TestVerifySyntheticOnly_PassesSyntheticDB(t *testing.T) {
	db := testutil.MustSetup(t, "simseed_guard_pass")
	if err := VerifySyntheticOnly(context.Background(), db.App); err != nil {
		t.Fatalf("guard must pass a purely-synthetic DB: %v", err)
	}
}

// SeedCohort must be deterministic and idempotent: a re-run with the same seed
// creates ZERO new rows, and the identities are exactly the deterministic ones.
func TestSeedCohort_DeterministicIdempotent(t *testing.T) {
	db := testutil.MustSetup(t, "simseed_cohort")
	ctx := context.Background()
	const n = 20
	plan := CohortPlan{Seed: "test-seed-A", Count: n}

	created1, err := SeedCohort(ctx, db.App, plan)
	if err != nil {
		t.Fatal(err)
	}
	if created1 != n {
		t.Fatalf("first run must create all %d, got %d", n, created1)
	}

	// Re-run: byte-identical intent → a pure no-op (idempotent).
	created2, err := SeedCohort(ctx, db.App, plan)
	if err != nil {
		t.Fatal(err)
	}
	if created2 != 0 {
		t.Fatalf("re-run must create 0 (idempotent), got %d", created2)
	}

	// The rows present are exactly the deterministic identities.
	for i := 0; i < n; i++ {
		var token string
		err := db.Admin.QueryRow(ctx,
			`SELECT msisdn_token FROM subscriber_accounts WHERE subscriber_account_id=$1 AND telco_id=$2`,
			subscriberID(plan.Seed, i), SyntheticTelco).Scan(&token)
		if err != nil {
			t.Fatalf("cohort member %d missing: %v", i, err)
		}
		if token != msisdnToken(plan.Seed, i) {
			t.Fatalf("member %d token drift: %q != %q", i, token, msisdnToken(plan.Seed, i))
		}
	}

	// A different seed produces disjoint identities (no accidental overlap).
	created3, err := SeedCohort(ctx, db.App, CohortPlan{Seed: "test-seed-B", Count: n})
	if err != nil {
		t.Fatal(err)
	}
	if created3 != n {
		t.Fatalf("a different seed must create a fresh cohort of %d, got %d", n, created3)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
