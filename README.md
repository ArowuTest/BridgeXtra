# Telco Digital Credit Platform

Multi-telco airtime/data credit platform — the licensed credit engine behind
telco-distributed advance products (Nigeria first). The platform owns the
credit decision, advance records, exposure, append-only financial ledger,
recovery allocation, reconciliation and regulatory evidence; telcos own the
network rails (channels, fulfilment, recharge data, garnishment execution).

**Governing documents** (read before contributing):

| Document | Purpose |
|---|---|
| `docs/SRS_V3_Volume1..3` | Authoritative requirements baseline (v3.0) |
| `docs/SRS_V3_REVIEW.md` | External review of the baseline |
| `build/BUILD_PLAN.md` | Binding build plan: milestones, schema/index matrix, idempotency & concurrency patterns |
| `build/adr/` | Architecture decisions (ADR-0001: stack, no-ORM, SQL-in-repo rule, SF amendments) |
| `build/REVIEW_GATES.md` | Reviewer gates G0–G5 + standing findings SF-1..7 |
| `build/ASSUMPTIONS.md` | Time-bound assumptions for unresolved design decisions (V1-GOV-007) |

**Requirement citations** use volume prefixes (`V1-`/`V2-`/`V3-`); `EDG-`,
`INV-`, `DD-`, `SIM-` are global (SF-1).

## Layout

```
backend/            Go services (ADR-0001 layering: entity → repo → usecase → handler)
  cmd/api           API service (boot-time self-migration)
  cmd/worker        Outbox dispatcher (per-aggregate FIFO, SF-4)
  cmd/migrate       Standalone migration runner
  internal/...      Domain packages — ALL SQL lives in internal/repo
  migrations/       Numbered SQL, fresh-DB validated in CI
simulator/          Standing telco simulator (V2-SIM-001..012)
frontend/           Next.js portals (from M4)
```

## Local development (Windows host; A-14)

```bash
# 1. Postgres 16 on port 5434 (container name: telco-credit-postgres)
docker run -d --name telco-credit-postgres -e POSTGRES_PASSWORD=devlocal \
  -e POSTGRES_DB=telco_credit -p 5434:5432 postgres:16-alpine

# 2. Migrate + run
go run ./backend/cmd/migrate
go run ./backend/cmd/api       # :8090
go run ./backend/cmd/worker
go run ./simulator/cmd/simulator  # :8091

# 3. Tests (no race on Windows-native Go)
go test -count=1 ./backend/...

# 4. Race-enabled suite (golang Docker image; the pattern CI uses)
MSYS_NO_PATHCONV=1 docker run --rm -v "C:/Users/sanus/telco-credit-platform:/src" \
  -w /src -e TCP_TEST_HOSTPORT=host.docker.internal:5434 \
  -e GOFLAGS=-buildvcs=false golang:1.25 go test -race -count=1 ./backend/...
```

Test databases are created per test package (`telco_credit_test_*`), migrated
from zero on every run, and exercised through the real database roles:
`tcp_app` (RLS-enforced) and `tcp_worker` (BYPASSRLS dispatcher). Local dev
role passwords in `0001_core.sql` are local-only; production credentials come
from the secrets manager (V2-SEC-005) — never from migrations.

## Non-negotiables (enforced by review, V2-ARC-007)

- All SQL in `internal/repo` — scale means specialized repo methods, never bypass.
- Money is `BIGINT` minor units + currency; no floats (V2-API-005).
- The DB is the idempotency arbiter (unique keys), app code returns stored outcomes.
- Never hold a DB transaction across a network call (V2-ADV-006).
- `FULFILMENT_UNKNOWN` is never blind-retried (INV-009).
- Ledger journals are append-only; corrections are linked reversals (V2-LED-002/003).
- Missing tenant context = zero rows, never all rows (fail closed).
