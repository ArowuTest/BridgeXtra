# ADR-0001: Technology Stack and Data-Access Approach

**Status:** Accepted (owner decision, 17 July 2026)
**Context:** V3 Volume 2 deliberately defers concrete technology to ADRs (§How to Use, V2-ARC-002). This is the first blocking technical decision.

## Decision

| Layer | Choice | Notes |
|---|---|---|
| Backend | **Go** (1.22+) | Owner's proven stack (uduXPass, Nirvet); maps cleanly to V2 requirements |
| Database | **PostgreSQL** | Transactional core + ledger; RLS for tenant isolation; declarative partitioning path for high-volume tables |
| Data access | **pgx v5 with hand-written SQL in repository layer — NO ORM (GORM rejected)** | See rationale |
| Frontend | **Next.js + TypeScript** | Tenant-aware BFF pattern satisfies V2-UI-001 (server-side authorization) |
| Migrations | Numbered SQL files + custom runner (Nirvet pattern: fresh-DB validated, FORCE-RLS-aware) | reference_migrator_force_rls / from-zero lessons apply |
| Events (R1 local) | **Postgres-backed outbox + worker dispatch** (`FOR UPDATE SKIP LOCKED`) | Satisfies V2-EVT-002/V2-EVT-014 (canonical envelope permits later transport migration to a broker at scale — R3 concern, not R1) |
| Simulator | Separate Go service in-repo (`simulator/`) implementing canonical telco contracts + fault catalogue (SIM-001..012) | |

## Rationale for rejecting GORM

1. GORM's implicit behaviors (zero-value skip on updates, hooks, upsert-ish `Save`) are ledger-invariant hazards (V2-LED-001..004).
2. V2 requires SQL-level constructs an ORM obscures: DB permissions on journal tables (V2-LED-015), integrity via keys/constraints not app convention (V2-DAT-002), atomic conditional updates for exposure reservation (V2-ADV-003), RLS, partial unique indexes, partitioning.
3. Owner's audited pattern (entity → repo → usecase → handler, hand-written SQL kept in sync) has survived multiple external reviews; consistency with it is worth more than ORM convenience.
4. `sqlc` may be adopted later for compile-time-checked queries; it generates from the same SQL files and does not change this decision.

## Layering contract (binding for every domain — this is the "consistency across the board")

```
backend/
  cmd/api/            — main, wiring, boot-time migration hook
  cmd/worker/         — outbox dispatcher, job workers
  internal/
    entity/           — pure domain structs + enum constants; no DB/JSON coupling; money = int64 minor units + currency
    repo/             — interfaces + pgx implementations; the ONLY layer containing SQL
    usecase/          — business logic, state machines, invariants; the ONLY layer composing repos in transactions
    handler/          — HTTP, authn, tenant-context resolution, validation; NEVER touches repo directly
    ledger/           — journal posting service; sole writer of journal tables (V2-SRV-002)
    mno/              — telco adapter framework + simulator adapter (canonical contracts)
    config/           — governed configuration service (versions, maker-checker, effective dating)
  migrations/         — NNNN_name.sql, fresh-DB validated in CI
simulator/            — standalone telco simulator service
frontend/             — Next.js portal (admin/risk/finance/ops/support workspaces)
docs/  build/adr/     — SRS volumes, reviews, ADRs, plan
```

No domain may deviate from this shape. Violations are architecture defects, not style preferences (V2-ARC-007).

## Data-access rule (owner-confirmed, 17 Jul 2026)

**All SQL lives in the repository layer — no exceptions, including "for scale."** Scalability concerns change what lives *inside* a repository, never whether code may go around it:

- Read-heavy portal views use purpose-built **read-model repositories** (one joined/aggregated query), not chains of per-entity lookups (V2-SRV-006; kills N+1).
- Batch work (reconciliation matching, aging, score publication) uses **set-based SQL in a repo method**, never Go loops over generic CRUD calls.
- Hot-path atomic operations (exposure reservation) are **single conditional statements** inside the repo (`UPDATE ... WHERE available >= amount RETURNING`).
- The **ledger is stricter still**: `internal/ledger/` is the sole writer of journal tables, enforced by Postgres role permissions (V2-LED-015), not convention.
- Only migrations live outside repositories. Reporting/reconciliation jobs source queries from repo/query packages so every query is enumerable for index review.

## Consequences

- More SQL written by hand; mitigated by repository-layer discipline and property/invariant tests (V2-TST-002/003).
- Broker migration (Kafka etc.) deferred to R3 scale trigger; outbox envelope keeps semantics stable (V2-EVT-014).

## Amendments (17 Jul 2026 — reviewer conditions SF-1/SF-2/SF-4 accepted)

**Requirement-ID convention (SF-1).** Volumes 1–3 reuse ID prefixes with different meanings (V1-CFG-001 ≠ V2-CFG-001). From the first commit, ALL requirement citations — code comments, test names, CI evidence, reviews, ADRs, plans — use volume prefixes: `V1-`, `V2-`, `V3-`. `EDG-`, `INV-`, `DD-` and `SIM-` IDs are globally unique and stay bare. CI test naming convention: `TestV2_ADV_004_DuplicateRequestReturnsOriginal` style.

**Concurrency config/schema boundary (SF-2).** The `advances` partial unique index (one open advance per subscriber) is the R1 DB-level backstop and is *deliberately stronger than the config surface*. While that index exists, config validation MUST reject `max_concurrent_advances > 1` with an explanatory error ("requires schema change ADR + architecture review: replace one-active partial index with per-programme counting under row lock + tested recovery waterfall"). Enabling N>1 is a schema-level, architecture-gated change (V1-PRD-005), never a config flip. A config knob that silently cannot take effect is a prohibited control pattern (armed-but-dead).

**Outbox ordering stance (SF-4) — resolution (a) plus defense-in-depth.** The dispatcher guarantees **per-aggregate FIFO**: a worker may claim an outbox row (`FOR UPDATE SKIP LOCKED`) only if no older unpublished row exists for the same `aggregate_id` (`WHERE NOT EXISTS (SELECT 1 FROM outbox o2 WHERE o2.aggregate_id = outbox.aggregate_id AND o2.seq < outbox.seq AND o2.published_at IS NULL)`). The order key `seq` is a **database-assigned identity column** (not the ULID id): client-generated ULIDs only guarantee ordering within one process per millisecond, whereas the DB sequence gives a total insertion order across all writers. Ordering is therefore guaranteed within `telco_id + aggregate_id` (V2-EVT-004) and NOT across aggregates. Additionally, every consumer remains sequence-aware/pending-match tolerant (V2-EVT-005) — reversal-before-original is the standing proof case — because telco-*inbound* events arrive with no ordering guarantee regardless of our outbox.
