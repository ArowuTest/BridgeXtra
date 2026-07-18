# Gate Review G2 — M2 Credit Core (commits 152b4d1 → 1e5ffa9)

**Reviewer:** Claude (Fable 5), lead reviewer · **Date:** 18 Jul 2026
**Scope:** full G2 gate — M2a..M2f plus the post-G1 deployment commits (448b1a0, bd87fd2, 3a60b51, bd23504) reviewed post-hoc.
**Method:** source read of the scoring engine, validators, ingest, replay; independent full-suite `-race` run (17 packages, all green); named M2 test re-runs in reviewer's hands; repo/deployment posture checks.

## Verdict: **G2 CONDITIONAL PASS — 3 MEDIUM findings, all in the decision path, must be folded before M3 work begins.**

No money-path defect. The ledger, saga, and recovery engines are untouched by these findings. But the credit brain — the part regulators and the risk committee will interrogate — has three defects of the exact class this project's own standards name: armed-but-dead controls and explainability that misstates what happened.

## Findings

### G2-F1 — MEDIUM — `missing_policy` is validated, then consumed by nothing
The scoring.policy validator REQUIRES `missing_policy` ∈ {REJECT, STARTER} with the message "silent imputation is forbidden (V2-SCR-017)" — and **no code anywhere reads the field** (verified by codebase-wide grep: single hit is the struct tag). An admin who sets REJECT believes missing-data subjects are rejected; the engine never looks. This is the armed-but-dead pattern in its purest form, inside the very validator layer built to prevent it. **Fix:** implement the missing-data path (quality flags beyond SHORT_HISTORY → apply the policy) or delete the field from schema+validator+seed. Validated-and-ignored is the one option that may not survive.

### G2-F2 — MEDIUM — `SPIKE_DISCOUNT_APPLIED` asserts an action that never happens
The spike detector (max/median ratio + all-but-one-empty shape) sets `spiky`, whose ONLY effect is appending the reason code `SPIKE_DISCOUNT_APPLIED`. No discount is applied — winsorisation runs identically with or without the flag, and nothing else consumes it. Reason codes are regulator- and support-facing explainability (V1-CRD-008); a code asserting an un-taken action is a mis-explanation — the verify-semantics-not-names failure class, in the customer-facing surface. **Fix:** (a) rename to `SPIKE_ANOMALY_DETECTED` immediately; (b) make an explicit, recorded policy decision on what spiky DOES (cap upward movement to 0 this cycle / feed the M3 fraud overlay / explicitly nothing-but-flag with rationale) — the decision may be "flag only", but it must be a decision, not an accident.

### G2-F3 — MEDIUM — No upper bound on ingested recharge values → overflow silently disarms the spike check
`validateRow` bounds day-counts and rejects negatives but accepts ANY positive int64 weekly recharge. A corrupt feed row near int64-max: (1) passes validation; (2) overflows `maxW*10_000` in the engine to negative, so the spike comparison silently evaluates false; (3) overflows the winsorised total, producing a garbage tier. EDG-014's whole point is corrupt feeds quarantine rather than silently score — this row scores garbage. **Fix:** a plausibility ceiling at ingest (config record, e.g. `max_weekly_recharge_minor`, seeded generously — ₦10m/week is far above any real prepaid subscriber) quarantining rows above it; optionally overflow-safe arithmetic in the engine as defense-in-depth.

## What passed (verified at source / in reviewer's hands)
- **Winsorisation-disarm fix is genuine and well-engineered:** validator floors `winsor_upper_bps` at 9,230 (12/13) with the exact nearest-rank rationale in the error message; superseding seed migration 0008; pinned EDG-013 wash-pattern test. The builder's own test caught the live disarm — BC-8 working.
- **Validator quality is high:** monotonic-ladder check, starter-tier and degrade-cap membership (closes the `p.Tiers[-1]` panic vector via the only write path), decisions-must-expire, spike-ratio sanity floor.
- **BC-4 is real:** pure engine (no clock/DB/randomness), EngineVersion discipline with input echoes (v1.1.0 makes replay self-contained against mutable external state — e.g. barred-after-scoring replays exactly), canonical bytes, tamper detection flags the precise document, `worker -replay` operator job. Full-run bit-exact + tamper tests pass in my hands.
- **M2e boundary wiring:** overlays block at offer AND confirm; expired decision refuses both; consent evidence written inside the confirm transaction — all re-run by me.
- **Scale proof is honest:** first-measurement (89/s) disclosed; 30× via set-based staged-COPY twins (the RLS-refuses-COPY reasoning is correct); behavioural tests unchanged over bulk paths; exclusions and headroom explicitly bounded. 1M in 15.8 min meets the M2 window criterion.
- **Deployment posture (post-hoc review):** repo confirmed **private** (unauthenticated API → 404); G1-N3 closed properly (env-rotated role passwords at boot, never dev passwords in production); managed-cluster BYPASSRLS fallback empirically probed with a serial CI test; DEPLOYMENT.md tracks the before-production list; live money story verified on the deployed stack. gosec annotations audited: both are genuine false positives with rationale.
- Full suite: 17 packages green under `-race` in my independent run; CI green on BridgeXtra main.

## Carried notes
- G2-N1 (LOW): engine integer arithmetic is unchecked generally (BC-1's overflow discipline stops at the Money type); after G2-F3's ingest ceiling this is belt-and-braces — consider checked helpers at M5 hardening.
- G2-N2 (process): worker-as-DB-owner on managed clusters (Render demo) is fine for demo; before ANY real subscriber data, a dedicated non-owner worker role with explicit grants (already tracked in DEPLOYMENT.md "before production" + M4 RBAC).
- G1 carried notes G1-N1 (INV-018 one-active sweep) and G1-N2 (randomized-seed soak) remain open, non-blocking.
- Branch protection on main could not be verified from this session (no `gh` CLI); builder to confirm/enable required-CI-check.

## Gate decision
**CONDITIONAL PASS.** M2 stands; the M2 series stays on main. **G2-F1/F2/F3 must be folded and reviewer-verified before M3 (money core) work begins** — same discipline as G0-F2 before M1. None require redesign: one field implement-or-delete, one rename plus a recorded policy decision, one ingest ceiling.

## Regulatory
DEON judgment expected **20 Jul 2026** (2 days). A-5 re-review fires when it lands. Nothing in this gate changes that posture.

Next checkpoint: **G2-F fold verification**, then **G3 — Money Core (M3)**.
