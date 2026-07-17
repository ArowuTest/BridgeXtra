# Gate Review G1 — M1 Walking Skeleton (commit 3bcbd6c)

**Reviewer:** Claude (Fable 5), lead reviewer · **Date:** 17 Jul 2026
**Scope:** full G1 gate per REVIEW_GATES.md — M1a..M1b-5 (commits 1f159f4 → 3bcbd6c), on top of the already-gated M0 foundation.
**Method:** everything below run or read by the reviewer directly in the canonical Docker environment — not accepted from builder reports.

## Verdict: **G1 PASS — first push to main is authorized.**

Zero open findings at any severity. The walking skeleton is complete, correct under adversity, and financially provable on demand.

## Independent verification performed

1. **Full suite:** `go test -race ./...` — all 13 packages green (incl. `backend/e2e`).
2. **Named EDG pack, run by reviewer:** EDG-001 (duplicate confirm → replay, one advance), EDG-002 (8 concurrent confirms → exactly one open advance), EDG-005 ×3 (timeout-after-success: adapter classification; UNKNOWN = no journal + reservation held; resolver continuation → ACTIVE exactly once), EDG-007 (crash between tx1/tx2 → stale-SENT recovered once, never re-lent), EDG-008 (never-landed → NOT_FOUND → FAILED + release), EDG-011 (expired offer → safe rejection), EDG-018 (duplicate recharge — via recovery suite), EDG-020 (over-recovery → applied + suspense), plus the reviewer's own G0-F2 starvation reproducer — **all PASS**.
3. **Wire-level walking skeleton** (`TestChannel_WalkingSkeleton_OverHTTP`): offer → confirm 201 + balanced correlated journal → replay 200 → status route → recovery → CLOSED, outstanding ₦0 — PASS.
4. **BC-3 invariant checker:** verified the implementation (11 set-based sweeps: INV-001/001b/002/003/004/006/013/014/015/016/017 — including ledger-vs-book and pool-vs-open-book cross-checks; read-only; extensible spec table). Ran the **operator job myself** against the dev DB: exit 0, "all invariants hold — the ledger balances at this instant". Randomized-histories property test verified genuinely randomized (24 subscribers, mixed fault scenarios, duplicated keys, interleaved resolver; deterministic seed for exact replay — correct trade-off) — PASS in my run.
5. **SF-7 measurement — honesty check PASSED:** method sound (warm-up excluded, same session, re-runnable test in tree), trigger proven firing on a raw admin unbalanced insert (SQLSTATE P0001), measured +31.8% vs the pre-agreed <10% bar → **declined at M1, correctly**, with four live defense-in-depth layers enumerated and an M3 revisit trigger recorded. This is what honest engineering discipline looks like.
6. **VR-10 folds verified in code:** correlation id bounded ≤64 chars `[A-Za-z0-9_.-]` with re-mint (never truncate); `ADVANCE_NOT_FOUND` on the status route; spec updated same-commit.
7. **Prior gate items carried and verified across VR-4..VR-10:** no-txn-across-network-call (structural), ledger append-only by grants, per-aggregate FIFO with dead-letter quarantine, tenant-isolation adversarial pack, recovery-only-against-ACTIVE/PARTIALLY_RECOVERED, BC-1 Money everywhere above the SQL scan line, BC-2 pipeline (lint/vuln/gosec/coverage 70%+), BC-6 tap-to-journal, BC-7 single-point error mapping.

## Carried notes (non-blocking)

| ID | Tier | Note |
|---|---|---|
| G1-N1 | NIT | Add an INV-018 one-active-advance sweep to the checker — currently enforced only by the partial index; a sweep guards against future schema drift (the builder's update narrative already listed it as covered; the code should match the narrative). |
| G1-N2 | LOW (M5) | The E2E property test uses a fixed seed (right for CI replay). At M5, add a nightly/soak variant with a randomized, logged seed and larger N to explore more history space. |
| G1-N3 | Pre-production (not pre-push) | Migrations create roles with dev-only passwords (`devlocal_*`) — fine for a private repo and local dev; production deployment MUST set role credentials from secrets (ALTER ROLE at deploy) and never reuse these. Track in the go-live checklist. |

## Regulatory position (A-5 / DD-14)

The DEON judgment lands **20 July 2026** (3 days after this gate). The trigger has not yet fired; A-5 stays armed and its re-review is mandatory when the ruling lands. Because the build encodes the conservative regulatory superset, **no ruling outcome changes the walking skeleton** — impact is config/activation-level. G1 therefore does not wait for the ruling.

## Push authorization & logistics

- **First push to main: AUTHORIZED** at commit `3bcbd6c` (plus this gate record).
- The repo has **no remote configured**. Owner decision required: create/confirm the remote (recommend a **private** GitHub repository, consistent with the ArowuTest org pattern). Dev-only credentials in migration history are acceptable for a private repo (see G1-N3).
- CI (`.github/workflows/ci.yml`) will run on first push — it is the same suite verified here.

## Next checkpoint

**G2 — Credit Core (M2):** scoring engine, anti-gaming (₦20k-spike scenario), staleness fail-closed, decision replay (BC-4 arrives), real-time overlays, disclosure/consent evidence, 1M-subscriber scoring run. Gate criteria already recorded in REVIEW_GATES.md.
