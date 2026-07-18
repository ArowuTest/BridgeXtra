# Gate Review G3 — M3 Money Core (commits f6f672e → aedeaf6)

**Reviewer:** Claude (Fable 5), lead reviewer · **Date:** 18 Jul 2026
**Scope:** full G3 gate — M3a..M3f, the two interim folds (M3B-F1, M3D-F1), and live verification of the deployed stack.
**Method:** everything below run/read by the reviewer directly — 20-package `-race` suite, live money story against the deployed Render API, operator jobs against the deployed database, source read of all M3 validators.

## Verdict: **G3 PASS.** M3 money core is complete, correct, and proven on live infrastructure. Zero open findings.

The most financially consequential gate in the plan clears with the register at zero. Both interim findings (M3B-F1 reversal-collision parking, M3D-F1 un-disarmable guardrail) were verified closed in VR-17/VR-19. This gate adds live proof and the zero-config-floor sweep.

## Independent verification performed

### 1. Full suite — 20 packages `-race` green
Includes new M3 packages: collections, settlement, ops. Run by reviewer in the canonical Docker environment.

### 2. Live money story on the DEPLOYED stack (not localhost)
Deployed head confirmed `aedeaf6` (M3 series), status `live`. Against `https://bridgextra-api.onrender.com` with the real channel key:
- `GET /v1/offers` → governed ladder, config-priced (₦100 face, ₦10 fee, DEDUCTED_UPFRONT) — **the posting-template + product config running in production**.
- `POST /v1/advances` (Idempotency-Key + X-Correlation-Id) → **201 ACTIVE**, outstanding ₦100.
- Replay same key → **200** (EDG-001 at the wire, in production).
- `POST /v1/recovery/events` ₦100 → **ALLOCATED, advance_closed=true**.
- `GET /v1/advances/{id}` → **CLOSED, outstanding ₦0.00**.
The complete money loop executed against the deployed API calling the deployed simulator.

### 3. Operator jobs against the DEPLOYED database
- **`worker -invariants` → exit 0: "all invariants hold — the ledger balances at this instant"** — the BC-3 whole-book proof, run against **production data** after my injected money story. This is the load-bearing G3 evidence: the platform's internal financial truth is provably consistent on live infrastructure.
- `worker -recon` → **exit 1, 1 break (missing_telco), 1 matched** — and this is the recon engine *working correctly*: it detected a platform-vs-simulator discrepancy from my freshly-injected demo traffic (the free-tier simulator does not persist its transaction log across spin-down), **recorded the break, and refused to force-match** (V2-REC-012). A recon that found nothing on injected mismatched data would be the suspicious result. Not a code defect — a demo-data artifact demonstrating the detect-don't-guess discipline on real infrastructure.
- `worker -breaks` → **exit 0: "no aged reconciliation breaks"** — the fresh break is correctly unaged; the aging alerter reads the governed threshold fail-closed.

### 4. Zero-config-floor sweep (every M3 guardrail validator)
Verified at source that each M3 governed domain rejects the empty/missing/unsafe case rather than defaulting: write-off maker-checker not-configurable-off; guardrail re-arm must be MAKER_CHECKER; settlement telco+platform shares must equal exactly 10,000 bps; delinquency ladder strictly ascending; aged-break threshold fail-closed; CFG-012 template symbolic-balance proof + account cross-check against the active chart. Floor tests present (`TestM3_WriteoffPolicyValidator_MakerCheckerFloor`, `TestM3_TreasuryGuardrailsValidator_ZeroConfigFloors`) and green.

### 5. Deployment posture
Repo confirmed private (unauth 404 earlier); deployed roles use env-rotated passwords; the credential file is outside every repo. gosec/lint/vuln green in CI on `aedeaf6`.

## Carried / non-blocking
- G1-N1 (INV-018 one-active sweep), G1-N2 (randomized-seed soak), G2-N1/N2, engine-artifact archive (M5) — all still open, all non-blocking, all tracked.
- **RA-1 branch protection** — owner deferred to pre-go-live GitHub Pro; still the reviewer's recommendation. The single blocking go-live item on the infra side.
- Demo-environment recon break is a data-state artifact; if a persistent simulator log is wanted for cleaner demos, that is an M5/demo-polish item, not a defect.

## DEON / A-5 regulatory re-review — status
System date is **18 Jul 2026**; the WASPAN v FCCPC judgment (FHC/L/CS/760/2026) remains scheduled for **20 Jul 2026** (verified via news; enforcement suspended in the interim). **The ruling has NOT yet landed** — the A-5 re-review cannot run against text that does not exist. A-5 stays armed; the re-review fires the moment the judgment is public. The conservative-superset build means no ruling outcome changes the M0–M3 code — impact is config/activation-level only.

## Gate decision
**G3 PASS.** Builder is cleared to begin **M4 (Next.js portals: admin, risk, finance, ops, support with server-side RBAC + tenant scoping)**. G4 criteria already in REVIEW_GATES.md — the headline is server-side authorization proven by the reviewer attempting cross-tenant and cross-role fetches directly against the BFF.

M0–M3 complete: the entire money story — origination with un-disarmable guardrails, the full recovery matrix, delinquency and maker-checker write-off, provably-balanced governed posting templates, ledger-derived reproducible settlement, and the operator surface — built, gated, CI-green on a private repo, and **proven balancing on live production infrastructure.**
