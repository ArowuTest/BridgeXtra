# SF-7 Measurement — Deferred Balance-Constraint Trigger

**Date:** 17 Jul 2026 · **Builder measurement, for reviewer verification (test: `TestSF7_DeferredTriggerCostMeasurement`, kept in tree and re-runnable)**

## Method
Same database, same session: 30-journal warm-up (excluded), then 300 three-entry journals posted through `ledger.Post` (full tenant transactions) WITHOUT the trigger, then 300 more WITH a `DEFERRABLE INITIALLY DEFERRED` row-level constraint trigger asserting per-journal, per-currency balance at COMMIT. Environment: golang:1.25 container → host Docker Postgres 16 (the same environment as all verification runs).

## Result

| Configuration | 300 journals | per journal |
|---|---|---|
| Without trigger | 8.68s | ~28.9ms |
| With trigger | 11.44s | ~38.1ms |
| **Overhead** | | **+31.8%** |

The trigger fires correctly: a raw unbalanced insert executed as admin — bypassing the ledger package entirely — is rejected at COMMIT (`SQLSTATE P0001`), proving the structural backstop works as designed.

## Decision: **DECLINE at M1** (threshold was <10%; measured 31.8%)

Rationale: constraint triggers are row-level in Postgres, so a 3-entry journal queues 3 deferred events, each re-running the grouped balance scan — the cost scales with entries × journals. The pre-agreed adoption bar (ENGINEERING_STANDARD SF-7 note: "adopt if <~10% posting throughput") is decisively missed.

What still guards balance (defense in depth, all live today):
1. `ledger.Post` rejects unbalanced journals **before any row is written** (per-currency, in-transaction) — the only in-code write path;
2. journals/entries are **append-only by grants** — no runtime role can mutate a posted row;
3. **INV-004 sweep** (invariant checker) proves whole-book balance on demand, in CI's property pack, and in the daily control cycle;
4. divergent-duplicate detection (`lines_hash`) makes replay drift loud.

## Revisit trigger (recorded, not silent)
Re-measure at M3 alongside the posting-template engine if either: (a) entry batching changes the arithmetic (single multi-row INSERT, fewer round trips → trigger share shrinks), or (b) a statement-level validation approach becomes viable (transition-table AFTER trigger validating only touched journals in ONE scan per statement — not expressible as a constraint trigger, but combinable with a commit-fence in the posting path). Logged in BUILD_PLAN §9 deferred register.
