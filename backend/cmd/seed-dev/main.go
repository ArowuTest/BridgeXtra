// cmd/seed-dev — reusable SYNTHETIC-ONLY dev-stack seeder. One command turns a bare
// Postgres into a browsable BridgeXtra dev environment by driving the REAL engine — it
// NEVER direct-INSERTs business rows. It:
//  1. applies all migrations (which alone seed the full SIM_NG governed config — there
//     is no config-seeding code here),
//  2. seeds operator logins through the bootstrap credential path (the same one CI
//     uses), idempotently, and
//  3. drives simseed.RunLoop to produce a varied loan population (active / recovering /
//     closed advances) through the real origination + recovery usecases.
//
// With -held-recharges it instead runs the held-recharge scenario: post signed webhooks
// over the per-event clamp with the RECOVERY layer armed LIVE, so the treasury guardrail
// holds them and real rows land in the Finance held-recharge queue (a running api is
// required — the handler verifies the HMAC against the api process's secret env var).
//
// Fail-closed: refuses without TCP_SEED_ALLOW=1, AND refuses to touch a non-synthetic
// database (assertSyntheticOrEmpty gates the whole tool — migrate, operators, population,
// held-recharges). The population drive additionally requires build tag simseed_loop (the
// always-CONFIRMED synthetic MNO adapter is fenced out of untagged/prod builds).
//
// Later slices add: delinquent / written-off / deferred-fee advances, and stale-feed /
// recon-break states.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform/dbmigrate"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform/dbroles"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/simseed"
	"github.com/ArowuTest/telco-credit-platform/backend/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

const defaultProgramme = "prg_sim_airtime01"

type seedOperator struct {
	AdminID string `json:"admin_id"`
	Actor   string `json:"actor"`
	Key     string `json:"key"`
	Role    string `json:"role"`
	Scope   string `json:"scope"`
}

func main() {
	log.SetFlags(0)
	heldMode := flag.Bool("held-recharges", false,
		"run only the held-recharge scenario (assumes a migrated DB + a running api)")
	flag.Parse()

	// Guard against ACCIDENTAL runs — seeding operators and driving the engine is a
	// privileged act. (This alone does NOT prove the target is synthetic; that is
	// assertSyntheticOrEmpty below.)
	if os.Getenv("TCP_SEED_ALLOW") != "1" {
		log.Fatal("seed-dev: refusing to run without TCP_SEED_ALLOW=1 (dev only)")
	}
	adminDSN := mustEnv("TCP_ADMIN_DSN") // migrations + operator credentials (owner/superuser)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	adminPool, err := platform.NewPool(ctx, adminDSN)
	if err != nil {
		log.Fatalf("seed-dev: admin connect: %v", err)
	}
	defer adminPool.Close()

	// Fail closed against a REAL database: the whole tool proceeds only if the DB is
	// empty (fresh — migrate will create the synthetic SIM_NG telco only) or already
	// synthetic (SIM_NG-only). Any real telco present => refuse. This gates migrate,
	// operator seeding, AND the held-recharge scenario — not just the RunLoop drive.
	if err := assertSyntheticOrEmpty(ctx, adminPool); err != nil {
		log.Fatalf("seed-dev: refusing to seed a non-synthetic database: %v", err)
	}

	if *heldMode {
		if err := runHeldRecharges(ctx, adminPool); err != nil {
			log.Fatalf("seed-dev: held recharges: %v", err)
		}
		return
	}

	// 1. Migrate — this ALONE installs the full SIM_NG governed config + telco +
	// programme + funding pool. seed-dev seeds no config of its own.
	n, err := dbmigrate.Apply(ctx, adminPool, migrations.FS)
	if err != nil {
		log.Fatalf("seed-dev: migrate: %v", err)
	}
	log.Printf("seed-dev: migrations applied: %d", n)
	// No-op in dev (env passwords unset keeps the migration-baked dev passwords).
	if _, err := dbroles.ApplyPasswords(ctx, adminPool); err != nil {
		log.Fatalf("seed-dev: role passwords: %v", err)
	}

	// 2. Seed operator logins (bootstrap path, idempotent). These ARE the portal logins.
	seedOperators(ctx, adminPool)

	// 3. Drive the real engine to a varied population (needs -tags simseed_loop).
	appPool, err := platform.NewPool(ctx, mustEnv("TCP_SIMSEED_DSN")) // MUST be tcp_app (RLS-bound, non-BYPASSRLS)
	if err != nil {
		log.Fatalf("seed-dev: app connect: %v", err)
	}
	defer appPool.Close()
	programme := envOr("TCP_SEED_PROGRAMME", defaultProgramme)
	summary, err := drivePopulation(ctx, appPool, programme, seedList(), seedRepeat())
	if err != nil {
		log.Fatalf("seed-dev: population: %v", err)
	}
	log.Printf("seed-dev: %s", summary)
	log.Print("seed-dev: done — dev stack seeded. Sign in at the portal with a seeded operator key.")
}

// assertSyntheticOrEmpty fails closed against a real database. If the schema is already
// migrated (a telcos table exists), it must be SIM_NG-only — VerifySyntheticOnly refuses
// if any non-synthetic telco is present. If the DB is unmigrated (no telcos table), it is
// empty and safe to seed from scratch (migrate creates only the synthetic SIM_NG telco).
func assertSyntheticOrEmpty(ctx context.Context, pool *pgxpool.Pool) error {
	var migrated bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.telcos') IS NOT NULL`).Scan(&migrated); err != nil {
		return fmt.Errorf("probe schema: %w", err)
	}
	if !migrated {
		return nil // empty DB — nothing real to protect yet
	}
	return simseed.VerifySyntheticOnly(ctx, pool)
}

// seedOperators provisions the portal logins from TCP_SEED_OPERATORS (a JSON array of
// {actor,key,role,scope}, the same shape CI uses), through the bootstrap credential
// path. Idempotent: an operator whose actor already exists is skipped.
func seedOperators(ctx context.Context, pool *pgxpool.Pool) {
	raw := os.Getenv("TCP_SEED_OPERATORS")
	if raw == "" {
		log.Print("seed-dev: TCP_SEED_OPERATORS unset — skipping operator seed (no portal logins)")
		return
	}
	var ops []seedOperator
	if err := json.Unmarshal([]byte(raw), &ops); err != nil {
		log.Fatalf("seed-dev: bad TCP_SEED_OPERATORS JSON: %v", err)
	}
	admins := &repo.Admins{Pool: pool}
	created := 0
	for _, o := range ops {
		if o.Actor == "" || o.Key == "" || o.Role == "" {
			log.Fatalf("seed-dev: each operator needs actor, key and role (got %+v)", o)
		}
		scope := o.Scope
		if scope == "" {
			scope = "*"
		}
		var exists bool
		if err := pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM admin_credentials WHERE actor=$1)`, o.Actor).Scan(&exists); err != nil {
			log.Fatalf("seed-dev: operator existence check %q: %v", o.Actor, err)
		}
		if exists {
			continue
		}
		adminID := o.AdminID
		if adminID == "" {
			adminID = "seed_" + o.Actor
		}
		if err := admins.CreateWithRole(ctx, adminID, o.Actor, o.Key, o.Role, scope); err != nil {
			log.Fatalf("seed-dev: create operator %q: %v", o.Actor, err)
		}
		created++
		log.Printf("seed-dev: operator created %s role=%s scope=%s", o.Actor, o.Role, scope)
	}
	log.Printf("seed-dev: operators — %d created, %d in list", created, len(ops))
}

// seedList is the set of RunLoop cohort seeds (each a disjoint synthetic population).
// Override with TCP_SEED_SEEDS (comma-separated); default gives three cohorts.
func seedList() []string {
	if v := os.Getenv("TCP_SEED_SEEDS"); v != "" {
		var out []string
		for _, p := range strings.Split(v, ",") {
			if s := strings.TrimSpace(p); s != "" {
				out = append(out, s)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return []string{"seed-dev-a", "seed-dev-b", "seed-dev-c"}
}

// seedRepeat is the per-profile member count per cohort (TCP_SEED_REPEAT, default 2).
func seedRepeat() int {
	if v := os.Getenv("TCP_SEED_REPEAT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 2
}

func mustEnv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		log.Fatalf("seed-dev: %s is required", k)
	}
	return v
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
