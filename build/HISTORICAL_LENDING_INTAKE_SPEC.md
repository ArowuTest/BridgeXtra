# Historical Lending Data Intake — Reviewer Design Gate (forward-ready)

**Author:** Lead reviewer · **Date:** 18 Jul 2026
**Status:** Design gate for the builder — build to this; deviations are architecture findings.
**Owner directive:** build the historical-lending-data ingestion now, ready to integrate the moment real MTN/incumbent data arrives, and **dynamic so it can be easily adapted** to whatever format the telco actually provides.

## Purpose & framing
The behavioural feature pipeline (M2) is built. This is a DIFFERENT feed: a subscriber's prior *lending* history — loans taken, repayment behaviour, defaults/write-offs, and (highest risk) current balances. It is the raw material for (a) calibrating the credit model on real outcomes, (b) seeding per-subscriber trust so proven repayers don't start cold, and (c) — separately and carefully — migrating live open balances. It maps to the SRS's own **DD-24 (migration population)** and the deferred **MIG-\*** workstream, started early at the SCHEMA + INTAKE level only.

We build this now because the *shape* can be defined and proven against the simulator with zero dependency on MTN. We do NOT pretend to have the data or the legal basis yet.

## The load-bearing design decision (what "dynamic" means here)
**Adaptability lives in a governed MAPPING layer, never in the canonical shape.**
- **Canonical internal schema** = typed, fixed, validated. Credit history is financial data — a free-form/untyped canonical target is prohibited (it would defeat quarantine, lineage, replay).
- **Source→canonical mapping** = a **governed config domain** (`lending_history.mapping`), versioned, maker-checker-approved, effective-dated, validated at approval, with a **dry-run preview against a sample before activation** (reuse the CFG-005 simulation discipline). A new telco/format = a new mapping version, not a code change. That is the platform's own configuration-first / no-hardcoding principle applied to data intake.
- Do NOT build a generic ETL engine. Scope is one canonical schema + a column/field mapping mechanism + validation + the simulator feed. Resist scope creep beyond that.

## Canonical schema (build the typed target)
A per-subscriber historical-lending record, telco-scoped, e.g.:
- `telco_id`, source subscriber ref, `msisdn_token`, effective-identity linkage
- prior-loan aggregates: count, total borrowed, avg ticket, tenure of borrowing relationship
- performance: successful repayments, partial, delinquencies, write-offs, avg time-to-repay
- (SEPARATE, flagged) current open balance components — principal/fee/outstanding — carried but NOT auto-applied (see lanes)
- money as minor-units `Money` (BC-1), never floats
- provenance: source batch id, checksum, mapping version, ingested_at, `as_of`
- **lawful-basis reference** (see guardrail L below)

## Three consumption lanes — built as SEPARATE, staged steps (never conflated)
1. **Model-calibration extract** (highest value, lowest risk): aggregate/de-identified export for data science to replace placeholder risk numbers. Build the intake + this extract lane now.
2. **Per-subscriber trust seed** (high value, medium care): feed proven external repayment into the scoring engine's existing trust dimension. Must respect telco-scoped identity + effective periods — a **recycled/ported MSISDN must never inherit prior history** (EDG-017 / MIG-006). Schema + hooks now; wiring into scoring gated on real data + risk sign-off.
3. **Open-balance migration** (highest risk): creating live outstanding debt from imports. **NOT auto-applied.** Requires the full MIG reconciliation (opening balances reconcile to the kobo, MIG-002; uncertain records quarantined, MIG-003; balanced opening-ledger entries). Schema carries the fields; the applying path stays DORMANT behind a separate gate until real data + reconciliation exist.

## Non-negotiable guardrails (reviewer will verify at the gate)
- **G-A Typed canonical + quarantine-not-drop:** every row validates against the typed schema; bad/uncertain rows quarantine with reasons, never silently coerced or given invented precision (MIG-003). No silent drops.
- **G-B Mapping is governed config, not code, not free-form:** `lending_history.mapping` is versioned + maker-checker + validated + preview-before-activate. A validator rejects a mapping that can't produce the typed canonical shape (armed-but-dead prevention).
- **G-C Identity-period safety:** import respects `subscriber_accounts` effective periods; unmatched/ambiguous identities quarantine; a recycled number never inherits prior lending history or debt.
- **G-D No live debt from import without the migration gate:** lane 3 (open balances) cannot create outstanding exposure or ledger entries except through the separate, dormant, reconcile-to-the-kobo migration path. An import batch by itself moves no money.
- **G-E Lineage & reversibility:** every imported record retains source batch, checksum, mapping version; an import is an identifiable batch that can be superseded, never a silent overwrite of derived trust or balances.
- **G-F Money discipline:** all amounts are `Money` (BC-1); the CI float-ban covers the new package.
- **G-L Lawful-basis stamp (reviewer add — hard requirement):** an import batch MUST carry a recorded lawful-basis / consent / data-agreement reference before ingest proceeds. The pipeline **refuses** to ingest personal financial lending history with no lawful-basis reference (NDPA; V1 §17). Fail closed — absent basis is never "proceed anyway." This protects you from a pipeline that would happily swallow data you don't yet have the right to hold.

## Simulator work (proves it now, no MTN needed)
- Simulator emits a synthetic historical-lending feed in the canonical shape AND a deliberately **messy, MTN-shaped variant** (different column names, missing fields, dirty values) so the mapping + quarantine + preview paths are exercised end-to-end today.
- Spec bumped, drift-test updated, same-commit (existing OpenAPI discipline).

## Acceptance (what the gate checks)
- Canonical schema + governed mapping domain with validator + preview; a new mapping version maps the messy variant to canonical with dirty rows quarantined (reasons), proven by test.
- Lane 1 calibration extract produces a reconciled, lineage-stamped dataset from an imported batch.
- Lanes 2 & 3 present as typed fields + dormant hooks, NOT wired to trust/ledger — with tests asserting an import creates **no** trust change and **no** ledger movement on its own (the dormant-by-design proof).
- G-A…G-L all demonstrated. `-race` green, lint/gosec/vuln clean, float-ban covers the package.

## Sequencing note (owner decision)
This is a new workstream, not part of M4 (portals). Options: (i) slot it before M4, (ii) run it after M4, or (iii) interleave. Reviewer recommendation: **do the intake + mapping + simulator + lane-1 extract now** (it's the highest-leverage prep for the MTN conversation and unblocks model calibration the instant an extract arrives), and stage lanes 2/3 later — but the priority-vs-M4 call is the owner's.
