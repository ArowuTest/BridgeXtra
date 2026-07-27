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
	"regexp"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/simseed"
)

// seedRe constrains the -seed flag to a simple identifier. This is a real input
// guard (a seed is a stable label, not free text) and it makes the value safe to
// echo to the log (no control characters → no CWE-117 log injection).
var seedRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// dateRe constrains the -recovery-day flag to a bare ISO date, so it is safe to echo
// to the log (SeedRecoveryDay re-parses it strictly as a Lagos business day).
var dateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

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
	recoveryDay := flag.String("recovery-day", "", "if set (YYYY-MM-DD), also seed a clean RECOVERY day (wh: events + matching EOD feed) for this Lagos business day")
	recoveryCount := flag.Int("recovery-count", 0, "cohort members that recovered on -recovery-day (0 => all seeded subscribers)")
	loop := flag.Bool("loop", false, "varied-user loop mode: drive a mixed cohort through the REAL usecases (feature->score->originate->[recover]). Requires a build with -tags simseed_loop.")
	businessDay := flag.String("business-day", "", "loop mode: the Lagos business day (YYYY-MM-DD) to run — use a recent day so profiles score FRESH")
	programme := flag.String("programme", "prg_sim_airtime01", "loop mode: the programme to originate against")
	repeat := flag.Int("repeat", 1, "loop mode: members per profile")
	flag.Parse()

	if !seedRe.MatchString(*seed) {
		log.Fatalf("simseed: -seed must match %s (a simple stable identifier)", seedRe.String())
	}
	if *recoveryDay != "" && !dateRe.MatchString(*recoveryDay) {
		log.Fatalf("simseed: -recovery-day must be YYYY-MM-DD")
	}
	if *loop {
		if !dateRe.MatchString(*businessDay) {
			log.Fatal("simseed: -loop requires -business-day YYYY-MM-DD")
		}
		if !seedRe.MatchString(*programme) {
			log.Fatalf("simseed: -programme must match %s", seedRe.String())
		}
	}
	recCount := *recoveryCount
	if recCount == 0 {
		recCount = *subscribers
	}
	if *recoveryDay != "" && recCount > *subscribers {
		// Each recovery event references a cohort subscriber (FK); the day cannot
		// cover more members than were seeded.
		log.Fatalf("simseed: -recovery-count %d exceeds -subscribers %d (no cohort member to reference)", recCount, *subscribers)
	}

	ctx := context.Background()
	pool, err := platform.NewPool(ctx, dsn)
	if err != nil {
		log.Fatalf("simseed: db connect: %v", err)
	}
	defer pool.Close()

	if err := simseed.VerifySyntheticOnly(ctx, pool); err != nil {
		log.Fatalf("%v", err)
	}

	// Loop mode is a distinct entrypoint (build-tag-split loopEntry: the real driver
	// under -tags simseed_loop, a fatal stub otherwise — the synthetic always-CONFIRMED
	// adapter is fenced out of normal builds).
	if *loop {
		loopEntry(ctx, pool, *seed, *businessDay, *programme, *repeat)
		return
	}

	created, err := simseed.SeedCohort(ctx, pool, simseed.CohortPlan{Seed: *seed, Count: *subscribers})
	if err != nil {
		log.Fatalf("simseed: cohort: %v", err)
	}
	// #nosec G706 -- *seed is validated against seedRe (^[A-Za-z0-9._-]+$) above,
	// so it carries no control characters and cannot forge log lines (CWE-117).
	log.Printf("simseed: cohort done — %d subscriber(s) created, %d requested, seed=%q (re-run is a no-op)", created, *subscribers, *seed)

	if *recoveryDay != "" {
		res, err := simseed.SeedRecoveryDay(ctx, pool, simseed.RecoveryDayPlan{
			Seed: *seed, BusinessDate: *recoveryDay, Count: recCount,
		})
		if err != nil {
			log.Fatalf("simseed: recovery day: %v", err)
		}
		// #nosec G706 -- *seed matches seedRe and *recoveryDay matches dateRe above,
		// so neither can carry control characters into the log line (CWE-117).
		log.Printf("simseed: recovery day %s done — %d event(s) + %d feed row(s) created, control total %d minor, seed=%q (re-run is a no-op)",
			*recoveryDay, res.EventsCreated, res.FeedRowsCreated, res.TotalMinor, *seed)
	}
}
