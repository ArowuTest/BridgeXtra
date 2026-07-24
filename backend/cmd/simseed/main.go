// cmd/simseed — deterministic, prod-safe synthetic-data seeder for local/CI
// exercise of the Phase 1 recovery pipeline (S3 recon). It is:
//
//   - GUARDED by TCP_SEED_ALLOW=1 so it can never run by accident.
//   - EXPLICIT-DSN-ONLY. Unlike cmd/seed-operators / cmd/migrate, there is NO
//     silent localhost fallback — TCP_SIMSEED_DSN is required, so the operator
//     always chooses the target database consciously. Point it at the tcp_app
//     role DSN (writes are RLS-bound to SIM_NG).
//   - PROD-SAFE. It refuses to touch any database that holds a real
//     (non-synthetic) telco (simseed.VerifySyntheticOnly).
//   - DETERMINISTIC. Same -seed => byte-identical data, so a re-run is idempotent
//     and never trips recovery source_event_id dedup.
//
// Seeder-A scope: the guard + determinism harness + the subscriber cohort.
// Seeder-B/C extend it (recharge/recovery events + the EOD feed), against
// build/PHASE1_S3_SEEDER_CONTRACT.md.
package main

import (
	"context"
	"flag"
	"log"
	"os"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/simseed"
)

func main() {
	if os.Getenv("TCP_SEED_ALLOW") != "1" {
		log.Fatal("simseed: refusing to run without TCP_SEED_ALLOW=1 (dev/CI only — this tool writes synthetic data)")
	}
	dsn := os.Getenv("TCP_SIMSEED_DSN")
	if dsn == "" {
		// NO localhost fallback by design: the seeder must never silently point
		// at a default database. An explicit DSN is required.
		log.Fatal("simseed: TCP_SIMSEED_DSN is required (no default — refusing to guess a target database)")
	}

	seed := flag.String("seed", "phase1-simseed-v1", "deterministic generation seed (same seed => byte-identical data)")
	subscribers := flag.Int("subscribers", 50, "number of synthetic subscribers to seed")
	flag.Parse()

	ctx := context.Background()
	pool, err := platform.NewPool(ctx, dsn)
	if err != nil {
		log.Fatalf("simseed: db connect: %v", err)
	}
	defer pool.Close()

	if err := simseed.VerifySyntheticOnly(ctx, pool); err != nil {
		log.Fatalf("%v", err)
	}

	created, err := simseed.SeedCohort(ctx, pool, simseed.CohortPlan{Seed: *seed, Count: *subscribers})
	if err != nil {
		log.Fatalf("simseed: cohort: %v", err)
	}
	log.Printf("simseed: cohort done — %d subscriber(s) created, %d requested, seed=%q (re-run is a no-op)", created, *subscribers, *seed)
}
