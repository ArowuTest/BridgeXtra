# Gate Review G0 — M0 Foundation

**Reviewer:** Claude (Fable 5), lead reviewer · **Date:** 17 Jul 2026
**Commits reviewed:** `0889b82` (M0 foundation), `86843d5` (OpenAPI specs + drift CI)
**Method:** read every source file in the M0 tree; ran the full suite myself with `-race` in `golang:1.25` against `telco-credit-postgres` (bridge 172.17.0.2:5432); wrote one adversarial reproducer.

## Verdict: **G0 CONDITIONAL PASS** — 1 HIGH must-fix before G1 closes; foundation is otherwise sound.

M0 is real, and the quality is high. The database genuinely is the arbiter, RLS fails closed, and the standing findings I raised pre-start are implemented as specified — not merely referenced. One latent defect in the event backbone, confirmed with a failing test, must be fixed before M1 routes financial events through it.

## Independent verification results

Ran `go vet` + `go test -race -count=1 ./api/... ./backend/... ./simulator/...` myself.
- **Builder's suite: all green** — `api`, `handler`, `repo`, `configsvc` pass under `-race`. Migrations proven from zero on a dropped/recreated DB per test package (real from-zero validation, not assumed).
- **My adversarial reproducer: FAILS as predicted** — see G0-F2.

### SF conditions — verified at source (not by report)
- **SF-1** ✓ every citation across ADR/plan/code/tests carries a volume prefix; CI test names embed it (`TestV2_TEN_003_...`).
- **SF-2** ✓ `validateConcurrency` (`configsvc.go:172`) introspects the **live schema** via `AdvancesOneActiveIndexExists` and rejects `max_concurrent_advances > 1` with the ADR-referenced instruction while the backstop exists — and fails safe (`!tableExists → reject`) before the advances table even exists. This is the zero-config-floor discipline done correctly.
- **SF-3** ✓ `tenantisolation_test.go` exercises cross-tenant read (→NotFound), cross-tenant UPDATE (→zero rows, verified untouched via admin pool), forged-`telco_id` INSERT (→RLS WITH CHECK rejects), no-context query (→0 rows, `WithTenantTx` refuses). Tests run as the **real `tcp_app` role**, not a superuser — the only way this proves anything.
- **SF-4** ✓ per-aggregate FIFO claim predicate (`outbox.go:33`) is correct: `NOT EXISTS (older unpublished row same aggregate)` blocks successors; a locked-but-uncommitted predecessor still has `published_at IS NULL` so it fails safe. Order key is the DB `seq`, not the ULID (the ADR correction is real).
- **SF-5** ✓ `terminal` flag + partial sweep index; `validateIdempotencyTTL` enforces the 72h hard floor; seeded default 168h/72h.
- **SF-6** ✓ ASSUMPTIONS.md A-1..A-14 present; A-5 (conservative regulatory superset, 20 Jul DEON trigger) is the right posture.

### One suspicion I checked and cleared
I suspected the tenant-mismatch security-audit write (`middleware.go:50`, fire-and-forget `_ =`) would be **rejected by RLS** — a platform-scope row with empty `telco_id` under no tenant context. It is NOT a bug: `InsertPlatform` uses `NULLIF($2,'')`, so the row is written with `telco_id = NULL`, which the `audit_tenant` WITH CHECK clause explicitly permits (`OR telco_id IS NULL`). `middleware_test.go` proves the row lands (count = 1). Cleared.

## Findings

### G0-F2 — HIGH — Outbox head-of-line starvation stalls financial-event dispatch
**File:** `backend/internal/usecase/outboxdispatch/dispatch.go` + `backend/internal/repo/outbox.go`
**Reproducer:** `backend/internal/usecase/outboxdispatch/reviewer_starvation_test.go` (uncommitted, reviewer-authored) — **FAILS**: `delivered=0 after 10 cycles`.

**Failure scenario:** `ClaimBatch` selects unpublished events globally ordered by `seq`, `LIMIT claim_batch_size` (seeded 50). Events that can never publish this cycle — an **unregistered event type**, or an event **parked at `max_attempts`** — are `continue`d in `RunOnce` (left unpublished, correctly), but they remain in the unpublished set and are **re-claimed into the batch every single cycle**. Put 50 such poison events on 50 distinct aggregates (each is head-of-its-own-aggregate, so all 50 are claimable) and they fill the entire batch; a healthy event at `seq 51` is never reached. Dispatch is stalled while the backlog "looks" like it's being worked.

**Why HIGH:** this is the backbone every M1 financial event rides — `FulfilmentConfirmed`, `RecoveryApplied`, `LedgerJournalPosted`. V2-EVT-012 explicitly requires recovery/ledger events prioritised above others; a stalled dispatcher means acknowledged financial events silently stop propagating (durability/liveness), which is precisely the failure class the whole design exists to prevent. No money is corrupted (idempotency + FIFO hold), so it is not CRITICAL — but undelivered ledger events are not shippable.

**Scope note (why G0 still passes):** in M0 there are no ledger/recovery events yet — blast radius opens at M1. The fix is bounded, not a redesign. Therefore: **fix required before G1 closes, and my reproducer must go green.**

**Fix direction (builder's call):** two independent leaks to close —
1. **Unhandled types must not occupy the claim window.** Pass the dispatcher's registered event types into `ClaimBatch` as a filter (`AND event_type = ANY($types)`), so an event nobody consumes is never claimed. (Or: forbid appending an event type with no registered consumer.)
2. **Max-attempts events must leave the unpublished set.** Dead-letter them (a `dead`/`parked_at` flag or a DLQ table excluded from the claim predicate) so permanently-failed events stop consuming slots and become explicit operator-replay backlog (V2-EVT-008), not silent drag.
Preserve the per-aggregate FIFO guard exactly as-is — a *quarantined* head must still block its own aggregate's successors; it must only stop blocking *other* aggregates and stop occupying the batch.

### G0-F3 — LOW — residual nits from VR-1 still open
- VR-1a: `ADR-0001` context line still cites bare `ARC-002` (→ `V2-ARC-002`).
- VR-1b: `BUILD_PLAN §10.3` still says local Postgres "port TBD" while A-14 + `testutil.go` fix it at 5434 — stale text, align to A-14.
Neither blocks anything; fold into the next commit.

## Gate decision
- G0: **CONDITIONAL PASS.** M0 committed state stands; foundation is sound and the SF conditions are genuinely met.
- **G1 entry is gated on G0-F2 fixed** (reviewer reproducer green) — do it as the first M1 change, before the walking-skeleton saga starts appending real events, so M1 is built on a dispatcher that can't starve.
- G0-F3 opportunistic.

Next checkpoint: **G1** (walking skeleton) — I will re-run EDG-001/002/004/005/007/009/018 myself and read the fulfilment saga source for the no-txn-across-network-call rule.
