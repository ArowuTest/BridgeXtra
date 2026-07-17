# Review of Telco Digital Credit Platform v3.0 (Volumes 1–3) — Comparison vs SRS v2.0

**Reviewer:** Claude (Fable 5) · **Date:** 17 July 2026
**Documents reviewed (full read):**
- Volume 1 — Enterprise Business Architecture (1,608 lines, ~200 requirements ENT/BUS/CFG/PRD/CHN/SOR/TEN/CRD/ADV/COL/TRE/LED/FIN/REG/DAT/RSK/GOV/TEL/RES/REP/SEC/MIG/REL + EDG-001..040 + DD-01..30)
- Volume 2 — Technical Architecture & Build Specification (1,664 lines, 396 numbered technical requirements + canonical contracts, schemas, invariants INV-001..012)
- Volume 3 — Operations, Assurance & Delivery (1,459 lines, ~380 operational requirements + RACI, runbooks, control calendar, KPI dictionary, go-live gate)

**Question asked:** Is v3 better than v2.0, or the reverse?

---

## Verdict

**V3 is better — and not marginally. Adopt it as the authoritative baseline and retire v2.0 to document history.** This is not an edit of v2; it is a re-architecture of the specification itself: three volumes with clear precedence (business intent → technical build → operations/delivery), a uniform requirement convention (ID + MUST/SHOULD + release tag + acceptance evidence), and — critically — **every one of the 18 active findings from my v2 review (F-3…F-20) is genuinely closed**, not just name-checked. I verified each disposition in the closure matrix (Vol 1 §27) against the actual requirement text; the table below shows the spot-check results.

V2.0 was an excellent single document with structural gaps. V3 is an institutional programme baseline that a bank, a telco partner, or a regulator could be shown without embarrassment. The remaining defects are mechanical and editorial — one of them (requirement-ID collisions across volumes) should be fixed before you import requirements into any backlog/traceability tool, because the volumes themselves mandate ID-level traceability.

---

## 1. Review-finding closure verification (F-3…F-20)

I checked every finding against the actual v3 text, not just the closure matrix's claims:

| Finding (v2 review) | v3 disposition | Verified? |
|---|---|---|
| F-3 bureau reporting out-of-scope | REG-006/007 (V1), BUR-001..006 (V2), §23 (V3): R1 build-capable, dormant until enabled, with reconciliation to ledger and rejection workflow | ✅ Closed — exactly as recommended |
| F-4 Nigeria regulatory too generic | V1 §16 full Nigeria annex: FCCPC/DEON, NDPA named, NCC/VAS, tax, complaints, automated-decision rights; §30 source register incl. the 20 July hearing as a formal watch item (REG-012) | ✅ Closed, with proper legal-status caution |
| F-5 USSD underspecified | V1 §10 (session model, timeout table, languages, cost bearing); V2 §14 (canonical flow, session architecture, CHN-001..016); TST-013 session-expiry-at-every-step tests | ✅ Closed — now among the strongest sections |
| F-6 concurrent advances unstated | PRD-005 / ADV-007: default one, atomic evaluation, higher values need programme approval; DD-09 decision row | ✅ Closed |
| F-7 collections missing | V1 §13 (delinquency stages + conduct), COL-001..012; V2 §16; V3 §18 operations | ✅ Closed, incl. explicit no-harassment/no-field-collections defaults |
| F-8 simulator not required | TEL-002 standing simulator R0/R1; V2 §27 SIM-001..012 (deterministic seeded faults, signed evidence packs); V3 §30 certification | ✅ Closed — elevated to "build-before-access" strategic capability |
| F-9 99.99% vs RTO 30min inconsistent | V1 §21 / V2 §24.1 / V3 §28: 99.9% R1, RTO ≤30m, ledger RPO≈0; 99.99% gated on error-budget-consistent failover (RES-009, NFR-002) | ✅ Closed — precisely the recommended resolution |
| F-10 serial-defaulter blind spot | SOR-007 (disabled until decided), RSK-009, DD-12 formal decision | ✅ Closed as explicit decision |
| F-11 no MVP/phasing | V1 §25 R0–R4 with exit criteria; REL-001 one-complete-programme discipline; V3 DLV-003 walking skeleton, DLV-005 R1 scope | ✅ Closed |
| F-12 OFFERED in advance FSM | V1 §12.2 explicitly removes it ("intentionally excluded… resolves the inconsistency"); V2 OFR-001 separate offer lifecycle | ✅ Closed |
| F-13 ledger balance invariant | LED-003 (V1): unbalanced template fails closed, non-configurable; V2 LED-001/002, CFG-012 | ✅ Closed |
| F-14 prose not numbered | Everything now carries IDs; edge cases became EDG-001..040 (expanded from ~40 unnumbered rows) | ✅ Closed (but see Defect 1 — ID collisions) |
| F-15 treasury thin | V1 §14 funding pools/models, TRE-001..010; FUNDING_COST_ACCRUED ledger event (V2 §17.2); V3 §22 | ✅ Closed |
| F-16 portfolio guardrails | CRD-013/014 numbered; V2 §19.1 automatic actions; V3 §15 + RB-011 runbook + re-arm authority | ✅ Closed |
| F-17 self-service rights | CHN-009/010 (opt-out, self-exclusion), V1 §17 DSR workflows, SUB-009, V3 §24 | ✅ Closed |
| F-18 notification requirements | CHN-006..010 (V1) + NOT-001..010 (V2): DND, quiet hours, sender identity, delivery evidence | ✅ Closed |
| F-19 unit-economics NFR | PRD-009, FIN-010, REP-004, SCL-012, OBS-011, INF-015, KPI "cost per advance" | ✅ Closed |
| F-20 tax generic | FIN-006, REG-015, DD-04 with adviser confirmation gate | ✅ Closed |
| F-1 residual (lender identity as config) | BUS-003: legal identity, licence refs, contacts, sender identity configurable per programme, effective-dated; §5 Legal Entity / Programme model | ✅ Closed — the §5 Programme object is an elegant light-weight version that preserves optionality without over-engineering |

**18/18 verified closed.** The closure matrix's claims are accurate — a rarity.

## 2. What v3 adds beyond closing findings

1. **The three-volume separation with precedence rules** (SCP-002: conflicts escalate, operations may not locally reinterpret financial/regulatory requirements). This makes the baseline maintainable by different teams without divergence.
2. **Non-configurable invariant register** (V2 Appendix B, INV-001..012) — the "configuration-first but not configuration-unbounded" governing principle made testable. This directly encodes the zero-config-floor lesson: fail closed for new credit, fail durable for recoveries.
3. **Canonical contracts and data discipline**: minor-unit money (API-005 — no floats in accounting), UTC + separately preserved telco event time (API-006), event envelope with causation/correlation, partition-key table per event, error families with retry semantics, and the Error/Retry/Ambiguity matrix (V2 Appendix D) — the timeout-after-success discipline is now mechanically specified end to end.
4. **The decision-layers model** (V1 §11.1: data readiness → eligibility → affordability → trust → fraud → portfolio → offer construction) with worked anti-gaming examples (the ₦2,000-baseline / ₦20,000-spike table). This is the credit IP of the platform, specified explainably.
5. **Design decisions expanded 10 → 30** (DD-01..30), each with owner — the design-freeze governance is now real.
6. **Operations as a first-class product** (all of Volume 3): daily control calendar, 25-runbook catalogue, 20-scenario major-incident catalogue, KPI dictionary with formulas, go-live gate with named approvers, evidence/traceability matrix. v2.0 had essentially none of this.
7. **The walking-skeleton mandate** (DLV-003: prove offer→fulfilment→recovery→ledger→reconciliation on the simulator first) — the single best build-sequencing decision in the document set.

## 3. Defects found (fix in v3.1 — none block design start)

**D-1. Requirement-ID collisions across volumes (the one that matters).** Vol 1, Vol 2 and Vol 3 each independently define `CFG-001`, `SEC-001`, `COL-001`, `REG-001`, `TEN-001`, `TRE-001`, `RES-001`, `MIG-001`, `GOV-001`, `DAT-001`, `RSK-001`… with *different text*. Example: V1 CFG-001 is "portal shall manage configuration without code deployment"; V2 CFG-001 is "configuration shall support Draft/Submitted/… states". The volumes themselves mandate ID-level traceability (V2 TST-001, V3 QAO-004, DLV-001), and this breaks the moment requirements are imported into one tool. **Fix:** prefix by volume (`V1-CFG-001` / `V2-CFG-001` / `V3-CFG-001`) or re-key one register — a find/replace exercise, but do it *before* the backlog import, not after.

**D-2. Advance state models diverge slightly between volumes.** V1 §12.2 includes `DELINQUENT` and `SETTLED` as advance FSM states; V2 §13.1 uses `CLOSED` (no `SETTLED`) and correctly treats delinquency as a separate classification dimension (§16.2) rather than an FSM state. V2's model is the better one — delinquency is an aging overlay, not a lifecycle transition — so align V1 to V2, and pick one terminal name (`CLOSED` or `SETTLED`).

**D-3. V1 MIG numbering starts at MIG-002** — MIG-001 is missing from Volume 1 (§24). Editorial.

**D-4. Figures are hosted on an external CDN** (`static-us-img.skywork.ai`). A confidential controlled document must not depend on — or leak its existence to — a third-party image host. Embed the figures in the source documents.

**D-5. Silent latency-target change.** v2.0 said offer p95 ≤150 ms; V2 SCL-002 says p95 ≤300 ms / p99 750 ms. The new number is the more realistic one and consistent with the 99.9% posture — but a version-history note should record the change so nobody later "restores" the old target thinking it was lost.

**D-6. Editorial nits:** V2 UI-005 "pagnination"; V1 exported list numbering runs a global counter across sections (items 10–41 continuing through §15→§24 — an export artifact); V1 table-of-contents table is duplicated; occasional split tables repeat header rows.

## 4. One strategic caution (not a defect — a proportionality risk)

Across three volumes there are roughly **900+ MUST requirements tagged R1**. Volume 3 in particular describes a bank-grade operating organisation: 16x7 command centre, quarterly toxic-access reviews, annual crisis simulations with telco participation, 25 runbooks, multi-committee governance. All of it is *correct* for the at-scale platform — but Release 1 will be delivered by a small team, and the go-live gate (GLV-001..012, ORR-001..015) as written is unpassable by a lean pilot organisation without waivers. The documents gesture at this ("Release 1 may combine functions", "operating model shall support a single-telco Release 1") but every requirement is still individually release-gating.

**Recommendation:** add a short **R1 Proportionality Annex** to Volume 3: for each operational MUST, state the lean-team acceptable evidence level for a capped pilot (e.g., command centre = one on-call rotation + dashboards; crisis simulation = tabletop; quarterly reviews = monthly single-page). Without this, the gate will be bypassed informally under launch pressure — which V3A-004 itself prohibits ("prevented from becoming undocumented defaults"). Make the proportionality explicit and approved rather than improvised.

## 5. Recommendation

1. **Adopt v3 Volumes 1–3 as the authoritative baseline.** Mark SRS v2.0 superseded (keep for history; the v2 review Rev 1.2 stands as the audit trail of how v3 got here).
2. Fix D-1 (ID prefixes) before importing requirements into any tracking tool; fold D-2..D-6 into a v3.1 editorial pass.
3. Add the R1 Proportionality Annex (§4 above).
4. Next build-side artifacts, in order: **ADR-0001 technology stack** (the volumes correctly defer this — it is now the first blocking decision), the **requirement import + traceability skeleton**, then the **walking skeleton against the simulator** per DLV-003.
5. Decision register: DD-01 (legal entity), DD-02 (first telco), DD-03 (funding), DD-05 (USSD route) and DD-14 (post-20-July regulatory baseline) are the five that gate everything else — the rest can be resolved in parallel with early build.

**Bottom line:** v2.0 was a strong SRS with structural gaps. v3 is a build-and-operate baseline of genuinely institutional quality — better in structure, coverage, consistency, testability and operational realism. It closed every finding from the external review verifiably. Fix the ID collisions, align the two state models, add the proportionality annex, and this document set is ready to govern the build.
