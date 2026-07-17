# SF-7 Measurement — Deferred Balance-Constraint Trigger

**Date:** 17 Jul 2026 · **Builder measurement, for reviewer verification (test: `TestSF7_DeferredTriggerCostMeasurement`, kept in tree and re-runnable)**

## Method
Same database, same session: 30-journal warm-up (excluded), then 300 three-entry journals posted through `ledger.Post` (full tenant transactions) WITHOUT the trigger, then 300 more WITH a `DEFERRABLE INITIALLY DEFERRED` row-level constraint trigger asserting per-journal, per-currency balance at COMMIT. Environment: golang:1.25 container → host Docker Postgres 16 (the same environment as all verification runs).

## Result — the environment cannot support a threshold claim

| Run | Baseline (300 journals) | With trigger | Overhead |
|---|---|---|---|
| 1 | 8.68s | 11.44s | +31.8% |
| 2 | 11.83s | 12.51s | +5.6% |
| 3 | 12.91s | 17.40s | +34.7% |
| 4 | 17.99s | 15.91s | **−11.5%** |
| 5 | 12.38s | 12.12s | −2.1% |

The baseline itself swings 8.7s→18.0s across runs (the dev box carries five other Docker stacks and slept mid-series), and the "overhead" spans −11.5%…+34.7% — the measurement noise exceeds the signal. Two runs even measured the trigger as *faster*, which is physically implausible and proves the numbers are load artifacts, not trigger cost. **No single-run figure from this environment — including the +31.8% first reported — is evidence for or against the <10% bar.**

What the series DOES establish, load-independently: the trigger fires correctly. A raw unbalanced insert executed as admin — bypassing the ledger package entirely — is rejected at COMMIT (`SQLSTATE P0001`) in every run. The structural backstop works as designed.

## Decision: **DECLINE at M1** (revised grounds)

Not "measured 31.8% > 10%" — that number doesn't reproduce. The honest grounds:

1. **The burden of proof is on adoption.** SF-7's pre-agreed bar was "adopt if <~10% posting throughput". This environment cannot demonstrate <10% (or any stable number), and a cost of unknown size is not adoptable against a cost-bounded bar.
2. **Structural expectation of real cost:** constraint triggers are row-level in Postgres, so a 3-entry journal queues 3 deferred events, each re-running the grouped balance scan — cost scales with entries × journals. The mechanism predicts overhead even where this box can't measure it cleanly.
3. **The property is already quadruply guarded** (all live today):
   - `ledger.Post` rejects unbalanced journals **before any row is written** (per-currency, in-transaction) — the only in-code write path;
   - journals/entries are **append-only by grants** — no runtime role can mutate a posted row;
   - **INV-004 sweep** (invariant checker) proves whole-book balance on demand, in CI's property pack, and in the daily control cycle;
   - divergent-duplicate detection (`lines_hash`) makes replay drift loud.

## Revisit trigger (recorded, not silent)
Re-measure at M3 alongside the posting-template engine, **in a controlled environment** (dedicated CI runner or quiet box, ≥10 interleaved A/B rounds, medians not single runs). Also revisit if: (a) entry batching changes the arithmetic (single multi-row INSERT → trigger share shrinks), or (b) a statement-level validation approach becomes viable (transition-table AFTER trigger validating only touched journals in ONE scan per statement — not expressible as a constraint trigger, but combinable with a commit-fence in the posting path). Logged in BUILD_PLAN §9 deferred register.
