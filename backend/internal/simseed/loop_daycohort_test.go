package simseed

import "testing"

// TestDayScopedSeed_DisjointCohorts locks the make-dev day-rollover fix
// (cmd/seed-dev/population_loop.go): the population seeder day-scopes each cohort
// seed (Seed = base + "|" + businessDay) so a re-up on a NEW Lagos day originates a
// FRESH cohort instead of re-originating into an earlier day's subscribers — who by
// then hold open advances / unconfirmed recovery holds that origination correctly
// refuses (the product is right; the seeder must not collide with it).
//
// Cohort identity is a pure function of the seed (msisdnToken / subscriberID), so two
// day-scoped seeds must share NO subscriber id or MSISDN token; that disjointness is
// exactly what prevents the re-origination collision across a midnight rollover. A
// same base seed on the SAME day stays byte-identical, so a same-day re-up remains
// idempotent (the existing replay-idempotency behaviour is preserved).
func TestDayScopedSeed_DisjointCohorts(t *testing.T) {
	const base = "loop-a"
	d1 := base + "|2026-07-26"
	d2 := base + "|2026-07-27"
	for i := 0; i < 64; i++ {
		if msisdnToken(d1, i) == msisdnToken(d2, i) {
			t.Fatalf("member %d: day-scoped seeds must yield DISJOINT MSISDN tokens across days (both = %s)", i, msisdnToken(d1, i))
		}
		if subscriberID(d1, i) == subscriberID(d2, i) {
			t.Fatalf("member %d: day-scoped seeds must yield DISJOINT subscriber ids across days", i)
		}
	}
	// Same base seed + same day is deterministic → a same-day re-up stays idempotent.
	if msisdnToken(d1, 7) != msisdnToken(base+"|2026-07-26", 7) {
		t.Fatal("a day-scoped seed must be deterministic within a day (same-day re-up idempotency)")
	}
}
