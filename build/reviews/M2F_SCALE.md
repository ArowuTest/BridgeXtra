# M2f Scale Proof — 1,000,000-Subscriber Ingest + Scoring Run

**Date:** 18 Jul 2026 · **Harness:** `backend/internal/usecase/scoringrun/scale_test.go` (env-gated `TCP_SCALE_N`, kept in tree and re-runnable) · **Environment:** golang:1.25 container → host Docker Postgres 16 on the dev box — the same environment as every verification run. M2 exit criterion (BUILD_PLAN §1): *"scoring run on 1M synthetic subscribers within window."*

## Method
One detached container run, real stack end to end: the simulator generates the canonical 1M-row feature file over HTTP (deterministic, ~260MB JSON); ingestion validates/canonicalises every row and lands it through the set-based repo paths; the scoring run pages subjects (single joined query per page), computes every decision through the pure engine in memory, and bulk-writes each page with full BC-4 replay pins. Wall-clock measured per stage inside the test.

## Result (n = 1,000,000; run PASSED — all subjects scored, zero skips)

| Stage | Wall-clock | Rate |
|---|---|---|
| Fetch (generate + transfer file) | 54.5s | — |
| Ingest (validate, canonicalise, subscribers + snapshots) | 6m 27s | **2,581 rows/s** |
| Score (engine + decisions with replay pins, all current) | 7m 57s | **2,095 subjects/s** |
| **Total** | **~15.8 min** | |

Replay verification is excluded from the harness above 50k by design — full-run replay is its own operator job (`worker -replay <run>`) with its own window; the bit-exactness property is proven by the M2d adversarial pack and full-run verification at 2k/50k scale.

## The finding that made this pass honest
The first harness run measured **89 subjects/s** (~3.1 hours for 1M): the scorer paid 4 network round trips per subject. Per the owner's standing rule ("scale" = specialized repo methods, never bypassing the repository layer), three set-based twins were added — `Subscribers.BulkEnsureByToken`, `FeatureSnapshots.BulkUpsert`, `Decisions.BulkInsertScored` — each a temp-table `COPY` + one `INSERT..SELECT` per page (Postgres refuses `COPY` directly into an RLS table, SQLSTATE 0A000, so the staged form is also the only correct one). Same invariants, same RLS `WITH CHECK` per row, ~30× throughput. All behavioural tests (dedup, quarantine, one-tier-up across runs, replay bit-exactness) pass unchanged over the bulk paths.

## Headroom notes (recorded, not promised)
- Rates above were measured while the CI-shape race suite ran concurrently against the same Postgres; an uncontended nightly run will be faster.
- The whole-file single transaction is the current correctness choice (a crashed ingest leaves nothing half-visible). If files grow ~10×, per-chunk transactions with the existing resume machinery are the next lever.
- Decision pages are 500 subjects; page-size tuning was not needed to meet the window and is left for evidence-driven tuning at M5 load tests.
