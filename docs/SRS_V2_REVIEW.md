# Review of Telco Digital Credit Platform SRS v2.0

**Reviewer:** Claude (Fable 5) · **Date:** 16 July 2026 · **Rev 1.2**
**Document reviewed:** `Telco_Digital_Credit_Platform_SRS_V2.docx` (30 sections + 2 appendices, ~60 numbered requirements)
**Review question:** Will this SRS stand up as the build baseline? What is missing, incorrect, or inconsistent?

> **Rev 1.2 note — final business model (owner decision).** The platform is built and operated **directly, as the Optasia replacement, under the company's own license**. All licensing-to-third-parties context is removed from this review. Consequences: **F-1 and F-2 are withdrawn** (the SRS's tenant = telco model is correct as written for direct operation). **F-3 and F-4 stand and are confirmed as the platform's obligations, not MTN's**: MTN suspended XtraTime in April 2026 precisely to avoid being classified as the lender under DEON, and a telco partnership contract will push all lending-side compliance onto the licensed partner. The division of labour is: **MTN owns the rails** (USSD/SMS delivery, data feeds, fulfilment, garnishment execution); **the platform owns the loan** (credit decisions, disclosure/consent records, complaints handling, bureau reporting, FCCPC registration and reporting, NDPA compliance).

---

## Verdict

**The SRS stands up as an institutional-quality specification for exactly what you intend to build — an Optasia-class, telco-facing credit platform operated under your own license.** The core tenancy model (tenant = telco), the systems-of-record boundaries, and the financial core are right for that mission. What the document still lacks is (a) **Nigeria-specific regulatory encoding** — including a direct conflict with the publicly mooted market-entry conditions (F-3, F-4), (b) depth in the **USSD channel and collections** behaviour (F-5, F-7), and (c) resolution of three internal inconsistencies. None of these are structural; all are addressable in a v2.1 pass.

Grade by area:

| Area | Assessment |
|---|---|
| Financial core (ledger, idempotency, states, reversals) | **Excellent** — best-in-class thinking, ship as-is |
| Edge-case coverage (§21) | **Excellent** — unusually complete |
| Config governance (§8, App. B) | **Excellent** |
| Multi-telco isolation (§7) | **Strong** |
| Tenancy model vs business model (direct operation, own license) | **Correct as written** |
| Regulatory (Nigeria/DEON specifics) | **Gap — partially conflicts with known entry conditions** |
| Channel layer (USSD/SMS realism) | **Underspecified** |
| Collections & concurrent-advance policy | **Underspecified** |
| Phasing / MVP definition | **Missing** |
| Internal consistency | 3 defects found (below) |

---

## 1. What is genuinely excellent (keep, do not dilute)

These choices are what separate this from a naive lending spec, and several directly encode lessons that kill platforms like this in production:

1. **Ledger-led with append-only entries and reversal-only corrections** (§13, LED-001/002). Deriving truth from the ledger rather than mutable balances is the single most important architectural decision in the document, and it's right.
2. **`FULFILMENT_UNKNOWN` as a first-class state with a no-blind-retry rule** (§12.1, ADV-004, §21 "telco timeout after credit"). Timeout-after-success is *the* classic double-credit bug in airtime advance systems. The SRS handles it correctly: status enquiry / reconciliation before any repeat instruction.
3. **CFG-004: every decision retains the exact configuration version used, with replay.** This makes decisions explainable to a regulator — which, given the FCCPC's posture, is not a nice-to-have.
4. **Anti-gaming design** (§10.3): medians/trimmed means, winsorisation, baseline-vs-spike comparison, one-tier-max movement per cycle. This is the real IP of the incumbent, and the SRS specifies it credibly.
5. **The §21 edge-case table.** MSISDN recycling, porting, out-of-order reversal-before-original, recovery-exceeds-outstanding, write-off-then-recovery, wrong-tenant-credentials-vs-payload — this table alone is worth more than most whole SRS documents. (See finding 5.3: it needs requirement IDs.)
6. **§24 migration from an incumbent with dual-running and cutover reconciliation.** This is exactly what displacing Optasia at a telco requires, and almost nobody specifies it upfront.
7. **Systems-of-record boundary table (§5).** Clean, correct, and the thing telco negotiations argue about most.
8. **§30 "decisions required before design freeze".** Honest about what is not yet decided. (Findings below add five more rows it needs.)

---

## 2. CRITICAL — gaps to resolve before design freeze

### F-1. Withdrawn (business model finalised: direct operation under own license)

Originally flagged the tenant = telco model as a mismatch for a license-to-third-parties strategy. With the final model — the platform operates directly as the lender of record — **the SRS's tenancy design is correct as written**. One residual note, consistent with the document's own configuration-first principle: keep the lending entity's legal identity (registered name, license references, complaint contacts, sender IDs, disclosure naming) as *configuration*, not hardcoded strings, since these appear in disclosures, statements, and regulatory exports and will change (entity renames, license renewals) without wanting a redeploy.

### F-2. Withdrawn (no third-party licensing to bill)

Only existed for the licensing GTM. §15.2's settlement parties (telco / platform / funder) are sufficient for direct operation.

### F-3. Credit bureau reporting is out-of-scope — this conflicts with the known market-entry conditions

**Evidence:** §3.2 "Credit bureau reporting unless enabled through a later approved integration."

**Why it's wrong (not just missing):** The publicly mooted conditions for new entrants in the post-Optasia market include **transparent credit data sharing with Nigerian credit bureaus**, alongside local data hosting and minimum Nigerian equity. And this obligation is **yours, not MTN's**: as the licensed lender of record, credit reporting attaches to your license — MTN's whole posture (including suspending XtraTime to avoid lender classification) is to keep lending-side obligations off itself. An entrant cannot defer the one capability regulators have already named as a condition of entry. DEON's framework also anticipates reporting obligations for digital lenders.

**Recommendation:** Move to in-scope for Release 1 as a *configurable, initially-dormant* capability: a bureau-export pipeline (CRC / FirstCentral / CreditRegistry formats), enableable per telco program, with the data mapping done even if submission starts later. This is cheap to build early (it reads the ledger you already have) and extremely expensive to retrofit under regulatory deadline.

### F-4. Nigeria-specific regulatory obligations are deferred too generically

**Evidence:** §26 defers everything to per-jurisdiction confirmation. That's the right *posture* for a multi-country platform, but the first market is known, the regulatory fight defining it is *in judgment right now* (DEON ruling reserved to 20 July 2026), and several obligations are already predictable.

**What should be named and encoded as configurable capabilities now:**

- **DEON/FCCPC:** registration data pack for the lending entity; conspicuous pre-acceptance disclosure of total cost of credit (PRD-002 covers content — bind it to a *retained, versioned disclosure acknowledgment record per advance*); complaint register with SLA tracking and FCCPC-format export; prohibition-style controls (e.g., harassment-free collections constraints on notification content/cadence). Note these are the **lender's** obligations — a telco partnership contract will assign them to you, not absorb them.
- **NDPA 2023 (not mentioned anywhere):** lawful-basis/consent records, data-subject rights workflows (access/correction/erasure within retention law), DPIA artifact, cross-border transfer controls — this last one interacts with your hosting choice (§30 infrastructure row) and the "local data hosting" entry condition. Data residency appears once as a tenant config field (§8); it should be a **hard Nigeria deployment constraint**, not a config value someone can leave unset.
- **NCC side:** the DEON dispute exists precisely because airtime advance straddles FCCPC (lending) and NCC (VAS). The SRS should support *both* classifications simultaneously — which, to its credit, the fee-structure flexibility in §30 already enables. Add: USSD shortcode approvals, SMS sender-ID registration, and Do-Not-Disturb (2442) compliance for promotional messaging as channel requirements.

**Recommendation:** Add a *Nigeria Regulatory Annex* with a requirement table (REG-00x), each row configurable per telco program, plus a watch-item note keyed to the 20 July judgment.

---

## 3. HIGH — significant gaps that will bite during build or launch

### F-5. The channel layer is hand-waved, but USSD *is* the product in this market

**Evidence:** §16.5 "Customer-facing channels may be USSD, SMS, telco app, web, IVR or API" — one sentence for the surface 90%+ of borrowers will use.

**What's missing:** USSD session mechanics (≈180-second session budget, mid-session timeout with money in flight — what happens when the session dies between "confirm" and response?), menu flow specification and versioning, session-state management, shortcode strategy (dedicated vs shared, who owns it — telco, platform, or aggregator), connectivity route (direct telco USSD gateway vs licensed VAS aggregator — an NCC-relevant commercial decision), SMS fallback for confirmation when the session drops, localisation (English/Pidgin/Hausa/Yoruba/Igbo), and per-session cost bearing (USSD sessions are charged; who pays changes unit economics).

The good news: the idempotency and offer-snapshot architecture already makes USSD-with-flaky-sessions safe. But "safe" isn't "specified" — the flows need their own section with requirement IDs.

### F-6. Multiple-concurrent-advance policy is never stated

**Evidence:** §14 allocation order ("oldest first…") *implies* multiple simultaneous advances per subscriber are possible; nothing says whether they're allowed, capped, or forbidden.

This is a first-order product and risk decision: incumbent airtime-advance products are overwhelmingly one-outstanding-advance-at-a-time (borrow again only after clearing). Multi-advance changes exposure math, allocation complexity, USSD UX, and disclosure. **Recommendation:** explicit configurable policy (`max_concurrent_advances`, default 1) + a §30 decision row.

### F-7. Recovery is specified; *collections* is not

**Evidence:** §14 handles the mechanics of garnishment events; §13 handles write-off accounting. Nothing specifies what happens across time when a subscriber simply never recharges: delinquency aging buckets (e.g., 7/30/60/90-day), reminder/dunning cadence and content constraints (interacts with DEON conduct rules and DND), configurable write-off trigger (age vs amount), and whether unrecovered exposure ever escalates beyond SMS (it usually shouldn't, in this product class — say so explicitly).

**Recommendation:** short *Delinquency and Collections* section (COL-00x), all config-driven; the reporting section (§25) already lists roll rates and write-offs, so the data model expects this — the behavior just isn't specified.

### F-8. Telco sandbox & certification environment is implied but not required

TST-002 requires adapter certification tests and §29 mentions a certification harness — but nothing requires a **standing sandbox with a telco simulator** where a telco integration team can certify against the platform, and where the platform itself can be developed and demonstrated against simulated telco behavior (including the nasty cases: timeout-after-success, duplicate events, reversal-before-original). This matters doubly for you: real telco API access will arrive *late* in a partnership negotiation, so the simulator is what lets the build proceed now — and a working end-to-end demo against a realistic simulator is your strongest artifact in the telco pitch itself. Make it a numbered requirement with the simulator's fault-injection catalogue drawn from §21.

### F-9. Availability/RTO/RPO numbers are internally inconsistent

**Evidence:** §18 — core decisioning targets **99.99%** availability *and* **RTO ≤ 30 minutes**.

99.99% permits ~52 minutes of downtime *per year*; a single DR event at the stated RTO consumes ~58% of the annual budget. Either the availability target is 99.9% (v1-realistic, still hard) with RTO 30 min, or you keep 99.99% and must specify near-instant automated failover (RTO in seconds-to-minutes) — which drives multi-region active-active cost. Also "RPO near zero through synchronous replication" is fine for the ledger but say explicitly which stores get sync replication vs async (sync-everything at 100M-subscriber scale is a cost decision someone must sign).

**Recommendation:** 99.9% / RTO 30 min / ledger RPO≈0 for Release 1, with 99.99% as a scale-phase target. Also state a degraded-mode SLO: "offers unavailable" is survivable; "recovery event ingestion lost" is not — the doc's backpressure row implies this priority ordering, make it explicit.

### F-10. Cross-tenant risk isolation is correct — but creates a serial-defaulter blind spot nobody owns

**Evidence:** §21 "Cross-telco coincidence: treat telco_id + effective period as distinct subscriber accounts"; §7 isolation throughout.

Strict isolation is right for privacy and tenant trust. Consequence: a subscriber who defaults on MTN borrows fresh on Airtel with a clean slate — and if the same platform serves both telcos, *your own engine* is the one being rotated through. Optasia's incumbency across multiple operators partially hides this problem today; a challenger winning telcos one at a time inherits it fully. Options: bureau-mediated visibility (ties into F-3), a privacy-preserving hashed negative-file check across telco tenants (with each telco's consent — this cuts against strict isolation and must be an explicit, contract-backed choice), or accept the risk. **This is a §30 decision row, currently absent** — and note NDPA constrains the hashed-consortium option.

### F-11. No MVP/phasing definition — "build-ready baseline" spans ten workstreams

§29 lists workstreams; §28 defines *final* acceptance. Nothing defines what Release 1 actually is. For your timeline (post-20-July window, telco partnership conversations needing a working demo), the SRS needs a phasing annex: e.g., R1 = one telco adapter (simulated) + airtime product + full financial core + admin config + sandbox; R2 = second telco + data products + settlement automation; R3 = scale/99.99% track. Without this, "build-ready" invites building everything and shipping nothing.

---

## 4. MEDIUM — defects and refinements

**F-12. `OFFERED` doesn't belong in the Advance state machine** (§12.1). An offer is its own entity with its own lifecycle (§22 has `Offer`); an advance should begin at `REQUESTED`. Cosmetic but it will confuse the state-machine implementation and its invariant tests.

**F-13. Configurable posting templates need a hard, numbered balance invariant.** LED-001 says "balanced according to configured accounting event templates" and Appendix B prose says configuration can't bypass ledger balancing — but the invariant deserves a non-configurable numbered requirement: *every posting template must produce debits = credits per currency, verified at config-validation time and at post time; a template that cannot balance must fail activation* (CFG-003 hook). Configurable accounting is powerful and dangerous in equal measure.

**F-14. Prose requirements lack IDs — traceability gap.** Whole load-bearing sections are unnumbered prose/bullets: §12.2 origination flow, §14 recovery bullets, §16 portal features, §19 security bullets, §20 observability, **§21 the entire edge-case table**, §24 onboarding steps. Your own acceptance-evidence discipline (every numbered req has one) stops where the numbering stops. Minimum fix: give §21 rows IDs (EDG-001…) and map each to a test-pack entry — that table is your best test plan and currently untrackable.

**F-15. Funding/treasury operations are thin.** "Funding cost" appears in scope (§3.1) and funder caps exist (SCR-005), but there's no `FUNDING_COST_ACCRUED` ledger event, no funding-pool drawdown/replenishment model, no funder statement content spec. If advances are telco-inventory-funded this is small; if external funders participate it's a module. Add a §30 decision row: *funding model per program* (telco inventory vs own balance sheet vs third-party funder).

**F-16. Portfolio-level automatic guardrails should be a numbered requirement.** §21 "mass configuration error" mentions "automatic guardrails" in passing. Elevate: configurable circuit breakers that automatically suspend originations per program when approval-rate, average-limit, or early-delinquency metrics deviate ≥ X% from baseline (maker-checker to re-arm). This is the control that saves you from the worst class of incident this platform can have (config error → over-approval at scale → unrecoverable exposure in hours).

**F-17. Subscriber self-service rights are missing.** Opt-out of offers/marketing (distinct from DND), self-exclusion from borrowing, and NDPA data-subject request workflows. Small surface, regulator-visible.

**F-18. Notification requirements have config but no requirement table.** Delivery-receipt handling, retry policy (notification failure edge exists in §21 — good), quiet hours, language selection, per-program sender identity. Two or three NOT-00x rows suffice.

**F-19. No cost/unit-economics NFR.** At 100M profiles and daily scoring, cost-per-decision and cost-per-advance are competitive weapons (they set the floor of the revenue share you can accept in a telco negotiation). Add an observability requirement: cost attribution per program (compute, USSD sessions, SMS) surfaced in capacity dashboards (NFR-005 hook).

**F-20. Tax handling is generic.** Fine for an SRS, but Nigeria specifics (VAT on service fees, WHT on revenue shares between parties) should be named in the Nigeria annex so the settlement engine's tax lines are designed, not bolted on.

---

## 5. Additions needed to §30 (Decisions Before Design Freeze)

The existing ten rows are good. Add:

| # | Decision | Why |
|---|---|---|
| 11 | **Lender-vs-telco responsibility matrix** — which DEON/NDPA obligations sit with the platform (as licensed lender) vs MTN (as channel): disclosure delivery, consent capture, complaints intake and SLA, bureau reporting, marketing rules | F-3/F-4; also the key liability schedule in the telco contract |
| 12 | **Cross-telco default visibility** — bureau-mediated, hashed negative-file with telco consent, or none? | F-10; NDPA constraints |
| 13 | **Max concurrent advances per subscriber** (default 1?) | F-6 |
| 14 | **USSD route** — direct telco gateway vs licensed aggregator; shortcode ownership | F-5; NCC licensing |
| 15 | **Funding model per program** — telco inventory, own balance sheet, third-party funder | F-15 |

---

## 6. Consistency check results

| Check | Result |
|---|---|
| §3.2 credit-bureau exclusion vs known entry conditions | **Conflict** (F-3) |
| 99.99% availability vs RTO ≤ 30 min | **Inconsistent** (F-9) |
| `OFFERED` in Advance FSM vs `Offer` as separate entity (§22) | **Inconsistent** (F-12) |
| §14 multi-advance allocation vs unstated concurrency policy | **Ambiguous** (F-6) |
| Tenant = telco vs chosen business model (direct Optasia replacement, own license) | **Correct as written** |
| Ledger events (§13) vs scope items (§3.1 "funding cost") | Missing event type (F-15) |
| Everything else cross-checked (states vs edge cases, config domains vs portals, entities vs requirements) | **Consistent** — notably clean |

---

## 7. Recommended path to v2.1

1. Add the three new sections: Nigeria Regulatory Annex (F-3/F-4/F-20), Channels & USSD (F-5), Delinquency & Collections (F-7). Move credit-bureau export into Release-1 scope (dormant until enabled).
2. Fix the three inconsistencies (F-9, F-12, F-13) and state the concurrency policy (F-6).
3. Number the prose (F-14) — at minimum §21 → EDG-00x.
4. Add the phasing annex (F-11), the telco-simulator sandbox requirement (F-8), and the five new §30 rows.
5. Hold v2.1 until after the **20 July DEON judgment** — it may settle §30 row 10 (regulatory controls) and F-4's annex content within days.

With those changes, this document is a genuinely build-ready baseline — and notably stronger than what most funded competitors will be working from.
