// Package simseed is the deterministic, prod-safe synthetic-data seeder for the
// SIM_NG telco — the harness the Phase 1 parallel tracks build on.
//
// Two properties are non-negotiable (both were adversarial-pass blockers):
//
//   - PROD-SAFETY. It refuses to touch any database that holds a real
//     (non-synthetic) telco, and it binds every write to the SIM_NG synthetic
//     telco through RLS. Unlike cmd/seed-operators / cmd/migrate — which silently
//     fall back to postgres://…@localhost:5434/… when their DSN env is unset —
//     the seeder requires an explicit DSN and fails closed on the guard.
//   - DETERMINISM. A re-run with the same seed produces byte-identical rows, so
//     recovery source_event_id dedup / idempotency is never tripped (a wall-clock
//     timestamp would make a re-run look like a fraudulent divergent duplicate).
//     IDs derive from (seed, key) via a stable hash — never platform.NewID (a
//     ULID built from wall-clock + entropy).
//
// This is the Seeder-A foundation (guard + determinism + the subscriber cohort
// everything else hangs off). Seeder-B/C add the recharge/recovery events and the
// EOD recovery-attributed-deduction feed, all against build/PHASE1_S3_SEEDER_CONTRACT.md.
package simseed

import (
	"context"
	"fmt"
	"hash/fnv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
)

// SyntheticTelco is the only telco the seeder is permitted to write.
const SyntheticTelco = "SIM_NG"

// syntheticAllowlist is a TOOL-level safety allowlist (not a runtime threshold):
// the set of telco_ids that may exist in a database the seeder is allowed to
// touch. Kept deliberately conservative — the seeder is dev/CI-only and must
// never run against a database that carries a real telco's data.
var syntheticAllowlist = map[string]bool{SyntheticTelco: true}

// VerifySyntheticOnly refuses to run against any database that holds a telco
// outside the synthetic allowlist. Fail-closed in every direction: a query/scan
// error aborts, any real telco present aborts, and the absence of the SIM_NG
// synthetic telco aborts (a real prod DB, or migrations not applied). This is the
// "never seed a production database" guard the old localhost fallback left open.
func VerifySyntheticOnly(ctx context.Context, pool *pgxpool.Pool) error {
	rows, err := pool.Query(ctx, `SELECT telco_id FROM telcos`)
	if err != nil {
		return fmt.Errorf("simseed guard: cannot read telcos (fail-closed): %w", err)
	}
	defer rows.Close()
	var foreign []string
	seenSynthetic := false
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("simseed guard: scan telco (fail-closed): %w", err)
		}
		if syntheticAllowlist[id] {
			seenSynthetic = true
			continue
		}
		foreign = append(foreign, id)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("simseed guard: telco rows (fail-closed): %w", err)
	}
	if len(foreign) > 0 {
		return fmt.Errorf("simseed guard: REFUSING — target DB holds non-synthetic telco(s) %v; the seeder only runs against a purely-synthetic database", foreign)
	}
	if !seenSynthetic {
		return fmt.Errorf("simseed guard: REFUSING — target DB has no %s telco (not a synthetic database, or migrations not applied)", SyntheticTelco)
	}
	return nil
}

// stableID derives a deterministic, namespaced id from the run seed and a logical
// key. NEVER uses platform.NewID (ULID = wall-clock + entropy), so a re-run with
// the same seed yields a byte-identical id. FNV-1a, matching the simulator's
// stableHash discipline.
func stableID(kind, seed, key string) string {
	return fmt.Sprintf("%s_%016x", kind, stableHash64(seed+"/"+kind+"/"+key))
}

// stableHash64 is the shared deterministic hash primitive.
func stableHash64(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return h.Sum64()
}

// subscriberID / msisdnToken are the deterministic identity for cohort member i
// under a given seed. They live in a dedicated "seed" namespace so they never
// collide with the migration-seeded sub_sim_0001 / tok_sim_0001 fixtures.
func subscriberID(seed string, i int) string {
	return stableID("subseed", seed, fmt.Sprintf("%06d", i))
}
func msisdnToken(seed string, i int) string {
	return fmt.Sprintf("tok_seed_%016x", stableHash64(seed+"/tok/"+fmt.Sprintf("%06d", i)))
}

// CohortPlan is a deterministic subscriber-cohort request.
type CohortPlan struct {
	Seed  string
	Count int
}

// SeedCohort creates Count synthetic subscriber accounts under SIM_NG in the
// dedicated seed namespace, idempotently — the live-identity unique index
// (telco_id, msisdn_token) WHERE effective_to IS NULL makes a re-run a no-op.
// The write is tenant-scoped: RLS structurally binds every row to SIM_NG, so even
// a coding slip cannot write another telco's data (defence in depth on top of the
// VerifySyntheticOnly guard). Returns the number of NEW rows created.
func SeedCohort(ctx context.Context, appPool *pgxpool.Pool, plan CohortPlan) (int, error) {
	if plan.Count <= 0 {
		return 0, fmt.Errorf("simseed: cohort count must be positive, got %d", plan.Count)
	}
	if plan.Seed == "" {
		return 0, fmt.Errorf("simseed: cohort seed must be non-empty (determinism)")
	}
	created := 0
	tctx := platform.WithTenant(ctx, SyntheticTelco)
	err := repo.WithTenantTx(tctx, appPool, func(tx pgx.Tx) error {
		for i := 0; i < plan.Count; i++ {
			ct, err := tx.Exec(ctx, `
				INSERT INTO subscriber_accounts (subscriber_account_id, telco_id, msisdn_token, status)
				VALUES ($1, $2, $3, 'ACTIVE')
				ON CONFLICT (telco_id, msisdn_token) WHERE effective_to IS NULL DO NOTHING`,
				subscriberID(plan.Seed, i), SyntheticTelco, msisdnToken(plan.Seed, i))
			if err != nil {
				return fmt.Errorf("simseed: insert cohort member %d: %w", i, err)
			}
			if ct.RowsAffected() == 1 {
				created++
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return created, nil
}
