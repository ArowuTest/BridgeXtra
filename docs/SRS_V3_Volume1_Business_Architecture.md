# TELCO DIGITAL CREDIT PLATFORM

## Enterprise Blueprint & Business Architecture

Volume 1 of 3 | Version 3.0

![image](https://static-us-img.skywork.ai/prod/nexus/1784233125/cropped_image_4_1784233125993947048.jpg)

A configuration-first, multi-telco digital credit operating model for airtime, data and future embedded-credit products.

Status: Enterprise baseline for design, commercial alignment and regulatory review Date: 16 July 2026 Classification: Confidential

## Document Control

| Field | Value |
| --- | --- |
| Document title | Telco Digital Credit Platform Enterprise Blueprint & Business Architecture |
| Document identifier | TDCP-EBBA-V3.0-VOL1 |
| Version | 3.0 |
| Date | 16 July 2026 |
| Status | Baseline for enterprise/design review |
| Primary audience | Board, product, risk, compliance, legal, finance, architecture, engineering, operations, telco and funding partners |
| Related documents | SRS v2.0; external review Rev 1.2; future Volume 2 Technical Architecture & SRS; future Volume 3 Operations, Delivery & Assurance |
| Confidentiality | Confidential - controlled circulation |

| Version | Date | Summary |
| --- | --- | --- |
| 2.0 | 16 Jul 2026 | Multi-telco SRS with configuration governance, decisioning, ledger, reconciliation and edge cases. |
| Review 1.2 | 16 Jul 2026 | External review validated the core and identified Nigeria regulatory, USSD, collections, simulator, phasing and consistency gaps. |
| 3.0 Volume 1 | 16 Jul 2026 | Reframed as an enterprise blueprint; incorporates review findings F-3 to F-20 at the business and enterprise-architecture level. |

## Document hierarchy

This volume is authoritative for business intent, enterprise capabilities, ownership boundaries, regulatory posture, product policy, operating model and design-freeze decisions. Where a technical implementation choice conflicts with this volume, the conflict must be raised through architecture governance rather than silently resolved in code.

## How to Use This Document

-   Board and executives: use Sections 1-5, 24 and 29 to approve the business model, risk appetite, investment sequence and unresolved decisions.
    
-   Product, risk and compliance: use Sections 6-18 and the binding requirements to define products, policies, disclosures, controls and operating procedures.
    
-   Architects and engineering: treat all MUST requirements, system-of-record boundaries, non-configurable invariants and tenant-isolation rules as constraints for Volume 2.
    
-   Telco and funding partners: use Sections 7-9, 15, 20 and 21 to agree responsibilities, data contracts, funding, settlement, service levels and certification.
    
-   QA and assurance: use Appendix A and the requirement-to-evidence matrix as the starting point for traceability and acceptance packs.
    

## Table of Contents

| Section | Title |
| --- | --- |

| Section | Title |
| --- | --- |
| 1 | Executive Summary |
| 2 | Purpose, Scope and Intended Decisions |
| 3 | Strategic Context and Value Proposition |
| 4 | Enterprise Architecture and Product Principles |
| 5 | Target Business, Legal-Entity and Programme Model |
| 6 | Product Strategy and Configurable Catalogue |
| 7 | Systems of Record, Identity and Data Ownership |
| 8 | Multi-Telco Operating and Tenancy Model |
| 9 | Enterprise Capability Model |
| 10 | Customer Experience and Channel Architecture |
| 11 | Credit Decisioning, Scoring and Anti-Gaming |
| 12 | Offer, Advance and Fulfilment Lifecycle |
| 13 | Recovery, Delinquency and Collections |
| 14 | Treasury, Funding and Exposure Management |
| 15 | Financial Architecture, Economics, Reconciliation and Settlement |
| 16 | Nigeria Regulatory and Conduct Architecture |
| 17 | Data Governance, Privacy and Automated-Decision Rights |
| 18 | Enterprise Risk Architecture |
| 19 | Target Operating Model and Governance |
| 20 | Telco Onboarding, Integration Certification and Simulator |
| 21 | Service Management, Resilience and Degraded Modes |
| 22 | Reporting, Analytics and Unit Economics |
| 23 | Security, Access and Third-Party Assurance |
| 24 | Incumbent Migration and Cutover Strategy |
| 25 | Release and Capability Roadmap |
| 26 | Decisions Required Before Detailed Design Freeze |
| 27 | External Review Closure Matrix |
| 28 | Requirement Summary and Priority Convention |
| 29 | Assumptions, Dependencies and Constraints |
| 30 | Regulatory Source and Watch Register |
| 31 | Glossary |
| 32 | Enterprise Baseline Conclusion |
| Appendix A | Numbered Edge-Case Catalogue |

| Section | Title |
| --- | --- |
| Appendix B | Enterprise Acceptance Evidence |
| Appendix C | Administration Configuration Catalogue |

## 1\. Executive Summary

The Telco Digital Credit Platform is intended to operate as the licensed credit and technology layer behind telco-distributed airtime, data and related embedded-credit products. It is designed to replace an incumbent telco-credit provider without forcing the telco to become the lender of record or to rebuild the credit, ledger, recovery, regulatory and portfolio capabilities internally. The platform will initially serve Nigeria and must be capable of supporting multiple Nigerian mobile network operators and MVNOs, while retaining a path to additional jurisdictions and products.

The platform owns the credit relationship: eligibility, score and limit determination, offer generation, disclosure and consent evidence, advance records, exposure, financial ledger, recovery allocation, delinquency, complaints, bureau reporting capability, regulatory exports, reconciliation and settlement calculations. Each telco owns the network rails and telco source data: subscriber status, NIN/KYC verification indicators, network and recharge behaviour, USSD/SMS or app channels, airtime/data fulfilment, top-up interception or garnishment execution, and the authoritative network balance.

The architecture is deliberately configuration-first. Telco-specific products, denominations, rules, score weights, limits, fees, revenue shares, funding pools, settlement calendars, languages, messages, thresholds, feature flags and workflow approvals must be administered through governed configuration rather than embedded in code. Configuration power is constrained by non-configurable controls: tenant isolation, append-only financial records, double-entry balancing, idempotency, evidence retention, maker-checker approval for material changes, security baselines and prohibitions that the applicable law does not allow to be waived.

### Core Proposition

The telco provides reach, channel, data and fulfilment. The platform provides the regulated credit brain, financial truth, risk control and operating discipline. Neither party should duplicate the other party’s system of record.

| Outcome | Enterprise objective | Evidence of success |
| --- | --- | --- |
| Customer continuity | A customer obtains an appropriate offer and receives airtime/data through a short, comprehensible journey even when the telco session is unreliable. | Low median decision latency; low abandonment; confirmation SMS for dropped sessions; complaint trend within appetite. |
| Telco value | Increase usage, recharge frequency, customer retention and incremental service-fee revenue without transferring uncontrolled lending risk to the telco. | Incremental usage/recharge analysis, revenue share, controlled exposure and independent reconciliation. |
| Risk sustainability | Grow limits through stable behaviour and earned trust rather than isolated recharge spikes. | Vintage loss, roll-rate, cure, exposure and approval-rate metrics by telco/product/score band. |
| Regulatory defensibility | Demonstrate consent, total-cost disclosure, fair conduct, decision explainability, complaints handling, privacy and accurate reporting. | Versioned evidence pack reproducible per advance and regulator-ready exports. |
| Operational scale | Support tens of millions of subscriber profiles and high peak traffic without cross-telco contagion. | Capacity tests, tenant-specific SLOs, fault isolation, recovery-event durability and cost attribution. |

ENT-001 \[MUST; R1\] The platform shall operate as a telco-facing digital credit platform in which the platform is the lender/credit operator of record for each enabled programme unless an approved programme contract explicitly defines a different regulated entity.

ENT-002 \[MUST; R1\] The platform shall support multiple telcos, products, funding models and jurisdictions through tenant-aware configuration and adapter-based integration.

ENT-003 \[MUST; R1\] The platform shall maintain an auditable, replayable and financially balanced record of every offer, decision, advance, fulfilment outcome, recovery, reversal, adjustment, write-off and post-write-off recovery.

ENT-004 \[MUST; R1\] No telco outage, malformed feed, configuration error or capacity surge shall be permitted to corrupt the financial ledger or expose another telco tenant’s data.

ENT-005 \[MUST; R1\] Release planning shall prioritise a complete, controllable end-to-end product over simultaneous delivery of all future products and telcos.

## 2\. Purpose, Scope and Intended Decisions

### 2.1 Purpose

This document establishes the enterprise baseline from which product design, legal agreements, technical architecture, implementation backlogs, operating procedures, regulatory packs and commercial negotiations shall be derived. It is intentionally more prescriptive than a vision document and less implementation-specific than an API specification. Its job is to prevent business-critical decisions from being rediscovered inconsistently by separate delivery teams.

### 2.2 In-Scope Enterprise Capabilities

-   Multi-telco tenant model, legal-entity and programme configuration, operator adapters and contractual responsibility boundaries.
    
-   Airtime advance as the Release 1 launch product, with data and bundle advances designed into the product framework.
    
-   Credit policy, eligibility, scoring, affordability, progressive trust, anti-gaming, fraud controls and portfolio guardrails.
    
-   Customer channel architecture including USSD, SMS, telco app/API and customer-support-assisted journeys.
    
-   Offer, disclosure, consent, advance, fulfilment, recovery, delinquency, write-off, complaints and bureau-reporting lifecycles.
    
-   Funding pools, exposure, immutable ledger, reconciliation, settlement, tax-line support and management reporting.
    
-   Nigeria regulatory and privacy capabilities, including retained evidence and explicit watch items where law or litigation is unsettled.
    
-   Telco onboarding, simulator/certification, migration from an incumbent, service management, phasing and design-freeze governance.
    

### 2.3 Out of Scope for This Volume

-   Final technology selections, physical deployment topology, detailed data schemas, API payloads, event schemas and code-level service boundaries; these belong in Volume 2.
    
-   Detailed runbooks, staffing rosters, test scripts, cutover plans and operational acceptance procedures; these belong in Volume 3.
    
-   Legal advice or a final statement of regulatory status. The document encodes capabilities and controls, while qualified Nigerian counsel and regulators determine applicability.
    
-   Unapproved assumptions about the commercial terms, fee level, revenue split, lender licence, shortcode ownership or funding source for a particular telco programme.
    

BUS-001 \[MUST; R0\] Every downstream product requirement, architecture decision and implementation epic shall trace to an enterprise requirement, an approved design-freeze decision or a formally recorded assumption.

BUS-002 \[MUST; R0\] Out-of-scope items shall not be treated as implicitly excluded from future releases; they shall be recorded in the roadmap or decision register with an owner and trigger for reconsideration.

## 3\. Strategic Context and Value Proposition

### 3.1 Market Problem

Prepaid subscribers periodically exhaust airtime or data before they are able or willing to recharge. A telco can preserve connectivity and revenue by advancing a controlled amount and recovering it from a later top-up. At scale, however, this is not a simple balance transfer. It is a high-frequency credit operation requiring reliable behavioural data, dynamic eligibility, clear customer terms, fast channel response, precise fulfilment state management, automated recovery, accounting, consumer protection, complaints management and portfolio controls.

The platform’s differentiator is therefore not merely a scoring formula. Sustainable advantage comes from the combined operating system: high-quality telco features; robust anti-gaming; progressive trust; safe distributed transactions; recovery and reconciliation discipline; configurable telco programmes; explainable decisions; and a low-cost, regulator-defensible operating model.

### 3.2 Value by Stakeholder

| Stakeholder | Value delivered | Non-negotiable concern |
| --- | --- | --- |
| Subscriber | Emergency connectivity, understandable cost, fast fulfilment, fair treatment and accessible complaint channels. | No hidden cost, no unsolicited advance, no duplicate credit, no harassment. |
| Telco | Usage continuity, ARPU/recharge uplift, partner income, reduced build burden and controlled reputational exposure. | Service reliability, customer experience, data protection, accurate settlement and clear liability. |
| Platform company | Fee/revenue-share income, reusable decisioning IP, multi-telco scale, product expansion and portfolio data. | Licence/compliance, funding sufficiency, loss control, operational resilience and partner dependency. |
| Funder / treasury | Short-duration granular exposure with automated recovery and transparent portfolio reporting. | Ring-fenced limits, eligibility discipline, funding statements and loss visibility. |
| Regulator / bureau | Transparent lending conduct, privacy, complaints, accurate reporting and evidence. | Named accountable entity, retained consent/disclosure, timely records and remediation. |
| Auditor / finance | Complete transaction lineage from customer request to GL/settlement. | Balanced entries, immutable history, controlled adjustments and reconciled cash/stock movements. |

### 3.3 Competitive Positioning

-   Operator-neutral core: onboarding a second telco is an adapter/configuration exercise, not a rewrite of MTN-specific logic.
    
-   Configuration governance: risk and commercial changes can be deployed safely with simulation, approval, effective dating, canary scope and rollback.
    
-   Explainability by construction: every outcome retains feature values, rule/model versions, disclosure version, offer snapshot and reason codes.
    
-   Financial integrity: append-only, balanced ledger and reconciliation-first handling of ambiguous telco outcomes.
    
-   Build-before-access capability: a standing telco simulator enables delivery, demos and certification before production telco connectivity is available.
    
-   Regulatory portability: Nigeria-specific controls coexist with a jurisdictional policy layer rather than contaminating the global core.
    

## 4\. Enterprise Architecture and Product Principles

| Principle | Interpretation |
| --- | --- |
| Configuration first, not configuration everywhere | Commercial, product, risk and operational variability shall be configurable. Security, tenant isolation, idempotency, ledger balancing and evidence integrity remain code-enforced invariants. |
| Canonical core, telco-specific edge | The core sees canonical subscriber, offer, advance, fulfilment and recovery contracts. Telco adapters translate operator-specific protocols, codes and files. |
| Ledger-led financial truth | Mutable summary balances are performance views. Financial truth is reconstructed from append-only entries and linked reversals. |
| Offer is not an advance | An offer has a validity window and disclosed terms. An advance starts only after a request/acceptance event and follows its own state machine. |
| Precompute, then protect in real time | Scheduled scoring creates low-latency offers. Real-time overlays revoke or reduce offers when safety-critical events such as SIM swap, barring, fraud, exposure or stale-data conditions occur. |
| At-least-once integration, exactly-once economic effect | Messages may be duplicated or replayed; idempotency keys, uniqueness constraints and ledger references prevent duplicate economic outcomes. |

| Principle | Interpretation |
| --- | --- |
| Fail closed for new credit, fail durable for recoveries | When material uncertainty exists, new originations stop or return an unavailable response. Recovery and reversal events are durably accepted, queued and replayed. |
| Tenant isolation with deliberate shared-risk choices | Cross-telco data is isolated by default. Any negative-file or cross-telco risk capability requires a lawful, contract-backed design decision. |
| Evidence is a product feature | Consent, disclosure, decisions, complaints, configuration and data lineage are stored in retrievable evidence packs rather than reconstructed after an incident. |
| Progressive delivery | Release 1 contains complete financial, risk, regulatory and operational controls for one product/telco, not superficial support for all future products. |

CFG-001 \[MUST; R1\] The administration portal shall manage telco, programme, product, fee, denomination, score, eligibility, tier, funding, recovery, settlement, notification and feature-flag configuration without code deployment.

CFG-002 \[MUST; R1\] Every configuration object shall have immutable version, status, owner, scope, effective-from/effective-to timestamps, change reason and approver evidence.

CFG-003 \[MUST; R1\] Material configuration changes shall require maker-checker approval; a maker may not approve the same change.

CFG-004 \[MUST; R1\] The platform shall validate dependency, range, type, balancing, completeness and conflict rules before a configuration version can be activated.

CFG-005 \[MUST; R1\] Risk-impacting configuration shall support historical back-test or simulation against a representative data sample before approval.

CFG-006 \[MUST; R1\] Configuration shall support preview, canary activation by telco/product/segment, progressive rollout, automatic guardrail suspension and rollback to a prior version.

CFG-007 \[MUST; R1\] Every decision and financial event shall persist the exact configuration and model/rule versions applied.

CFG-008 \[MUST; R1\] Secrets, private keys and passwords shall not be stored as normal portal configuration; they shall be referenced from an approved secrets-management service.

CFG-009 \[MUST; R1\] The platform shall prevent configuration from disabling statutory evidence, tenant isolation, ledger balancing, audit logging, idempotency or mandatory security controls.

-   CFG-010 \[MUST; R1\] Configuration export/import shall be signed, access-controlled, environment-aware and incapable of silently moving production secrets or tenant data.

## 5\. Target Business, Legal-Entity and Programme Model

The default business model is direct operation by the platform company under its own applicable authorisations, replacing the incumbent provider. The telco supplies rails and source data but does not become the owner of the advance record merely because it performs fulfilment or recovery. This distinction must be reflected consistently in contracts, disclosures, complaint routing, regulatory reports, accounting and customer statements.

### 5.1 Core Enterprise Objects

| Object | Definition | Key relationships |
| --- | --- | --- |
| Legal Entity | Regulated or contracting company that owns programme obligations and customer disclosures. | May operate many telco programmes; has licence, tax, complaint and privacy attributes. |
| Telco Tenant | Operator or MVNO whose subscriber base, data, channels and fulfilment rails are isolated in the platform. | Owns programmes, adapters, users, credentials, products and settlement arrangements. |
| Programme | Commercial/regulatory arrangement between legal entity, telco and optional funder for a market/product set. | Binds jurisdiction, product, fees, rules, funding, recovery, settlement and responsibilities. |

| Object | Definition | Key relationships |
| --- | --- | --- |
| Product | Reusable credit template such as airtime advance or data advance. | Configured per programme with denominations, terms, limit mapping and fulfilment type. |
| Funding Pool | Ring-fenced source of exposure capacity. | Assigned to programme/product; has cap, utilisation, replenishment and suspension rules. |
| Subscriber Account | Telco-scoped identity used for scoring and exposure. | Keyed by telco\_id plus durable internal subscriber account, not MSISDN alone. |
| Advance | Accepted and approved credit contract for a specific product and offer snapshot. | Linked to disclosure, consent, fulfilment, ledger, recoveries and complaints. |

### 5.2 Responsibility Matrix

| Capability | Platform accountable | Telco accountable | Shared / contract-defined |
| --- | --- | --- | --- |
| Subscriber status and network balances |  | ✓ |  |
| NIN/SIM-registration verification source flag |  | ✓ | Interpretation and use are platform responsibilities. |
| Behavioural/recharge source data |  | ✓ | Data quality, cadence and correction SLAs are shared. |
| Eligibility, score, offer and limit | ✓ |  |  |
| Disclosure content and retained acceptance | ✓ |  | Telco channel must faithfully render/deliver. |
| Advance record and exposure | ✓ |  |  |
| Airtime/data fulfilment instruction |  | ✓ | Platform orchestrates and reconciles outcome. |
| Recharge interception/garnishment execution |  | ✓ | Platform allocates and records recovery. |
| Complaints policy, register and regulatory SLA | ✓ |  | Telco may provide first-line intake under agreed routing. |
| Bureau/regulatory reporting | ✓ |  | Telco supplies required source confirmation where contractually agreed. |
| Ledger and settlement calculations | ✓ |  | Both exchange reports and resolve exceptions. |
| Customer messaging | ✓ | ✓ | Sender ID, route, DND and cost bearing contract-defined. |

BUS-003 \[MUST; R1\] The legal identity, licence references, privacy contact, complaints contact, disclosure name, sender identity and regulatory-reporting identity shall be configurable per programme and effective-dated.

BUS-004 \[MUST; R0\] A programme responsibility matrix shall be approved before production onboarding and shall map each customer, regulatory, data, financial and incident obligation to one accountable party.

BUS-005 \[MUST; R1\] The platform shall not infer that the telco is lender of record solely because the telco credits airtime/data or deducts repayment from a later recharge.

BUS-006 \[MUST; R1\] Commercial agreements shall define ownership and permitted use of derived scores, model features, portfolio data, customer communications and post-termination records.

BUS-007 \[MUST; R0\] Each programme shall identify the funding model, economic principal, inventory/cash settlement method, loss bearer and tax treatment before design freeze.

## 6\. Product Strategy and Configurable Catalogue

The core shall be designed as a digital-credit platform, but the launch scope shall remain disciplined. Airtime advance is the Release 1 product because its fulfilment and recovery are native to the telco relationship and provide the shortest path to an end-to-end production proof. Data and bundle advances reuse most capabilities but may differ in fee representation, fulfilment units, expiry and reversal behaviour. Device, utility, education and insurance products require additional affordability, KYC, settlement or collections controls and are future product families rather than hidden Release 1 scope.

| Product family | Release intent | Distinctive considerations |
| --- | --- | --- |
| Airtime Advance | R1 | Monetary denomination; upfront/net-credit or added fee; telco airtime wallet fulfilment; recovery from recharge. |
| Data Advance | R2 or controlled R1 pilot | Bundle size/validity; non-cash value; data catalogue/version; partial fulfilment usually invalid. |
| Voice/SMS Bundle Advance | R2 | Bundle catalogue, validity, channel disclosure and usage constraints. |
| Device Finance | Future | Long tenor, stronger KYC, deposit, inventory, instalments, repossession/insurance and bureau behaviour. |
| Utility/Education/Insurance Credit | Future | Third-party merchant settlement, purpose restriction, longer collections, consumer-credit and partner disputes. |
| Generic Embedded Credit API | Future | Merchant/partner onboarding, merchant risk, settlement and consent beyond telco-only rails. |

### 6.1 Product Configuration Domains

-   Value type: monetary airtime, data volume, bundle SKU, third-party payment or device inventory.
    
-   Offer denominations, minimum/maximum, available-offer ladder and whether partial drawdown is allowed.
    
-   Fee model: upfront deduction, fee added to outstanding, flat fee, percentage, tiered fee, tax-inclusive/exclusive presentation.
    
-   Term/expiry, grace treatment, delinquency trigger and write-off policy.
    
-   Maximum concurrent advances, total programme exposure and per-product exposure.
    
-   Eligibility/risk policy and score-to-tier/limit mapping.
    
-   Fulfilment adapter and required status enquiry/reversal capabilities.
    
-   Recovery sources, waterfall, overpayment handling and recovery after write-off.
    
-   Disclosure, consent, language, channel and notification templates.
    
-   Accounting templates, revenue share, tax lines, funding allocation and settlement calendar.
    

PRD-001 \[MUST; R1\] A product shall be created from a governed product template and activated only within an approved programme; product behaviour shall not be inferred from product name.

PRD-002 \[MUST; R1\] Before acceptance, the customer shall receive a concise, conspicuous statement of advance value, spendable value, fee/tax, total amount recoverable, recovery mechanism, material restrictions and complaint route.

PRD-003 \[MUST; R1\] The platform shall retain the exact disclosure version, rendered language, channel, timestamp, offer identifier and affirmative acceptance evidence for each advance.

PRD-004 \[MUST; R1\] Product denomination ladders and offer-display rules shall be configured per telco programme and validated against funding, score-tier and telco fulfilment constraints.

PRD-005 \[MUST; R1\] The default maximum concurrent active advances per subscriber account shall be one; a higher value requires explicit programme approval, configuration and tested recovery/allocation behaviour.

PRD-006 \[MUST; R1\] The platform shall support feature flags and kill switches at legal entity, telco, programme, product, channel and segment scope.

PRD-007 \[MUST; R1\] Product changes that alter customer economics or eligibility shall create a new version and shall not retroactively change an existing offer or advance contract.

PRD-008 \[MUST; R1\] Every product shall define its customer cancellation/reversal policy, including whether unused airtime/data can be reversed by the telco and the associated ledger treatment.

PRD-009 \[MUST; R1\] Products shall expose unit economics by telco/programme, including telco fees, USSD/SMS cost, funding cost, expected loss, infrastructure allocation, taxes and revenue share.

PRD-010 \[MUST; Future\] Future products shall not be activated until their additional KYC, affordability, merchant, tenor, collections and regulatory controls are formally assessed.

## 7\. Systems of Record, Identity and Data Ownership

| Domain | Authoritative system | Platform treatment |
| --- | --- | --- |
| MSISDN status, barring, SIM registration/NIN flag, network balance | Telco System of Record | Ingest as time-stamped source attributes; do not overwrite telco truth. |
| Recharge and usage source events | Telco System of Record | Validate, deduplicate, feature-engineer and retain lineage to source event/file. |
| Eligibility, score, tier, limit, reason codes | Lending Platform System of Record | Versioned decisions with source-feature timestamps and model/config references. |
| Offer and disclosure snapshot | Lending Platform System of Record | Immutable after acceptance/expiry; linked to advance. |
| Advance contract, exposure, recovery allocation | Lending Platform System of Record | Canonical record independent of telco reporting lag. |
| Airtime/data fulfilment result | Telco authoritative for network effect; platform authoritative for orchestration state | Ambiguous outcomes enter FULFILMENT\_UNKNOWN until status/reconciliation resolves. |
| Financial journal and sub-ledger | Lending Platform System of Record | Append-only balanced entries; no mutable deletion/correction. |
| Corporate GL, statutory accounts, bank statement | Finance System of Record | Receive controlled journals/statements; reconcile back to platform ledger. |
| Credit bureau file status | Bureau authoritative for acceptance; platform authoritative for submission | Retain file, acknowledgement, rejection, correction and dispute trail. |

### 7.1 Subscriber Identity

MSISDN is a routing attribute, not a safe perpetual person identifier. Numbers may be ported, recycled, reassigned, temporarily barred or linked to changed SIM credentials. The platform therefore creates an internal subscriber-account identifier scoped to a telco and effective identity period. Where the telco supplies a privacy-permitted stable subscriber reference, it may be used to strengthen lifecycle continuity; actual NIN values should not be ingested merely to prove verification when a verified/not-verified flag is sufficient.

-   Primary operational scope: telco\_id + internal subscriber\_account\_id.
    
-   MSISDN history retained with effective dates and source event.
    
-   Port-in/port-out and number recycling create controlled account transitions rather than silent identity merging.
    
-   NIN/KYC indicators include source, verification status, verification timestamp and freshness; the NIN number is excluded unless legally and operationally necessary.
    
-   SIM swap, device change, barring and fraud flags are real-time overlays that can suppress an otherwise valid precomputed offer.
    
-   Cross-telco linkage is disabled by default and can only be introduced through an approved lawful-basis and contract decision.
    

SOR-001 \[MUST; R1\] Every tenant-owned business and financial entity shall contain an immutable telco\_id and programme\_id where applicable; MSISDN alone shall never be a database key or access-control boundary.

SOR-002 \[MUST; R1\] The platform shall maintain effective-dated MSISDN and subscriber-account history sufficient to handle porting, recycling, reactivation and correction without assigning prior debt to a new holder.

SOR-003 \[MUST; R1\] The platform shall ingest NIN/SIM-registration status as a minimal verification indicator by default and shall prohibit unnecessary storage of the underlying NIN value.

SOR-004 \[MUST; R1\] Every derived feature shall retain source period, last-updated timestamp, completeness indicator and lineage to the source feed/event version.

SOR-005 \[MUST; R1\] Data ownership, derived-data rights, retention and post-termination transfer/destruction obligations shall be defined in each telco data-processing and commercial agreement.

SOR-006 \[MUST; R1\] The platform shall distinguish authoritative source facts, derived analytical features, decision outcomes and financial records in the data catalogue and access model.

SOR-007 \[MUST; R1\] Any cross-telco negative-file or identity linkage shall be disabled until the approved Design Decision DD-12 identifies lawful basis, participating telcos, permitted fields, governance and customer rights.

## 8\. Multi-Telco Operating and Tenancy Model

![image](https://static-us-img.skywork.ai/prod/nexus/1784233129/cropped_image_7_1784233129854634976.jpg)

**Shared platform economics with independent telco configuration, isolation, scaling, failure domains and settlement.**

The administration portal will provide a telco filter, but the infrastructure must do more than filter screens. Tenant identity is resolved from authenticated credentials and corroborated against the payload; it propagates through API context, events, storage partitions, cache keys, logs, metrics, exports, reconciliation and settlement. Operator-specific behaviour is isolated in adapters and configuration. A telco may later move from shared infrastructure to a dedicated database or deployment without changing core business semantics.

### 8.1 Isolation and Routing

-   Inbound identity: mTLS/API credentials, source network and signed message establish telco context; a conflicting payload telco\_id is rejected and audited.
    
-   Canonical routing: gateway selects adapter, programme, product, rules, queue partition, rate limit and response mapping from telco context.
    
-   Data: mandatory telco\_id on rows; row-level security; partitioning/sharding strategy; tenant-aware caches and object-store paths.
    
-   Events: shared logical event types with telco/programme partition keys; dedicated topics or clusters where volume, residency, security or commercial isolation requires them.
    
-   Resilience: independent circuit breakers, retry budgets, dead-letter queues, worker quotas and kill switches.
    
-   Operations: platform super-users may see approved consolidated views; telco users only see authorised programmes and fields.
    
-   Finance: separate reconciliation, funding and settlement positions per programme, with consolidated group reporting derived above them.
    

| Isolation level | Default / trigger | Implication |
| --- | --- | --- |
| Shared services, partitioned data | Default for launch and normal programmes | Best economics; strict tenant context and row-level controls required. |
| Dedicated data store/schema | Contract, volume, residency or risk trigger | Greater blast-radius and access isolation; consolidated reporting through governed data products. |
| Dedicated deployment/region | Major telco or jurisdiction requirement | Independent scaling, release and DR; higher cost and operating complexity. |
| Separate legal/operating instance | Regulatory or corporate separation | Independent keys, staff, policies and reporting; canonical contracts retained. |

-   TEN-001 \[MUST; R1\] Tenant context shall be established from authenticated connection identity and shall not rely solely on a caller-supplied telco\_id.
    
-   TEN-002 \[MUST; R1\] The gateway shall reject and security-log any mismatch between credential tenant, endpoint scope, certificate identity and payload tenant.
    
-   TEN-003 \[MUST; R1\] Every cache key, object path, message partition, search index, log correlation and export query containing tenant data shall include tenant context.
    
-   TEN-004 \[MUST; R1\] Each telco adapter shall have independent connection pools, rate limits, circuit breakers, retry budgets, dead-letter handling, health checks and maintenance switches.
    
-   TEN-005 \[MUST; R1\] A degradation or outage in one telco adapter shall not materially reduce originations, recoveries or portal availability for another telco beyond approved shared-capacity limits.
    
-   TEN-006 \[MUST; R1\] Tenant users shall be authorised by role plus permitted legal entity, telco, programme, product and data domain; an All Telcos view shall be restricted and audited.
    
-   TEN-007 \[MUST; R2\] The data architecture shall support moving a telco from shared to dedicated storage/deployment through controlled migration without changing external identifiers or ledger meaning.
    
-   TEN-008 \[MUST; R1\] Reconciliation and settlement shall be performed at telco-programme level before any consolidated reporting.
    
-   TEN-009 \[MUST; R1\] Capacity management shall reserve or quota resources so that one telco’s traffic, replay or batch scoring cannot starve another tenant.
    
-   TEN-010 \[MUST; R2\] Tenant-specific encryption keys shall be supported where required by contract, risk assessment or jurisdiction.
    

## 9\. Enterprise Capability Model

The target operating model is organised around reusable capabilities rather than a collection of channel-specific applications. Capabilities may be implemented as services or modules in Volume 2, but this document defines the business responsibility and required outcomes.

| Capability domain | Core capabilities | Primary accountable owner |
| --- | --- | --- |
| Partner & Programme | Legal entity, telco, funder, bureau and programme onboarding; contracts; configuration; certification. | Partnerships / Product / Legal |
| Customer & Channel | USSD/app/API sessions, identity context, offers, disclosures, consent, notifications and self-service. | Product / Customer Operations |
| Credit & Portfolio | Features, scoring, affordability, tiers, limits, anti-gaming, fraud, guardrails and monitoring. | Chief Risk / Credit Risk |
| Origination & Fulfilment | Offer snapshot, request, exposure reservation, telco fulfilment, status enquiry and reversal. | Product / Technology Operations |
| Ledger & Finance | Sub-ledger, accounting templates, revenue, fees, tax, funding and GL interfaces. | Finance / Treasury |
| Recovery & Collections | Recharge interception events, allocation, delinquency, dunning, write-off and post-write-off recovery. | Collections / Finance / Risk |
| Reconciliation & Settlement | Transaction matching, breaks, partner statements, approval and cash/inventory settlement. | Finance Operations |
| Compliance & Conduct | Disclosure, consent, complaints, bureau/regulatory reporting, privacy and evidence. | Compliance / DPO / Legal |
| Data & Analytics | Data quality, feature lineage, reporting, portfolio analytics, unit economics and regulatory data. | Data / Risk / Finance |
| Platform & Assurance | Security, tenant isolation, observability, resilience, testing, simulator and change governance. | Technology / CISO / Service Management |

CAP-001 \[MUST; R0\] Every enterprise capability shall have one accountable business owner, one system-of-record owner and defined service measures before production launch.

CAP-002 \[MUST; R1\] The product roadmap shall reuse enterprise capabilities and shall not create product-specific ledgers, identities, complaints registers or reconciliation engines unless approved by architecture governance.

CAP-003 \[MUST; R1\] Business ownership and technical service ownership shall be separately recorded so operational incidents have both a decision owner and a restoration owner.

## 10\. Customer Experience and Channel Architecture

USSD is a first-class product surface, not a thin wrapper around an API. Sessions are short-lived, may be retried by the network, may terminate without a final screen, and can fail while an economic action is in flight. The customer experience must therefore be designed around offer snapshots, idempotent confirmation, durable transaction state and SMS or app fallback. The platform should support telco apps and APIs, but Release 1 acceptance must prove the USSD journey under realistic failure conditions.

### 10.1 Canonical Customer Journey

1.  The customer enters the telco-owned or shared shortcode/menu and selects Borrow/Advance.
    
2.  The channel gateway resolves telco and subscriber context and requests currently valid offers.
    
3.  The platform applies real-time suppression checks and returns a short ranked offer list with expiry.
    
4.  The customer selects an amount/product and receives the required cost and recovery disclosure in the chosen language.
    
5.  The customer gives active confirmation. The channel sends a unique confirmation/request identifier.
    
6.  The platform validates the offer snapshot, concurrency, funding and exposure, then creates a REQUESTED advance and reserves capacity.
    
7.  The correct telco adapter sends fulfilment and records the response. A timeout after possible success is never blindly retried.
    
8.  The customer receives the final USSD response where possible and a durable SMS/app confirmation or failure notice.
    
9.  Future recharge/recovery events reduce the outstanding advance according to the configured waterfall and generate receipts where required.
    

### 10.2 USSD Session Model

| Session concern | Required design behaviour |
| --- | --- |
| Session identity | Telco-scoped session ID plus subscriber and correlation ID; no financial effect keyed only to a screen number. |
| Menu version | Versioned per telco/programme/language; active sessions remain on their original version where feasible. |
| Timeout before confirmation | No advance created; offer may remain valid for later retrieval. |
| Timeout after confirmation | Advance state is authoritative; send SMS outcome. Duplicate confirmation replays the existing response. |
| Timeout after telco call | Set FULFILMENT\_UNKNOWN; status enquiry/reconciliation resolves before any repeat fulfilment. |
| Back navigation/repeat menu | No duplicate offer/advance; preserve or recreate a safe session from the offer snapshot. |
| Long disclosure | Use concise mandatory screen(s) and a durable SMS/link where permitted; never omit total cost or acceptance evidence. |
| Language | English plus configurable Pidgin/Hausa/Yoruba/Igbo; legal approval and fallback language per programme. |
| Cost bearing | USSD and SMS charging model recorded per programme and included in unit economics. |
| Accessibility | Plain language, short choices, error recovery, customer-support route and non-smartphone compatibility. |

### 10.3 Notifications and Customer Rights

-   Transactional messages: offer confirmation where required, successful advance, failed/unknown fulfilment resolution, recovery receipt, outstanding balance, closure, reversal, complaint acknowledgement and correction.
    
-   Promotional messages: separate consent/legitimate-basis assessment, DND controls, frequency caps, opt-out and quiet hours.
    
-   Self-service: check eligibility/limit, outstanding amount, recovery history, fee/terms, complaint status, marketing opt-out and borrowing self-exclusion.
    
-   Notification evidence: template/version, language, destination, route, provider ID, send/delivery status, retries and failure reason.
    
-   Conduct guardrails: no threatening or humiliating content, no contact scraping, no third-party shaming and no misleading urgency.
    

CHN-001 \[MUST; R1\] USSD shall be a Release 1 certified channel with versioned menu flows, state management, telco-specific routing and fault-tested timeout behaviour.

CHN-002 \[MUST; R1\] A customer confirmation shall carry an idempotency key unique within telco/programme scope; replay shall return the original economic outcome rather than create a new advance.

CHN-003 \[MUST; R1\] When a session terminates after confirmation, the platform shall send a durable outcome notification and provide a status-check route without requiring a second advance request.

CHN-004 \[MUST; R1\] The channel shall display the total cost, amount/value delivered, amount recoverable and recovery mechanism before active opt-in.

CHN-005 \[MUST; R1\] Menu and message content shall be versioned, effective-dated, locally approved and configurable by telco, product, segment, language and channel.

CHN-006 \[MUST; R1\] Transactional and promotional communications shall use separate consent, suppression and frequency policies.

CHN-007 \[MUST; R1\] The platform shall retain send and delivery evidence, retry outcome and failure reason for material transactional notifications.

CHN-008 \[MUST; R1\] Quiet hours, frequency limits, sender identity, DND handling and language selection shall be configurable per programme subject to non-waivable legal constraints.

CHN-009 \[MUST; R1\] Subscribers shall be able to opt out of marketing and self-exclude from new borrowing without preventing repayment, complaint access or required service messages.

CHN-010 \[MUST; R1\] The platform shall support customer-facing balance, recovery-history and complaint-status enquiry through at least one low-bandwidth channel.

CHN-011 \[MUST; R1\] The telco partnership shall decide and document shortcode ownership, direct gateway versus aggregator route, USSD/SMS cost bearing, session limits and production support responsibility.

CHN-012 \[MUST; R1\] A customer-facing channel shall never present an offer whose underlying snapshot has expired, been revoked or exceeded current exposure/funding controls.

## 11\. Credit Decisioning, Scoring and Anti-Gaming

The decisioning model combines precomputed eligibility and limits with real-time safety checks. Scheduled processing allows tens of millions of subscriber profiles to be scored economically and makes USSD responses fast. It does not mean the daily score can be trusted blindly: real-time telco events, current exposure, funding availability, stale-data thresholds, fraud indicators and programme suspensions can suppress or reduce an offer at request time.

### 11.1 Decision Layers

| Layer | Purpose | Illustrative inputs | Output |
| --- | --- | --- | --- |
| Data readiness | Determine whether the profile is sufficiently fresh and complete. | Feed age, missing periods, source quality, unresolved identity event. | Ready / hold / conservative fallback / reject. |
| Eligibility | Apply hard policy and conduct gates. | Active prepaid status, NIN flag, tenure, barring, self-exclusion, fraud, outstanding policy. | Eligible/ineligible and reason code. |
| Behaviour & affordability | Estimate sustainable repayment capacity from normal behaviour. | Recharge median/trend/frequency, active days, spend mix, tenure, volatility. | Affordability band and confidence. |
| Trust / repayment | Reward proven performance and penalise poor repayment. | Successful cycles, time-to-repay, partial repayment, delinquency, write-off. | Trust score and tier movement constraint. |
| Fraud / gaming | Detect manipulation or compromised accounts. | Recharge spikes, circular top-ups, SIM swap, device churn, event velocity, blacklists. | Suppress, cap, review or allow. |
| Portfolio control | Protect programme and funding pool. | Approval rate, average limit, early delinquency, exposure cap, liquidity. | Programme/product/segment throttle or stop. |
| Offer construction | Map approved capacity to product options. | Tier, product ladder, fee, telco denominations, current exposure. | Ranked, time-bound offer snapshot. |

### 11.2 Recharge Anti-Gaming

A single large recharge must not create an immediate proportional limit increase. The platform shall distinguish normal capacity from isolated events using multiple windows, robust statistics and progressive trust. One-off spikes may improve recency or demonstrate available funds, but their contribution to affordability is capped until repeated behaviour or repayment performance validates the change.

-   Use 30/60/90/180-day windows and effective active-period measures rather than only the last calendar month.
    
-   Calculate median, trimmed mean, winsorised total, frequency, days-since-last-recharge, variance and concentration in top recharge events.
    
-   Compare current window to personal baseline, peer/segment range and prior scoring-cycle input.
    
-   Cap the contribution of any single recharge and of recharges occurring immediately before a scoring cut-off.
    
-   Detect rapid recharge-borrow-consume or circular patterns that suggest limit gaming or reseller/SIM-box behaviour.
    
-   Allow a maximum upward movement of one configured tier per scoring cycle by default; downward changes may be immediate for safety.
    
-   Require successful repayment cycles before high tiers and use challenger models only within approved experiment limits.
    

| Example | Naive interpretation | Required interpretation |
| --- | --- | --- |
| Typical monthly recharge around ₦2,000; one ₦20,000 event | Average rises sharply and limit jumps. | Spike is capped/winsorised; baseline and consistency dominate; at most one tier upward. |
| Recharge rises gradually over four months and advances repay quickly | Recent average increases. | Sustained trend plus trust can increase tier within affordability and exposure caps. |
| High total recharge from many same-minute transfers | High-value user. | Velocity/concentration anomaly; fraud/gaming rule may suppress or review. |
| Low recharge value but 30 successful fast repayment cycles | Low affordability only. | Trust supports higher limit, but still bounded by sustainable recharge/recovery capacity. |

CRD-001 \[MUST; R1\] The platform shall maintain separately identifiable data-readiness, eligibility, affordability, behaviour, trust, fraud and portfolio-control outcomes for each decision.

CRD-002 \[MUST; R1\] Scheduled scoring cadence shall be configurable per programme and segment, with daily processing as the default for active eligible populations; scoring shall be incremental where practicable.

CRD-003 \[MUST; R1\] Real-time decisioning shall query precomputed offers but shall revalidate offer expiry, subscriber safety flags, concurrency, exposure, funding, programme status and critical data freshness.

CRD-004 \[MUST; R1\] Recharge affordability shall use robust multi-window features and shall not be determined by a single recharge or a simple percentage of last-month recharge.

CRD-005 \[MUST; R1\] The maximum upward tier movement shall default to one tier per scoring cycle and shall be configurable only within approved risk-policy bounds; downward movement may be immediate.

CRD-006 \[MUST; R1\] A large isolated recharge shall have a configurable capped contribution and shall generate an explainable spike/anomaly feature.

CRD-007 \[MUST; R1\] Repayment history shall include count, recency, speed, partial recovery, delinquency, write-off and post-write-off behaviour and shall become a major driver after sufficient observed cycles.

CRD-008 \[MUST; R1\] Every decision shall retain feature values used, feature timestamps, missing-data treatment, rule hits, score components, model/rule/config versions and human-readable reason codes.

CRD-009 \[MUST; R1\] A material model or policy change shall be validated through back-testing, fairness/conduct review, portfolio-impact simulation, independent approval and controlled rollout.

CRD-010 \[MUST; R1\] The platform shall support deterministic rule replay for complaints, audit and regulatory enquiries using the historical data snapshot or a provably equivalent retained feature snapshot.

CRD-011 \[MUST; R1\] Stale, incomplete or corrupt source data shall trigger configurable conservative treatment; the platform shall not silently reuse an indefinitely old limit.

CRD-012 \[MUST; R1\] The offer amount shall be the minimum of policy limit, affordability limit, trust/tier limit, product cap, current available exposure, subscriber headroom, funding headroom and telco fulfilment constraints.

CRD-013 \[MUST; R1\] Approval-rate, average-limit, exposure, fulfilment-failure and early-delinquency deviations shall be monitored against baselines and may automatically suspend originations by programme/product/segment.

CRD-014 \[MUST; R1\] Automatic portfolio guardrails shall require an authorised maker-checker action and incident record to re-arm after a material breach.

CRD-015 \[MUST; R1\] Decision experiments and challenger models shall be explicitly scoped, customer-safe, exposure-capped and separately reportable.

## 12\. Offer, Advance and Fulfilment Lifecycle

Offer is separate from the advance. The advance begins at REQUESTED and is controlled by idempotent state transitions.

| Discover / Request | Eligible Offers | Disclosure & Consent | Reserve Exposure | Telco Fulfilment | Active Advance | Recovery / Delinquency | Close / Write-off |
| --- | --- | --- | --- | --- | --- | --- | --- |

Every step retains telco, product, rule, score, disclosure and configuration versions. Financial events post to the append-only ledger.

### 12.1 Offer Lifecycle

-   DRAFT/CALCULATED: internal result not yet exposed.
    
-   AVAILABLE: valid for the subscriber, telco, product and time window.
    
-   SELECTED: customer chose the offer during a channel session.
    
-   ACCEPTED: active consent recorded and advance request initiated.
    
-   EXPIRED/REVOKED: cannot be accepted due to time, risk, configuration or programme change.
    

### 12.2 Advance State Model

| State | Meaning | Allowed next outcomes |
| --- | --- | --- |
| REQUESTED | Accepted request received; idempotency established. | VALIDATED, REJECTED |
| VALIDATED | Offer, customer, funding and policy revalidated. | EXPOSURE\_RESERVED, REJECTED |
| EXPOSURE\_RESERVED | Credit/funding headroom atomically reserved. | FULFILMENT\_PENDING, CANCELLED |
| FULFILMENT\_PENDING | Instruction sent or ready to send to telco. | ACTIVE, FULFILMENT\_FAILED, FULFILMENT\_UNKNOWN |
| FULFILMENT\_UNKNOWN | Telco may have succeeded; blind retry prohibited. | ACTIVE, FULFILMENT\_FAILED after enquiry/reconciliation |
| ACTIVE | Value confirmed delivered; outstanding exists. | PARTIALLY\_RECOVERED, SETTLED, DELINQUENT, REVERSED |
| PARTIALLY\_RECOVERED | Some outstanding recovered. | SETTLED, DELINQUENT, WRITTEN\_OFF |
| DELINQUENT | Age/policy threshold reached. | PARTIALLY\_RECOVERED, SETTLED, WRITTEN\_OFF |
| SETTLED | Outstanding fully recovered or lawfully extinguished. | CLOSED |

| State | Meaning | Allowed next outcomes |
| --- | --- | --- |
| WRITTEN\_OFF | Accounting write-off; recovery may continue if policy permits. | RECOVERED\_AFTER\_WRITE\_OFF, CLOSED |
| REVERSED/CANCELLED/REJECTED | No valid active exposure, with reversal entries if financial effect existed. | Terminal or corrected through explicit workflow |

The OFFERED state is intentionally excluded from the advance state machine. Offer and advance are distinct entities. This resolves the inconsistency identified in the v2 review and allows offers to expire or be revoked without creating financial records.

### 12.3 Distributed Transaction Controls

-   Persist request and idempotency key before external fulfilment.
    
-   Reserve subscriber/programme/funding exposure atomically and expire abandoned reservations.
    
-   Use telco transaction reference and platform correlation ID in every call and status enquiry.
    
-   On timeout after send, enter FULFILMENT\_UNKNOWN; query telco or reconcile before retry.
    
-   Post the advance/fee ledger entries only according to the approved accounting point (usually confirmed fulfilment), while retaining pending control entries or reservations.
    
-   Use compensating/reversal entries; never delete an advance to hide a failed transaction.
    
-   Send final channel response independently of economic completion, with SMS fallback.
    

ADV-001 \[MUST; R1\] Offer and advance shall be separate entities; an advance shall begin at REQUESTED only after active acceptance of a valid offer snapshot.

ADV-002 \[MUST; R1\] The platform shall enforce idempotency at request, fulfilment, status-enquiry, recovery, reversal and notification boundaries.

ADV-003 \[MUST; R1\] Subscriber, programme and funding exposure shall be reserved atomically before fulfilment and released through controlled expiry/cancellation when no economic effect occurred.

ADV-004 \[MUST; R1\] A telco timeout or connection loss after a fulfilment instruction may have been received shall produce FULFILMENT\_UNKNOWN and shall prohibit blind fulfilment retry.

ADV-005 \[MUST; R1\] FULFILMENT\_UNKNOWN shall be resolved through status enquiry, telco evidence or reconciliation within a configured operational SLA, with escalation and customer communication.

ADV-006 \[MUST; R1\] The platform shall support duplicate and out-of-order telco acknowledgements without duplicate economic effect or invalid backward state transition.

ADV-007 \[MUST; R1\] The default maximum concurrent active advances per subscriber account shall be one; the decision shall be evaluated atomically to prevent simultaneous requests from different channels.

ADV-008 \[MUST; R1\] Where multiple active advances are explicitly enabled, the programme shall define aggregate exposure, customer disclosure, recovery waterfall and limit-availability behaviour.

ADV-009 \[MUST; R1\] A channel response failure after successful fulfilment shall not reverse the advance automatically; the customer shall receive a durable confirmation and status route.

ADV-010 \[MUST; R1\] A fulfilment failure shall release unused exposure and produce no customer liability unless telco evidence later proves value was delivered, in which case a controlled correction workflow applies.

ADV-011 \[MUST; R1\] All state transitions shall record actor/service, source event, timestamp, prior/new state, correlation reference and reason.

ADV-012 \[MUST; R1\] Manual state repair shall be prohibited; authorised operations shall use explicit commands that generate audit and any required ledger correction.

## 13\. Recovery, Delinquency and Collections

Recharge recovery is the primary automated collection method, but recovery mechanics alone do not define a collections strategy. The platform must determine ageing, reminders, write-off and conduct when a subscriber does not recharge. For short-term airtime/data credit, the expected strategy is low-friction and proportionate: intercept eligible future recharges, provide factual reminders, restrict further credit and avoid aggressive off-network collections unless a future product explicitly requires them.

### 13.1 Recovery Allocation

-   Telco sends recharge/deduction event or calls a synchronous recovery API according to the programme integration.
    
-   Platform deduplicates and validates the event, determines eligible outstanding, and applies the approved waterfall.
    
-   Recovery may be partial; the platform records principal, fee, tax and other components according to accounting policy.
    
-   Excess recovered value is not silently retained. It is returned/released, applied only with lawful authority, or held in a defined suspense process.
    
-   Reversal-before-original and original-after-reversal are handled through pending linkage and eventual consistency.
    
-   Recoveries after write-off are recorded distinctly for finance and portfolio reporting.
    

### 13.2 Delinquency and Conduct

| Stage | Illustrative trigger | Permitted treatment |
| --- | --- | --- |
| Current | Within expected recovery period | Normal service messages; new advance only if concurrency policy allows. |
| Early overdue | e.g. 1-7 days without sufficient recharge | Factual balance reminder; suppress new credit; no threatening language. |
| Delinquent | Configurable age such as 8-30/60 days | Periodic reminders within contact caps; complaint and vulnerability handling. |
| Late delinquent | Beyond programme threshold | Continue passive telco recovery where lawful; consider write-off; bureau treatment per policy. |
| Written off | Accounting threshold met | No deletion; credit remains closed/restricted; post-write-off recoveries separately posted. |
| Disputed / vulnerable | Complaint, fraud claim, death, reassignment or hardship marker | Pause specified communications/recovery as policy requires; case management and correction. |

COL-001 \[MUST; R1\] Recovery events shall be idempotent, tenant-scoped and linked to a telco source reference; duplicates shall not increase recovered amount.

COL-002 \[MUST; R1\] Recovery allocation shall be configurable per programme, with oldest-due-first as the default where multiple balances exist, and shall remain reproducible from the ledger.

COL-003 \[MUST; R1\] The platform shall support partial recovery and shall recalculate outstanding and available limit immediately after committed recovery.

COL-004 \[MUST; R1\] Recovery shall never exceed the recoverable outstanding plus any legally authorised amount; excess shall enter a defined release/refund/suspense workflow.

COL-005 \[MUST; R1\] Delinquency buckets, ageing basis, reminder cadence, contact caps, quiet hours, write-off triggers and bureau status shall be configured per programme under approved conduct policy.

COL-006 \[MUST; R1\] Collections messages shall be factual, proportionate and harassment-free and shall not disclose debt to unrelated third parties.

COL-007 \[MUST; R1\] A dispute, fraud allegation, number-recycling event, death/vulnerability marker or legal hold shall trigger the configured pause/review treatment.

COL-008 \[MUST; R1\] Write-off shall create explicit ledger and portfolio events and shall not delete the advance or stop lawful passive recovery unless policy requires it.

COL-009 \[MUST; R1\] Recoveries after write-off shall be separately classified, reconciled and reported.

COL-010 \[MUST; R1\] The platform shall provide ageing, roll-rate, cure, recovery-time, write-off and post-write-off recovery reporting by telco, product, vintage and score band.

COL-011 \[MUST; R1\] The default airtime/data strategy shall not escalate to external field or social-contact collections; any future escalation requires product-specific approval and regulatory assessment.

COL-012 \[MUST; R1\] Customers shall be able to obtain an accurate statement of original advance, fees, recoveries, outstanding and corrections through support or self-service.

## 14\. Treasury, Funding and Exposure Management

The platform must be able to operate under different funding arrangements without obscuring who bears economic exposure. A telco may effectively supply airtime inventory and settle net amounts; the platform may fund advances from its balance sheet; or an external funder may provide a ring-fenced facility. Each model changes cash timing, accounting, pricing and suspension controls and must be defined per programme.

| Funding model | Economic flow | Key controls |
| --- | --- | --- |
| Telco inventory / net settlement | Telco fulfils value; platform and telco settle fees, recoveries and share periodically. | Inventory exposure ownership, loss bearer, netting, evidence and telco statement. |
| Own balance sheet | Platform funds economic value and receives recoveries/fees. | Liquidity forecast, pool cap, cost of funds, concentration and bank settlement. |
| Third-party funder | Funder allocates facility; platform originates/services; returns and losses allocated contractually. | Eligibility covenants, drawdown, utilisation, waterfall, funder statements and audit rights. |
| Hybrid | Different pools fund products/segments or overflow. | Deterministic allocation, no double funding, pool priority and transfer restrictions. |

### 14.1 Funding Pool Controls

-   Approved principal/currency, available capacity, committed/reserved/active exposure, recovery cash, fees and losses.
    
-   Per-telco, programme, product, score-band, tier and daily-origination caps.
    
-   Minimum liquidity buffer and forecast based on demand, recovery curves and settlement timing.
    
-   Automatic throttle or stop when capacity, covenant, settlement or loss thresholds are breached.
    
-   Funding-cost accrual and allocation to unit economics and financial statements.
    
-   Independent reconciliation of platform pool position to funder/telco/bank evidence.
    

TRE-001 \[MUST; R1\] Every programme shall identify an approved funding model and loss bearer before product activation.

TRE-002 \[MUST; R1\] The platform shall maintain funding pools with currency, owner, cap, utilisation, reservations, available headroom, cost and effective-dated terms.

TRE-003 \[MUST; R1\] An advance shall be allocated to exactly one economic funding source at origination unless an approved split-funding model is explicitly supported.

TRE-004 \[MUST; R1\] Funding headroom shall be checked and reserved atomically with subscriber/programme exposure.

TRE-005 \[MUST; R1\] The platform shall automatically throttle or suspend new originations when a pool cap, liquidity buffer, covenant, settlement default or risk trigger is breached; recoveries shall continue.

TRE-006 \[MUST; R2\] Funding-cost accrual shall be represented by explicit accounting events and included in programme/product profitability.

TRE-007 \[MUST; R1\] Funder statements shall include opening capacity, drawdowns/originations, recoveries, fees, losses, adjustments, closing exposure and exceptions.

TRE-008 \[MUST; R2\] Treasury shall be able to forecast funding demand and expected recoveries by telco/programme/product using current offers, utilisation and vintage curves.

TRE-009 \[MUST; R1\] Manual funding adjustments shall require maker-checker approval, reason and ledger/statement reconciliation.

TRE-010 \[MUST; R1\] A funding outage or insufficient headroom shall fail closed for new credit and shall return a customer-safe unavailable outcome rather than create an unfunded obligation.

## 15\. Financial Architecture, Economics, Reconciliation and Settlement

### 15.1 Non-Configurable Ledger Invariants

Accounting event templates may be configurable, but balance is not. Every activated template must produce equal debits and credits per currency at validation time and posting time. Entries are append-only. Corrections are linked reversals and replacement postings. Summary balances, dashboards and partner statements are derived from the ledger and reconciliation state.

| Economic event | Illustrative ledger/control treatment |
| --- | --- |
| Fulfilment confirmed | Recognise receivable/exposure and inventory/cash/funder position according to funding model. |
| Service fee/tax | Recognise fee receivable/revenue/tax liability according to approved accounting policy and timing. |
| Recovery | Reduce receivable; allocate fee/principal/tax; update cash/telco settlement position. |
| Recovery reversal | Reverse linked recovery entries, restore outstanding and create exception where required. |
| Write-off | Move receivable to loss/write-off accounts without deleting customer history. |
| Post-write-off recovery | Recognise recovery of written-off balance distinctly. |
| Funding cost accrual | Accrue cost to programme/pool based on approved methodology. |
| Partner revenue share | Accrue telco/platform/funder shares and applicable taxes/withholding lines. |
| Settlement | Clear partner payable/receivable against cash or inventory settlement evidence. |

### 15.2 Reconciliation Layers

10.  Transaction reconciliation: platform advance/fulfilment/recovery events against telco transaction records.
     
11.  Balance reconciliation: opening + movements = closing for outstanding, funding pool and partner positions.
     
12.  Settlement reconciliation: calculated payable/receivable against partner statement, invoice, bank or inventory movement.
     
13.  General ledger reconciliation: platform sub-ledger journals against finance system postings.
     
14.  Regulatory/bureau reconciliation: reported populations and amounts against platform authoritative data and acknowledgements.
     

LED-001 \[MUST; R1\] All financial postings shall be append-only, uniquely referenced, time-stamped and linked to the originating business event.

LED-002 \[MUST; R1\] Corrections shall use linked reversal and replacement entries; direct update or deletion of posted financial entries is prohibited.

LED-003 \[MUST; R1\] Every posting template shall produce debits equal to credits per currency, validated at configuration activation and transaction posting; an unbalanced template shall fail closed.

LED-004 \[MUST; R1\] The platform shall prevent the same economic event from posting more than once through idempotency and unique business-reference constraints.

LED-005 \[MUST; R1\] Ledger posting shall retain telco, programme, product, advance, funding pool, counterparty, currency, accounting template/version and source event.

LED-006 \[MUST; R1\] Mutable balance tables shall be treated as derived projections and shall be periodically verified against ledger reconstruction.

FIN-001 \[MUST; R1\] Fee, revenue-share, funding-cost and tax rules shall be effective-dated and configured per programme, while existing advances retain their contracted economics.

FIN-002 \[MUST; R1\] The settlement engine shall calculate gross and net positions for telco, platform, funder and other approved parties without hardcoding a fixed number of participants.

-   FIN-003 \[MUST; R1\] Settlement shall support configurable cycles, cut-offs, calendars, currency, thresholds, netting, invoice references and approval workflow.
    
-   FIN-004 \[MUST; R1\] Reconciliation breaks shall have type, amount, age, owner, evidence, status, root cause, proposed correction and approval trail.
    
-   FIN-005 \[MUST; R1\] A break shall not be force-matched without preserving the original mismatch and authorised rationale.
    
-   FIN-006 \[MUST; R2\] The platform shall support tax lines and evidence required for Nigerian VAT, withholding or other applicable treatment, with final rules confirmed by qualified tax advisers.
    
-   FIN-007 \[MUST; R1\] Partner statements shall be reproducible from ledger and reconciliation records and shall provide drill-down to transaction references.
    
-   FIN-008 \[MUST; R1\] Financial close shall include completeness checks for fulfilment/recovery feeds, unresolved FULFILMENT\_UNKNOWN items, suspense, unposted events and settlement exceptions.
    
-   FIN-009 \[MUST; R1\] Manual adjustments shall be role-restricted, maker-checker approved, reason-coded, ledger-posted and included in partner reconciliation.
    
-   FIN-010 \[MUST; R2\] The platform shall produce cost and profitability attribution by telco/programme/product, including infrastructure, communication, funding, loss, tax and partner-share components.
    

## 16\. Nigeria Regulatory and Conduct Architecture

### Regulatory Status Note

This section defines required capabilities and a conservative control baseline; it is not legal advice. As at 16 July 2026, the FCCPC Digital, Electronic, Online or Non-Traditional Consumer Lending Regulations 2025 and related court proceedings require active legal monitoring. Any unresolved market-entry claim, including reported local-hosting or ownership conditions, is a watch item unless confirmed by an authoritative instrument or binding approval.

The platform must be capable of satisfying the obligations that attach to the licensed lending partner even when the telco provides the customer channel. Contractual allocation does not eliminate statutory accountability. The design shall therefore capture evidence at source rather than rely on the telco to reconstruct disclosure, consent, complaints, privacy or reporting records later.

### 16.1 Regulatory Capability Matrix

| Domain | Enterprise capability | Release posture |
| --- | --- | --- |
| FCCPC / digital lending | Registration data pack, product/fee register, clear pre-acceptance disclosure, active opt-in, responsible conduct, complaint register, audit/report exports. | R1 mandatory capability; activation/content aligned to final legal position. |
| Credit bureaux | Configurable extract, validation, submission/acknowledgement, corrections, disputes and reconciliation. | R1 build-capable and initially dormant where submission is not yet mandated/approved. |
| NDPA / NDPC | Lawful-basis and consent records, privacy notice, minimisation, DPIA, rights workflow, processor controls, breach response and transfer/residency governance. | R1 mandatory. |
| NCC / VAS / channels | Shortcode/USSD and SMS route approvals, sender identity, DND handling, aggregator/telco responsibilities and service records. | R1 programme prerequisite. |
| Tax | Configurable VAT/withholding/other tax lines, evidence and reporting; entity/counterparty treatment confirmed by adviser. | R1 accounting design; rates/treatment confirmed before launch. |
| Consumer complaints | Accessible intake, acknowledgement, priority, SLA, root cause, redress, regulatory export and telco hand-off. | R1 mandatory. |
| Automated decisions | Explainability, human review/correction path where applicable, model governance and customer data correction. | R1 mandatory capability. |

### 16.2 Disclosure and Consent Evidence

-   Plain-language product name and legal entity making the advance.
    
-   Amount/value advanced, spendable amount/value, service fee, tax where applicable and total recoverable amount.
    
-   Repayment/recovery mechanism, timing uncertainty and effect of partial recharges.
    
-   Eligibility/offer validity and any material restriction on use or expiry.
    
-   Privacy notice or accessible reference and use of telco behavioural data for automated decisioning.
    
-   Complaint and correction route.
    
-   Active opt-in captured with channel/session/request ID; no pre-selected or automatic lending.
    
-   Immutable disclosure version and rendered language retained with the advance.
    

### 16.3 Complaints and Redress

| Complaint type | Required controls |
| --- | --- |
| Did not request / duplicate advance | Locate consent/idempotency evidence; pause relevant activity; reconcile fulfilment; reverse or correct where substantiated. |

| Complaint type | Required controls |
| --- | --- |
| Value not received | Check telco fulfilment/status/reconciliation; prevent double fulfilment; provide outcome and redress. |
| Incorrect fee or recovery | Reconstruct offer and ledger; compare telco deduction; correct through reversal/repost and statement. |
| Number reassigned / wrong person | Apply identity-period controls; suspend recovery and credit; investigate telco evidence; detach prior debt where appropriate. |
| Fraud/SIM swap | Freeze new credit, investigate device/SIM events, preserve evidence and follow fraud/breach process. |
| Privacy/data correction | Route to DPO workflow; validate source and derived features; correct prospectively and assess prior decisions/reports. |
| Bureau dispute | Trace submitted record, acknowledgements and corrections; update bureau and customer within governed process. |

-   REG-001 \[MUST; R1\] Each programme shall maintain a regulatory obligation register mapping requirement, accountable entity, control, evidence, reporting cadence, legal source and status.
    
-   REG-002 \[MUST; R1\] The platform shall retain versioned disclosure and active acceptance evidence per advance and shall prohibit auto-acceptance, pre-ticked consent or unsolicited automatic advance creation.
    
-   REG-003 \[MUST; R1\] The platform shall maintain an accessible complaint register with category, customer, telco, advance, acknowledgement, SLA, owner, evidence, outcome, redress and regulatory-report fields.
    
-   REG-004 \[MUST; R1\] Complaint SLAs shall be configurable by jurisdiction but shall not exceed any statutory or contractual maximum; imminent breach shall alert and escalate.
    
-   REG-005 \[MUST; R1\] The platform shall provide regulator-ready exports for products, fees, portfolio, complaints, conduct incidents, audits and other required returns without direct database manipulation.
    
-   REG-006 \[MUST; R1\] Credit bureau reporting capability shall be in Release 1 scope, configurable per programme and bureau, with validation, acknowledgements, rejections, corrections, disputes and reconciliation.
    
-   REG-007 \[MUST; R1\] A bureau submission shall not be considered complete until accepted or an exception is owned; aggregate submitted totals shall reconcile to the eligible platform population.
    
-   REG-008 \[MUST; R1\] Nigeria production data residency and cross-border processing shall be determined through legal/DPIA approval and encoded as a deployment constraint, not left as an optional blank field.
    
-   REG-009 \[MUST; R1\] The platform shall support lawful-basis/consent evidence, data-subject access/correction/erasure or restriction workflows, retention exceptions and processor records.
    
-   REG-010 \[MUST; R1\] Marketing eligibility shall be separate from service eligibility and shall honour applicable DND, opt-out, consent and frequency rules.
    
-   REG-011 \[MUST; R1\] USSD shortcode, SMS sender identity and aggregator/direct route approvals shall be recorded as programme prerequisites with expiry/renewal monitoring.
    
-   REG-012 \[MUST; R1\] The legal and compliance team shall maintain a formal watch item for the DEON litigation and any subsequent FCCPC, NCC, court or government instrument; confirmed changes shall enter controlled impact assessment.
    
-   REG-013 \[MUST; R1\] Regulatory evidence shall be producible within the applicable deadline and protected against alteration or unauthorised deletion.
    
-   REG-014 \[MUST; R1\] Product conduct monitoring shall test hidden/incorrect charges, unsolicited offers, misleading messages, repeated contact, complaint outcomes and vulnerable-customer treatment.
    
-   REG-015 \[MUST; R1\] Tax rates, bases and withholding treatment shall be effective-dated configuration approved by Finance/Tax; the platform shall retain calculation evidence per settlement.
    
-   REG-016 \[MUST; R1\] The programme responsibility matrix shall explicitly allocate disclosure delivery, consent capture, complaints intake, privacy requests, bureau reporting, regulatory reporting and marketing controls between platform and telco.
    

## 17\. Data Governance, Privacy and Automated-Decision Rights

Telco behavioural data is commercially powerful and privacy-sensitive. The platform shall use the minimum data needed for a defined credit purpose, retain lineage and freshness, control access by role and tenant, and provide correction mechanisms when source data is wrong. The privacy architecture must cover raw feeds, derived features, scores, decisions, complaints, bureau submissions and analytic datasets, not merely customer names and identifiers.

### 17.1 Data Classification and Use

| Class | Examples | Control expectation |
| --- | --- | --- |
| Restricted identity | MSISDN, stable subscriber reference, NIN value if exceptionally approved. | Tokenisation/pseudonymisation, least privilege, strong encryption, monitored access. |
| Confidential behavioural | Recharge, usage, device/SIM events, repayment behaviour, fraud signals. | Purpose limitation, tenant isolation, feature lineage, controlled analytics. |
| Financial regulated | Advance, ledger, recovery, settlement, bureau records. | Immutability, reconciliation, retention, segregation of duties. |
| Operational confidential | Credentials metadata, endpoints, incident evidence, configuration. | Secrets separation, access logging, change control. |
| Aggregated analytics | Portfolio metrics with sufficient de-identification. | Re-identification assessment, minimum cohort sizes, approved sharing. |

### 17.2 Data Lifecycle

15.  Collect under contract and documented lawful basis, using canonical schemas and minimised fields.
     
16.  Validate completeness, source, time period and consent/permission where required.
     
17.  Pseudonymise/tokenise operational identifiers where feasible and segregate direct identifiers.
     
18.  Derive features with versioned logic and traceability to source periods.
     
19.  Use features only for approved products, risk, fraud, operations and reporting purposes.
     
20.  Retain according to financial, credit, complaints, privacy and contractual schedules with legal holds.
     
21.  Correct or annotate disputed source/derived data and propagate approved corrections to decisions, reports or bureaus.
     
22.  Delete/anonymise at end of retention where no overriding duty applies, with evidence of completion.
     

DAT-001 \[MUST; R1\] A data catalogue shall identify owner, definition, source, classification, lawful purpose, retention, quality rules, tenant scope and downstream uses for each critical field and feature.

DAT-002 \[MUST; R1\] Raw and derived data shall include event/effective time and ingestion/processing time so late or corrected telco records can be handled explicitly.

DAT-003 \[MUST; R1\] Critical source feeds shall be profiled for completeness, duplicates, range, distribution, timeliness, schema drift and cross-field consistency before scoring or reconciliation use.

DAT-004 \[MUST; R1\] The platform shall support data quarantine and reprocessing without contaminating prior accepted partitions or silently changing historical decision evidence.

DAT-005 \[MUST; R1\] Access to restricted identity, behavioural, financial and cross-tenant analytics data shall be separately authorised and logged.

DAT-006 \[MUST; R1\] Training and model-development datasets shall use approved minimisation/pseudonymisation and shall be reproducible by data/model version.

DAT-007 \[MUST; R1\] A data correction workflow shall assess affected current offers, active advances, complaints, bureau submissions and regulatory reports and shall generate controlled remediation.

DAT-008 \[MUST; R1\] Retention shall be effective-dated by record class and jurisdiction; legal holds shall prevent deletion while remaining auditable.

DAT-009 \[MUST; R1\] Production data shall not be copied into non-production environments except through an approved, masked and access-controlled process.

DAT-010 \[MUST; R1\] Cross-border transfer or remote access shall require documented legal basis, DPIA/security review, approved destination and technical enforcement.

DAT-011 \[MUST; R1\] Automated-decision explanations shall be understandable to support and compliance users and shall not expose fraud-sensitive rules unnecessarily to customers.

DAT-012 \[MUST; R1\] The platform shall support customer access and correction requests without granting direct exposure to other subscribers, model IP or security-sensitive data.

## 18\. Enterprise Risk Architecture

Risk management is layered. Subscriber-level controls cannot compensate for a broken telco feed, unsafe configuration, insufficient funding or failed settlement. The enterprise framework therefore covers credit, fraud, conduct, operational, technology, data, liquidity, third-party, regulatory and model risk, each with an owner, appetite, key indicators and escalation path.

| Risk class | Illustrative risks | Primary controls |
| --- | --- | --- |
| Credit/portfolio | Over-limit, unstable affordability, rising delinquency, concentration. | Tier/limit policy, progressive trust, caps, vintage monitoring, guardrails. |
| Fraud/gaming | SIM swap, account takeover, spike manipulation, duplicate fulfilment, insider abuse. | Real-time overlays, velocity, idempotency, segregation, fraud cases. |
| Conduct/regulatory | Hidden cost, unsolicited lending, harassment, complaint breach, wrong bureau record. | Disclosure/consent evidence, message controls, complaints, reporting reconciliation. |
| Operational/technology | Telco outage, queue backlog, data loss, configuration mistake. | Isolation, durable events, circuit breakers, maker-checker, DR, runbooks. |
| Data/model | Feed corruption, drift, bias, stale model, unexplainable outcome. | Quality gates, lineage, monitoring, validation, champion/challenger governance. |
| Liquidity/funding | Pool exhausted, delayed settlement, funder covenant breach. | Reservation, buffers, forecasts, originations stop, statements. |
| Third party | Telco/aggregator/bureau/cloud failure or control weakness. | Due diligence, SLAs, certification, monitoring, exit/continuity plans. |
| Cyber/privacy | Credential theft, cross-tenant leak, ransomware, misuse of behavioural data. | Zero-trust access, encryption, secrets, monitoring, segmentation, incident response. |

### 18.1 Portfolio Guardrail Examples

| Metric | Trigger design | Automated action |
| --- | --- | --- |
| Approval rate | Deviation from approved baseline by segment and volume confidence. | Throttle or suspend affected rule/model scope. |
| Average approved limit | Step change beyond configured tolerance. | Freeze config/model rollout and revert. |
| Early delinquency / failed recovery | Exceeds threshold for recent vintages. | Reduce tiers, pause segment/product, alert Risk/Treasury. |
| Fulfilment unknown/failure | Telco adapter rate above SLO. | Open circuit, stop new instructions, continue enquiry/reconciliation. |
| Funding headroom | Below buffer or cap exceeded. | Stop originations using pool; continue recoveries. |

| Metric | Trigger design | Automated action |
| --- | --- | --- |
| Feed freshness/quality | Critical feed stale or distribution anomaly. | Suppress offers or use approved conservative fallback. |
| Complaint spike | Duplicate/unrequested/fee complaints exceed tolerance. | Pause campaign/product and initiate conduct incident. |

RSK-001 \[MUST; R1\] The Board or delegated committee shall approve risk appetite and limits for credit loss, exposure, concentration, conduct, liquidity, operations, data and third-party risk.

RSK-002 \[MUST; R1\] Each programme shall have key risk indicators, thresholds, action owners and escalation paths mapped to automated and manual controls.

RSK-003 \[MUST; R1\] Portfolio guardrails shall operate independently of normal scoring and shall be able to suspend originations by telco, programme, product, segment, model or funding pool.

RSK-004 \[MUST; R1\] A mass configuration or model incident shall trigger kill switch, evidence preservation, affected-population analysis, customer/partner remediation and controlled re-arm.

RSK-005 \[MUST; R1\] Model governance shall cover development, validation, approval, performance/drift, explainability, fairness/conduct, change, retirement and independent review.

RSK-006 \[MUST; R1\] Fraud cases shall link subscriber, device/SIM events, advances, telco evidence, complaints and financial impact and shall support watchlist/blacklist expiry and appeal.

RSK-007 \[MUST; R1\] Risk acceptance shall be time-bound, owned, approved at the appropriate level and recorded with compensating controls.

RSK-008 \[MUST; R1\] No commercial target or telco request shall bypass non-waivable conduct, tenant, ledger, privacy or funding controls.

RSK-009 \[MUST; R1\] Cross-telco serial-default risk shall be addressed through the approved design decision: bureau-mediated visibility, lawful privacy-preserving negative file, or explicit risk acceptance.

RSK-010 \[MUST; R1\] Stress tests shall cover demand surge, lower recharge/recovery, telco outage, funding delay, fee change, data degradation and adverse regulatory change.

## 19\. Target Operating Model and Governance

### 19.1 Governance Forums

| Forum | Accountability | Minimum cadence / trigger |
| --- | --- | --- |
| Board / Risk Committee | Risk appetite, funding, major telco/country entry, material incidents and regulatory posture. | Quarterly and material trigger. |
| Product & Programme Steering | Roadmap, telco commitments, economics, scope and release gates. | Monthly / release gate. |
| Credit & Model Committee | Policy, tiers, models, experiments, portfolio guardrails and losses. | Monthly and emergency trigger. |
| Configuration Change Board | Material risk/commercial/accounting configuration activation. | Weekly / urgent controlled path. |
| Architecture Review Board | System boundaries, invariants, technology, tenant model and exceptions. | Fortnightly / design gate. |
| Financial Control & Settlement | Ledger integrity, reconciliation breaks, partner settlement and close. | Daily/weekly/monthly depending process. |
| Conduct, Privacy & Complaints | Complaints, messaging, data rights, bureau disputes and regulatory reporting. | Monthly and breach trigger. |
| Service Review with Telco | SLOs, incidents, data quality, reconciliation, capacity and roadmap. | Weekly launch; monthly BAU. |

### 19.2 Segregation of Duties

-   Risk authors policy; authorised approver activates material policy; engineering cannot silently override it.
    
-   Finance owns accounting templates and settlement approval; operations may investigate but not self-approve write-offs/adjustments beyond thresholds.
    
-   Partner administrators manage telco configuration within scope; secrets are controlled by security/platform operations.
    
-   Support can view evidence and initiate cases but cannot alter disclosure, score or ledger history.
    
-   Data science develops models; independent validation approves; production release follows model governance.
    
-   Platform super-user and break-glass access is time-bound, monitored and reviewed.
    

GOV-001 \[MUST; R1\] Every material business process shall have accountable owner, control owner, operator, approver, evidence and escalation path.

GOV-002 \[MUST; R1\] Role and approval matrices shall be configurable by legal entity/telco/programme but shall enforce minimum segregation for risk, finance, security and customer redress.

GOV-003 \[MUST; R1\] Break-glass access shall require justification, time-limited elevation, enhanced logging and post-use review.

GOV-004 \[MUST; R1\] Material incidents shall have one incident commander and named business, telco, compliance, finance and communications contacts as applicable.

GOV-005 \[MUST; R1\] Telco contracts and operating procedures shall use the same system-of-record and responsibility terminology as this blueprint.

GOV-006 \[MUST; R1\] Enterprise requirements marked MUST shall not be downgraded by a project team without formal architecture/risk approval and documented rationale.

## 20\. Telco Onboarding, Integration Certification and Simulator

Real telco connectivity often becomes available late in commercial negotiations. The platform must therefore be buildable, demonstrable and certifiable against a realistic simulator. The simulator is not a disposable mock: it is a standing assurance product used for adapter development, fault injection, regression, capacity tests and telco certification.

### 20.1 Onboarding Lifecycle

23.  Commercial and regulatory qualification: programme, responsibilities, funding, data, channel, licence and economics.
     
24.  Data discovery: source attributes, cadence, history, quality, corrections, NIN/KYC flags, SIM/port/recycle events.
     
25.  Canonical mapping and adapter design: APIs, files, events, authentication, rate limits, status enquiry and reversal.
     
26.  Simulator development and contract tests before production connectivity.
     
27.  Sandbox integration and joint certification using normal and adverse scenarios.
     
28.  Historical-data backfill, feature/scoring dry runs and population reconciliation.
     
29.  Dual run against incumbent or telco benchmark, without double fulfilment.
     
30.  Controlled pilot by segment/region/denomination with guardrails and daily review.
     
31.  Progressive rollout, financial cutover reconciliation and BAU service handover.
     

### 20.2 Simulator Fault Catalogue

| Scenario | Simulator behaviour | Expected platform result |
| --- | --- | --- |
| Timeout after success | Accept fulfilment, suppress response, later status says success. | FULFILMENT\_UNKNOWN then ACTIVE; no duplicate credit. |
| Duplicate fulfilment request | Receive same idempotency key twice. | Single network/economic effect; repeat returns original result. |
| Out-of-order recovery reversal | Send reversal before original recovery. | Pending linkage; eventual correct ledger, no negative corruption. |

| Scenario | Simulator behaviour | Expected platform result |
| --- | --- | --- |
| Malformed/partial feed | Missing columns, bad types, truncated file. | Reject/quarantine; no partial silent score update. |
| Slow responses/rate limit | Variable latency, 429/limit, connection reset. | Circuit breaker/backoff; tenant isolation; customer-safe response. |
| Contradictory status | Initial failure then later success evidence. | Controlled correction, exposure/ledger repair and complaint evidence. |
| Number recycling/port | Change identity period/status. | Prior debt not assigned to new holder; offers suppressed pending resolution. |
| Mass event replay | Replay hours/days of recharge events. | Idempotent recovery and durable backlog processing. |
| Settlement mismatch | Statement omits or duplicates transactions. | Break creation, ageing and evidence; no force match. |
| Traffic surge | Peak requests plus batch scoring/reconciliation. | SLO/capacity controls and no cross-tenant starvation. |

TEL-001 \[MUST; R1\] Every telco shall integrate through an independently versioned adapter that conforms to canonical platform contracts.

TEL-002 \[MUST; R1\] A standing telco simulator shall be delivered in R0/R1 and shall support normal behaviour, configurable latency, errors, duplicates, out-of-order events and fault scenarios from Appendix A.

TEL-003 \[MUST; R1\] Adapter certification shall include contract, idempotency, security, performance, failover, status-enquiry, reconciliation and negative/fault tests.

TEL-004 \[MUST; R1\] Production credentials, certificates and network routes shall be independent from sandbox and shall be rotated/monitored under security policy.

TEL-005 \[MUST; R1\] Telco onboarding shall define source-data quality SLAs, correction processes, historical backfill, operational contacts and change-notification periods.

TEL-006 \[MUST; R1\] A telco API or file schema change shall be versioned, backward-compatible where possible, certified in simulator/sandbox and deployed through controlled rollout.

TEL-007 \[MUST; R1\] Dual running and migration shall prevent both incumbent and new platform from fulfilling the same customer request; routing ownership shall be explicit at every cutover stage.

TEL-008 \[MUST; R1\] Joint certification evidence shall be retained and linked to the adapter/programme version approved for production.

TEL-009 \[MUST; R1\] The platform shall provide telcos with tenant-scoped operational, reconciliation and service-health views without exposing other tenants or internal model IP beyond contract.

TEL-010 \[MUST; R1\] Adapter failure shall open only the affected telco/programme circuit and shall not block durable ingestion of recoveries or reversals where a safe route remains available.

## 21\. Service Management, Resilience and Degraded Modes

Availability targets must be internally consistent with recovery design and cost. Release 1 adopts a realistic 99.9% monthly target for core customer decision/origination service, an RTO of 30 minutes for a major regional/service failure, and near-zero committed-ledger RPO through appropriate transactional replication and durable event handling. A 99.99% target is a scale-phase objective requiring materially faster automated failover and additional cost.

| Service class | Release 1 target / principle | Degraded-mode priority |
| --- | --- | --- |
| Offer/decision API | 99.9% monthly; normal p95 target defined in Volume 2. | May fail closed/unavailable rather than use unsafe stale data. |
| Origination/fulfilment orchestration | 99.9%; no blind retry; idempotent. | Stop new originations for affected telco; resolve unknowns. |
| Recovery/reversal ingestion | Durable at-least-once; no accepted event loss. | Queue/backpressure and replay; highest data-durability priority. |

| Service class | Release 1 target / principle | Degraded-mode priority |
| --- | --- | --- |
| Ledger posting | Strong consistency for committed financial events; RPO near zero. | Pause dependent processing rather than accept unbalanced/lost posting. |
| Portals/reporting | Lower availability class acceptable. | Read-only/deferred reports; core processing continues. |
| Batch scoring/analytics | Recoverable within scoring SLA. | Use last valid offer only within explicit freshness policy or suppress. |
| DR | RTO (\\leq)30 minutes for R1 critical services; tested. | Prioritise ledger, recovery ingestion, telcostatus/reconciliation, then new originations. |

RES-001 \[MUST; R1\] Release 1 service objectives shall be 99.9% monthly for core decision/origination, RTO not greater than 30 minutes for critical services, and near-zero RPO for committed ledger events; any higher target requires approved architecture/cost.

RES-002 \[MUST; R1\] The platform shall define service classes and degraded modes that prioritise financial integrity and recovery-event durability over new offer availability.

RES-003 \[MUST; R1\] Recovery and reversal events accepted at the platform boundary shall be durably stored before acknowledgement and replayable after downstream outage.

RES-004 \[MUST; R1\] Each telco adapter and major shared dependency shall have health, saturation, latency, error, backlog and circuit-breaker monitoring.

RES-005 \[MUST; R1\] Disaster-recovery tests shall prove restoration, ledger integrity, event replay, telco routing, reconciliation and security controls at least annually and after material change.

RES-006 \[MUST; R1\] Backups shall be encrypted, immutable where appropriate, restoration-tested and tenant/jurisdiction aware.

RES-007 \[MUST; R1\] Batch scoring, reporting or replay workloads shall not exhaust resources reserved for real-time decisions, fulfilment resolution or recovery ingestion.

RES-008 \[MUST; R1\] The platform shall support planned telco maintenance windows and programme-specific suspension messages without disabling other tenants.

RES-009 \[MUST; R1\] A 99.99% target shall be introduced only when failover design and operational evidence can meet an RTO consistent with the annual error budget.

RES-010 \[MUST; R1\] Major incidents shall include customer/telco impact assessment, financial exposure, unresolved unknown fulfilments, recovery backlog, regulatory triggers and reconciliation plan.

## 22\. Reporting, Analytics and Unit Economics

| Audience | Required views |
| --- | --- |
| Executive / Board | Active subscribers, approvals, advances, exposure, recovery, loss, profitability, telco performance, funding utilisation, incidents and regulatory posture. |
| Risk | Score/tier distribution, approval funnel, vintage, roll rates, cure, write-off, concentration, model drift, guardrails and exceptions. |
| Finance / Treasury | Ledger control totals, funding pools, fee/revenue/tax, partner shares, reconciliation, settlement, cash/inventory position and forecasts. |
| Telco | Tenant-scoped demand, fulfilment, recovery, usage/recharge uplift, service levels, reconciliation and customer issues. |
| Compliance / DPO | Disclosures, consent, complaints, SLA, marketing/DND, rights requests, bureau submissions, audits and breaches. |
| Operations / Technology | Latency, errors, unknown fulfilments, queue/backlog, data quality, adapter health, configuration changes and incident trends. |

| Audience | Required views |
| --- | --- |
| Product | Journey funnel, abandonment, offer utilisation, channel/language, repeat borrowing, customer outcomes and experimentation. |

### 22.1 Cost Attribution

-   Compute/storage/messaging cost by tenant and workload.
    
-   USSD session, SMS, app/API and aggregator charges.
    
-   Telco fulfilment/inventory cost and settlement timing.
    
-   Funding cost and liquidity buffer.
    
-   Expected and realised credit loss/write-off.
    
-   Bureau, KYC/privacy/compliance and support costs.
    
-   Tax and partner revenue share.
    
-   Cost per scored profile, decision, successful advance, active exposure and recovered naira.
    

REP-001 \[MUST; R1\] Every report and dashboard shall state source, refresh time, filters, currency, metric definition and whether values are ledger-final or provisional.

REP-002 \[MUST; R1\] Tenant-scoped reports shall enforce the same authorisation and row-level isolation as transactional services.

REP-003 \[MUST; R1\] Portfolio reports shall support telco, programme, product, funder, tier, score band, geography/segment where lawful, channel, vintage and time dimensions.

REP-004 \[MUST; R1\] The platform shall provide cost attribution and unit economics per telco/programme/product, including communication and infrastructure costs.

REP-005 \[MUST; R1\] Regulatory, bureau and partner reports shall reconcile to ledger/authoritative populations and retain submission/version evidence.

REP-006 \[MUST; R1\] Metrics used for guardrails shall be computed on controlled definitions and shall not be altered through dashboard-only logic.

REP-007 \[MUST; R1\] Data exports shall be access-controlled, watermarked/labelled where appropriate, time-limited and audited.

REP-008 \[MUST; R1\] The platform shall support a data-quality dashboard showing feed freshness, completeness, rejected records, distribution shifts and unresolved corrections.

## 23\. Security, Access and Third-Party Assurance

-   Zero-trust service and user authentication; mTLS or equivalent for telco and high-trust integrations.
    
-   Least privilege RBAC plus attribute/tenant/programme constraints; periodic recertification.
    
-   Encryption in transit and at rest; managed keys and optional tenant-specific keys.
    
-   Secrets manager/HSM-backed key lifecycle; no plaintext credentials in code, configuration tables or logs.
    
-   Pseudonymisation/tokenisation for MSISDN and restricted identifiers in non-channel services where feasible.
    
-   Tamper-evident audit logs for authentication, data access, decisions, configuration, financial actions and exports.
    
-   Secure SDLC, code review, dependency scanning, penetration testing, threat modelling and incident response.
    
-   Third-party due diligence and contract controls for telcos, aggregators, cloud, bureaus, messaging and funders.
    
-   Data-loss prevention, egress controls and approval for bulk extracts.
    
-   Privileged access monitoring, just-in-time elevation and break-glass review.
    

SEC-001 \[MUST; R1\] All external telco, bureau, funder and finance integrations shall use strong mutually authenticated and encrypted channels appropriate to risk.

SEC-002 \[MUST; R1\] User and service authorisation shall include tenant/programme scope and deny by default; cross-tenant access shall require an explicitly approved role.

SEC-003 \[MUST; R1\] Restricted identifiers and financial data shall be encrypted at rest and in transit; keys shall be centrally managed, rotated and access-logged.

SEC-004 \[MUST; R1\] Secrets shall be stored only in an approved secrets-management facility and referenced by configuration.

-   SEC-005 \[MUST; R1\] Audit records for security, configuration, decisioning and financial actions shall be immutable/tamper-evident, time-synchronised and retained per policy.
    
-   SEC-006 \[MUST; R1\] The platform shall detect and alert on tenant-context mismatch, unusual bulk access, privileged activity, credential abuse and abnormal export patterns.
    
-   SEC-007 \[MUST; R1\] Production releases shall pass secure-SDLC controls including dependency, code, container/infrastructure, secret and vulnerability scanning.
    
-   SEC-008 \[MUST; R1\] Critical vulnerabilities and penetration-test findings shall have risk-based remediation SLAs and production launch gates.
    
-   SEC-009 \[MUST; R1\] Third parties with data or transaction access shall undergo security/privacy due diligence, contractual controls, incident notification and periodic review.
    
-   SEC-010 \[MUST; R1\] Security incidents shall integrate with financial reconciliation, fraud, privacy breach assessment, telco notification and customer redress where relevant.
    
-   SEC-011 \[MUST; R1\] Non-production environments shall use synthetic or appropriately masked data and separate credentials/keys.
    
-   SEC-012 \[MUST; R1\] Bulk exports and regulatory files shall use controlled destinations, encryption, integrity checks and complete audit trail.
    

## 24\. Incumbent Migration and Cutover Strategy

Replacing an incumbent requires migration of more than software. Existing outstanding advances, historical repayment behaviour, eligibility/limits, customer disclosures, telco routing, settlement positions and complaints may sit across different systems. The cutover objective is not merely to switch the USSD route; it is to preserve financial truth, prevent duplicate fulfilment/recovery and establish a reconciled opening position.

32.  Discovery and data reconciliation: inventory incumbent/telco/platform records, definitions, fees, states, write-offs and historical quality.
     
33.  Migration policy: decide which history, active exposure, score features, complaints and evidence can/shall transfer and under what legal basis.
     
34.  Canonical transformation: map identities, advances, recoveries, outstanding, fees, dates, references and source confidence.
     
35.  Opening ledger: create controlled migration/opening entries with balancing and total reconciliation; do not fabricate transaction-level history where unavailable.
     
36.  Dry runs: repeat extraction/transformation/loading, reconcile counts/amounts, test idempotency and rollback.
     
37.  Dual run/shadow score: compare offers and outcomes without dual fulfilment; investigate material differences.
     
38.  Routing cutover: define customer segmentation and one authoritative originator at every moment.
     
39.  Recovery continuity: ensure recharge deductions for pre-cutover advances route to the correct owner and ledger.
     
40.  Hypercare: daily reconciliation, complaints, unknown fulfilment, portfolio and settlement review with telco/incumbent where required.
     
41.  Decommission/retention: confirm contractual data return, regulatory retention, access removal and audit evidence.
     

MIG-002 \[MUST; R1\] Opening exposure and financial positions shall reconcile by telco, product, status, component and total before production acceptance.

MIG-003 \[MUST; R1\] Uncertain or incomplete incumbent records shall be quarantined or flagged and shall not silently receive invented precision.

MIG-004 \[MUST; R1\] Dual running shall never permit both incumbent and new platform to fulfil the same request or apply the same recovery economically twice.

MIG-005 \[MUST; R1\] Cutover shall include a documented routing matrix, freeze/catch-up windows, rollback criteria, partner contacts and financial close plan.

MIG-006 \[MUST; R1\] Migrated customers shall retain fair treatment; a new holder of a recycled number shall not inherit prior exposure.

-   MIG-007 \[MUST; R1\] The platform shall retain migration batch, source checksums, record counts, rejected items, approvals and reconciliation evidence.
    
-   MIG-008 \[MUST; R1\] Production migration shall be preceded by at least two successful dress rehearsals using representative volume and failure scenarios.
    
-   MIG-009 \[MUST; R1\] Post-cutover hypercare shall monitor customer complaints, duplicate/unknown fulfilment, recovery mismatch, exposure drift and settlement breaks daily.
    
-   MIG-010 \[MUST; R1\] Incumbent decommission shall not occur until retention, legal hold, audit access, final settlement and data-destruction/return obligations are satisfied.
    

## 25\. Release and Capability Roadmap

![image](https://static-us-img.skywork.ai/prod/nexus/1784233129/cropped_image_2_1784233129591178642.jpg)

Each release has explicit entry/exit criteria; no release weakens ledger, security, tenant-isolation or regulatory evidence invariants.

| Release | Included capability | Exit criteria |
| --- | --- | --- |
| R0 - Foundation | Canonical contracts; product/config model; identity/tenant; ledger prototype; simulator; secure delivery pipeline; decision/register. | Architecture and legal responsibility baseline approved; simulator passes core fault scenarios; balanced ledger invariants proven. |
| R1 - First Production Programme | One telco; airtime advance; USSD/SMS; scheduled scoring + real-time overlays; full advance/ledger/recovery/reconciliation; complaints/privacy; bureau capability dormant/enableable; admin/risk/finance/ops portals. | Pilot and scale acceptance; financial positions reconcile; DR/security/regulatory evidence complete; operating team ready. |
| R2 - Product/Finance Expansion | Data/bundle products; active bureau submission where required; automated treasury/funder statements; richer collections and analytics; second adapter certification. | Product-specific controls and bureau reconciliations accepted; treasury and unit economics proven. |
| R3 - Multi-Telco Scale | Second/third telco production; isolation scaling; dedicated deployment option; advanced models; 99.99% track where justified. | No cross-tenant leakage/contagion; capacity and multi-telco settlement proven; faster failover evidence. |
| R4 - Multi-Country | Jurisdiction packs, currencies, languages, legal entities, residency and cross-border governance; future product families. | Country legal/regulatory approval and country-specific certification; no weakening of global invariants. |

REL-001 \[MUST; R0/R1\] Release 1 shall prioritise one complete airtime programme over shallow simultaneous multi-telco/product support.

REL-002 \[MUST; R0/R1\] Every release shall have entry/exit criteria covering product, financial, risk, compliance, security, resilience, data, operations and partner readiness.

REL-003 \[MUST; R0/R1\] No release may defer append-only ledger, idempotency, disclosure/consent evidence, tenant isolation, recovery reconciliation or complaint capability for an active production product.

REL-004 \[MUST; R0/R1\] Future-release architecture hooks shall not become unfinished production complexity unless required to avoid material rework.

REL-005 \[MUST; R0/R1\] Pilot exposure, segment, denomination, geography/channel and duration shall be capped and monitored through configurable guardrails.

REL-006 \[MUST; R0/R1\] Progressive rollout shall require daily launch review of approvals, fulfilment, unknowns, recovery, complaints, data quality, losses, funding and settlement.

REL-007 \[MUST; R0/R1\] Rollback shall distinguish channel/origination stop from financial rollback; posted economic events shall be corrected, not deleted.

REL-008 \[MUST; R0/R1\] A release shall not be declared complete solely on functional demonstration; it must provide traceable acceptance evidence for all mandatory requirements in scope.

## 26\. Decisions Required Before Detailed Design Freeze

The following decisions must be resolved, or explicitly time-bound as assumptions, before detailed design commits the programme to an irreversible operating model. The decision register should record options, evidence, owner, approvers, date, consequences, review trigger and superseded decisions.

| ID | Decision | Question / outcome required | Owner(s) |
| --- | --- | --- | --- |
| DD-01 | Licensed/contracting legal entity | Which entity is lender/credit operator of record, and what licences/registrations and customer naming apply? | Board / Legal / Compliance |
| DD-02 | First telco and programme scope | Exact operator, subscriber population, product, channels and pilot scope. | Executive / Partnerships / Product |
| DD-03 | Funding model | Telco inventory, own balance sheet, third-party funder or hybrid; who bears losses and settlement timing? | CFO / Treasury / Legal |
| DD-04 | Fee and tax structure | Upfront deduction vs added fee, total-cost presentation, VAT/WHT and revenue share. | Finance / Tax / Legal / Product |
| DD-05 | USSD route and ownership | Dedicated/shared shortcode, direct telco gateway vs aggregator, session charges, menu ownership and support. | Partnerships / Product / Technology / Legal |
| DD-06 | Data contract | Exact fields, history, cadence, NIN/KYC flags, SIM/port/recycle events, corrections, retention and derived-data rights. | Data / Risk / Privacy / Telco |
| DD-07 | Scoring cadence and population | Daily/weekly/incremental segmentation, freshness threshold and conservative fallback. | Risk / Data / Technology |
| DD-08 | Initial tiers and limits | Denominations, score mapping, one-tier movement, affordability caps and high-tier trust requirements. | Credit Risk / Product / Treasury |
| DD-09 | Concurrent advances | Default one; whether any segment/product may hold more, aggregate cap and recovery waterfall. | Risk / Product / Finance |
| DD-10 | Recovery mechanics | Synchronous recharge allocation vs event/file; deductions, partial recharge treatment, reversal and overpayment. | Telco / Finance / Technology |
| DD-11 | Collections/write-off | Ageing, reminders, contact caps, write-off and post-write-off recovery. | Risk / Collections / Compliance / Finance |
| DD-12 | Cross-telco default visibility | Bureau-mediated, privacy-preserving negative file with consent/contracts, or none/risk acceptance. | Risk / DPO / Legal / Telcos |
| DD-13 | Credit bureau strategy | Which licensed bureaux, reporting trigger/cadence, data fields, dispute/correction and dormant launch posture. | Compliance / Risk / Legal |
| DD-14 | Nigeria regulatory baseline | Final DEON/FCCPC posture, registration/approval, NCC/VAS requirements and court/agency changes. | Legal / Compliance |
| DD-15 | Data residency and cloud | Nigeria hosting constraint, cloud/provider, remote access, backups and cross-border controls. | DPO / CISO / Architecture / Legal |
| DD-16 | Availability/cost target | Confirm R1 99.9%/RTO 30m/ledger RPO near zero and approved path/cost to 99.99%. | CTO / COO / CFO |
| DD-17 | Tenant deployment level | Shared partitioned, dedicated store or dedicated deployment for first telco. | Architecture / CISO / Telco |
| DD-18 | Accounting point and templates | When receivable/fee/revenue/tax are recognised and how telco inventory/funder economics post. | CFO / Accounting / Auditor |

| ID | Decision | Question / outcome required | Owner(s) |
| --- | --- | --- | --- |
| DD-19 | Settlement design | Cycle, netting, cash vs inventory, invoices, thresholds, bank accounts and dispute SLA. | Finance / Telco / Funder |
| DD-20 | Customer languages | Release 1 languages, translation/legal approval and fallback. | Product / Compliance / Telco |
| DD-21 | Complaint operating model | Platform direct, telco first line, shared intake; SLAs, evidence, redress and regulator reports. | Compliance / Customer Ops / Telco |
| DD-22 | Self-service and exclusion | Channels for statements, marketing opt-out and borrowing self-exclusion; cooling-off if any. | Product / Compliance |
| DD-23 | Fraud-data scope | Device/SIM attributes, blacklists, telco fraud feeds, external intelligence and lawful basis. | Fraud / DPO / Telco |
| DD-24 | Migration population | Active advances, write-offs, repayment history, scores, consent evidence and complaints to migrate. | Programme / Finance / Legal / Risk |
| DD-25 | Incumbent cutover | Dual run, routing switch, recovery ownership, final settlement and incumbent cooperation. | Programme / Telco / Legal / Finance |
| DD-26 | Portfolio guardrail thresholds | Approval/limit/delinquency/fulfilment/funding/data triggers and authorised re-arm. | Risk / Treasury / Operations |
| DD-27 | Operational support model | 24x7 coverage, telco NOC interface, incident authority, on-call and outsourcing. | COO / CTO / Telco |
| DD-28 | Build/buy technology choices | Core database, event bus, rules/model tooling, observability, IAM and analytics. | Architecture / CTO / CISO |
| DD-29 | Data retention schedule | Financial, decision, telco source, complaints, bureau, audit and security retention. | Legal / DPO / Finance / CISO |
| DD-30 | Future-country/product gating | Criteria for expanding beyond Nigerian airtime/data and governance for new regulatory packs. | Board / Product / Risk / Legal |

> GOV-007 \[MUST; R0\] All design-freeze decisions shall be resolved or explicitly recorded as time-bound assumptions before production design approval.

GOV-008 \[MUST; All\] A decision that changes lender identity, customer economics, funding, data use, regulatory responsibility or system-of-record ownership shall trigger formal impact assessment and document revision.

## 27\. External Review Closure Matrix

The v2 external review concluded that the financial core, edge cases, configuration governance and multi-telco model were strong, while identifying material gaps. The following matrix shows how each active finding is treated in this volume and where additional implementation detail will appear.

| Finding | Issue | Disposition | Volume 1 closure | Remaining detail |
| --- | --- | --- | --- | --- |
| F-3 | Credit bureau reporting incorrectly deferred | Accepted | REG-006/007; R1 capability, configurable/dormant until enabled. | Volume 2 interfaces; Volume 3 operations. |
| F-4 | Nigeria regulatory obligations too generic | Accepted with legal-status qualification | Section 16 Nigeria annex and REG-001-016; unresolved claims treated as watch items. | Detailed legal control mapping maintained outside code. |
| F-5 | USSD underspecified | Accepted | Section 10, CHN-001-012; session, timeout, fallback, language, cost and route decisions. | Volume 2 sequence/API; Volume 3 test scripts. |
| F-6 | Concurrent-advance policy ambiguous | Accepted | PRD-005, ADV-007/008; default one, configurable only with approval. | Atomic implementation in Volume 2. |

| Finding | Issue | Disposition | Volume 1 closure | Remaining detail |
| --- | --- | --- | --- | --- |
| F-7 | Collections not specified | Accepted | Section 13, COL-001-012. | Volume 3 procedures and contact strategy. |
| F-8 | Simulator not required | Accepted | Section 20, TEL-002/003; R0/R1 standing simulator. | Volume 2 simulator contracts; Volume 3 certification. |
| F-9 | 99.99% vs RTO 30m inconsistent | Accepted | Section 21; R1 99.9%, RTO 30m, ledger RPO near zero; 99.99% scale target. | Volume 2 topology; Volume 3 DR evidence. |
| F-10 | Cross-telco serial defaulter blind spot | Accepted as explicit decision | SOR-007, RSK-009, DD-12. | Legal/privacy and bureau design required. |
| F-11 | No MVP/phasing | Accepted | Section 25, REL-001-008. | Delivery plan in Volume 3. |
| F-12 | OFFERED in advance FSM | Accepted | Section 12 separates Offer and Advance; advance starts REQUESTED. | State machine implementation in Volume 2. |
| F-13 | Ledger balance invariant not explicit | Accepted | LED-003 non-configurable balance validation at activation and posting. | Posting schema/tests in Volume 2/3. |
| F-14 | Prose/edge cases not numbered | Accepted | Binding requirements and EDG-001-030 in Appendix A. | Full traceability matrix in Volume 3. |
| F-15 | Funding/treasury thin | Accepted | Section 14, TRE-001-010, funding-cost event. | Detailed ledger and pool model in Volume 2. |
| F-16 | Portfolio automatic guardrails | Accepted | CRD-013/014 and Section 18. | Metrics/automation in Volume 2. |
| F-17 | Subscriber self-service rights missing | Accepted | CHN-009/010 and Sections 10/17. | Channel flows in Volume 2. |
| F-18 | Notification requirements missing | Accepted | CHN-006-010. | Provider adapters/templates in Volume 2. |
| F-19 | No unit-economics NFR | Accepted | PRD-009, FIN-010, REP-004. | Cost telemetry in Volume 2. |
| F-20 | Nigeria tax generic | Accepted with adviser confirmation | FIN-006, REG-015 and DD-04. | Tax engine detail in Volume 2. |

## 28\. Requirement Summary and Priority Convention

Requirements use domain IDs and contain a priority/release marker. MUST means mandatory for the stated release or before its gate. SHOULD means expected unless a recorded risk/architecture exception exists. MAY means optional capability. R0 is foundation; R1 is first production programme; R2+ is expansion. Requirements labelled All remain invariant across releases.

| Domain | Prefix | Purpose |
| --- | --- | --- |
| Enterprise / Business | ENT, BUS, CAP | Mission, scope, ownership and capability model. |
| Configuration / Governance | CFG, GOV | Admin-driven variability, approval and design decisions. |
| Product / Channel | PRD, CHN | Product economics, USSD/SMS and customer rights. |
| Systems / Tenancy | SOR, TEN, TEL | Records, identity, isolation, adapters and certification. |

| Domain | Prefix | Purpose |
| --- | --- | --- |
| Credit / Advance / Collections | CRD, ADV, COL | Decisioning, anti-gaming, lifecycle and delinquency. |
| Treasury / Finance / Ledger | TRE, FIN, LED | Funding, accounting, reconciliation and settlement. |
| Regulatory / Data / Security | REG, DAT, SEC | Nigeria conduct, privacy, reporting and security. |
| Risk / Resilience / Reporting | RSK, RES, REP | Portfolio guardrails, continuity and analytics. |
| Migration / Release / Edge | MIG, REL, EDG | Cutover, phasing and exceptional behaviour. |

### Traceability Rule

A backlog item may refine a requirement but shall not silently weaken it. Each mandatory requirement must map to design, implementation, test evidence, operating control and owner before release acceptance.

## 29\. Assumptions, Dependencies and Constraints

| Type | Statement | Treatment |
| --- | --- | --- |
| Assumption | The platform company will obtain/maintain the required legal authority to operate the first programme. | Launch gate; no production lending without confirmation. |
| Dependency | Telco supplies timely, accurate subscriber, behavioural, fulfilment and recovery data and supports status enquiry/reconciliation. | Contract/SLA, simulator and data-quality controls. |
| Assumption | A verified NIN/SIM-registration flag is sufficient for most scoring; raw NIN is not required. | Validate with telco/legal; minimise data. |
| Dependency | USSD/SMS route and shortcode/sender approvals are available for pilot and production. | DD-05 and programme prerequisite. |
| Constraint | New credit must respond quickly enough for USSD, while full scoring cannot run synchronously over raw history. | Precomputed offers plus real-time overlays. |
| Constraint | Cross-store atomicity with telco systems is impossible. | State machines, idempotency, status enquiry and reconciliation. |
| Dependency | Funding capacity and settlement accounts are available before launch. | Funding-pool launch gate and treasury controls. |
| Assumption | Credit bureau integration can be built before final activation/mandate. | Configurable connector and dormant capability. |
| Constraint | Nigeria legal position may change after document date. | REG-012 watch and controlled impact assessment. |
| Dependency | Incumbent/telco provides enough data/evidence for migration and opening reconciliation. | Migration discovery; quarantine uncertainty. |
| Constraint | The platform must preserve tenant trust while managing potential cross-telco default. | Default isolation; explicit DD-12 decision. |
| Assumption | Release 1 is one telco and airtime advance, even though the core is multitelco/product capable. | REL-001 scope discipline. |

## 30\. Regulatory Source and Watch Register

The legal/compliance team shall replace or supplement this high-level register with a controlled legal obligations register. Sources below are included to anchor the current blueprint and must be revalidated before launch.

| Ref | Source / authority | Use in this blueprint | Status as of document date |
| --- | --- | --- | --- |
| SRC-01 | Federal Competition and Consumer Protection Commission - Digital, Electronic, Online or Non-Traditional Consumer Lending Regulations 2025 and associated guidance. | Scope including airtime/data; disclosure, opt-in, responsible conduct, complaints, records and reporting capabilities. | Implementation/enforcement subject to current court proceedings; monitor. |
| SRC-02 | FCCPC public releases of 22 May and 6 June 2026 concerning the Federal High Court proceedings. | Legal-status caution and 20 July 2026 hearing watch item. | Current at 16 Jul 2026; verify subsequent orders. |
| SRC-03 | Nigeria Data Protection Act 2023 / Nigeria Data Protection Commission materials. | Lawful processing, privacy governance, rights, DPIA, processor and transfer controls. | In force; legal mapping required. |
| SRC-04 | Nigerian Communications Commission licensing/USSD/SMS/VAS instruments and determinations. | Shortcode, aggregator/direct route, sender/DND and telco-channel requirements. | Programme-specific confirmation required. |
| SRC-05 | Central Bank of Nigeria credit reporting / credit bureau framework and licensed bureau requirements. | Bureau connector and reporting/correction operating model. | Applicability to lending entity/product to be confirmed. |
| SRC-06 | Telco partnership, data processing, funding and settlement agreements. | Binding responsibility, data, service, financial and liability arrangements. | Not yet final; design-freeze dependency. |
| SRC-07 | Qualified Nigerian legal, privacy, tax and accounting advice. | Final licence, tax, data residency, disclosure, reporting and accounting positions. | Mandatory before production. |

## 31\. Glossary

| Term | Definition |
| --- | --- |
| Advance | A customer-accepted credit contract resulting in telco value fulfilment and an outstanding recoverable balance. |
| Affordability | Evidence-based estimate of sustainable recovery capacity, distinct from probability of repayment. |
| Canonical contract | Operator-neutral platform representation translated by telco adapters. |
| Configuration version | Immutable effective-dated rules/settings applied to a decision or event. |
| DEON | Digital, Electronic, Online or Non-Traditional consumer lending framework referenced by the FCCPC. |
| Exposure | Outstanding or reserved economic risk attributable to subscriber/programme/funding source. |
| Fulfilment | Telco action that credits airtime/data/bundle value to a subscriber. |
| FULFILMENT\_UNKNOWN | State where telco may have succeeded but the platform lacks authoritative confirmation; blind retry prohibited. |
| Garnishment / interception | Telco deduction of all or part of a recharge to recover an outstanding advance under the programme terms. |
| Idempotency | Property that duplicate delivery of the same request/event causes one economic effect. |
| Ledger | Append-only balanced financial event record from which authoritative financial positions are derived. |
| MSISDN | Mobile subscriber number; a routing attribute that may be ported or recycled, not a perpetual person ID. |
| Offer snapshot | Time-bound immutable terms and approved amount options presented to a subscriber. |

| Term | Definition |
| --- | --- |
| Programme | Configured commercial/regulatory arrangement linking legal entity, telco, products, funding, policies and settlement. |
| Recovery | Application of eligible value/cash to reduce outstanding advance components. |
| Subscriber account | Internal telco-scoped identity period used for decisions, exposure and history. |
| Telco adapter | Independent component translating canonical platform contracts to one operator's APIs/files/events. |
| Telco System of Record | Authoritative source for subscriber/network status, balances, fulfilment and recharge facts. |
| Tenant | A telco/operator boundary for data, configuration, users, integrations, operations and finance. |
| Tier | Configurable trust/risk band mapped to product offer limits; upward movement is constrained. |
| Write-off | Accounting recognition that exposure is unlikely to recover; it does not delete the debt history or prevent lawful later recovery. |

## 32\. Enterprise Baseline Conclusion

This blueprint defines a controlled path to an Optasia-class telco credit platform without copying incumbent assumptions blindly. It retains the strengths of SRS v2 - ledger integrity, idempotency, multi-telco architecture, configuration governance and edge-case thinking - while making the Nigeria regulatory, USSD, collections, treasury, bureau, simulator, phasing and operational responsibilities explicit.

The decisive design choices are: the telco owns rails and source network facts; the platform owns the credit decision, advance and financial truth; offers are precomputed but protected by real-time safety controls; isolated recharge spikes do not create uncontrolled limits; one active advance is the default; ambiguous fulfilment is reconciled rather than retried; configuration is powerful but governed; and Release 1 is a complete one-telco airtime product with production-grade financial and conduct controls.

### Approval Statement

Approval of this volume authorises detailed technical and operational design within its constraints. It does not approve unresolved legal, commercial, funding, tax, telco-interface or design-freeze decisions. Those items require named owners and formal closure before production launch.

## Appendix A - Numbered Edge-Case Catalogue

| ID | Scenario | Required behaviour |
| --- | --- | --- |
| EDG-001 | Duplicate USSD confirmation | Same idempotency key creates one advance and replays original result. |
| EDG-002 | Concurrent requests from USSD and app | Atomic concurrency/exposure control approves at most the allowed number; other request receives deterministic decline/current status. |
| EDG-003 | Session ends before confirmation | No advance or exposure remains; offer may expire normally. |
| EDG-004 | Session ends after confirmation before response | Economic process continues; SMS/status enquiry gives outcome; no second advance. |
| EDG-005 | Telco timeout after successful credit | FULFILMENT\_UNKNOWN; status enquiry/reconciliation resolves; no blind retry. |
| EDG-006 | Telco returns failure but later evidence shows success | Controlled correction activates exposure/ledger, informs customer and records incident. |

| ID | Scenario | Required behaviour |
| --- | --- | --- |
| EDG-007 | Platform crashes after telco success before local update | Outbox/status/reconciliation recovers to ACTIVE exactly once. |
| EDG-008 | Platform approves but telco never receives instruction | Reservation expires/retries only under safe unsent evidence; no customer liability. |
| EDG-009 | Duplicate fulfilment callback | Idempotent state/ledger; duplicate audited. |
| EDG-010 | Partial airtime/data fulfilment | Policy-specific: reject/reverse whole or recognise exact delivered value only where supported; exception raised. |
| EDG-011 | Offer expires between menu selection and confirmation | Reject safely and present refreshed offer; no stale contract. |
| EDG-012 | Risk flag/SIM swap after daily scoring | Real-time overlay suppresses/reduces offer immediately. |
| EDG-013 | One unusually large recharge before scoring | Spike capped; one-tier maximum; anomaly feature retained. |
| EDG-014 | Corrupt or incomplete scoring feed | Quarantine; no silent partial update; suppress or conservative fallback by policy. |
| EDG-015 | Late corrected recharge history | Recompute prospectively; assess material prior decisions/reports; do not mutate historical evidence. |
| EDG-016 | Number ported to another telco | Close/transition telco-scoped identity period; no automatic cross-tenant merge. |
| EDG-017 | MSISDN recycled to a new person | Create new identity period; prevent prior debt, messages or bureau data attaching to new holder. |
| EDG-018 | Recharge event delivered twice | Single recovery through source-event idempotency. |
| EDG-019 | Recovery reversal arrives before original | Hold/link pending event and resolve when original arrives; no negative corruption. |
| EDG-020 | Recovery exceeds outstanding | Cap application; release/refund/suspense excess; exception and customer evidence. |
| EDG-021 | Recovery after write-off | Post separate recovery-after-write-off event and update reporting without rewriting write-off history. |
| EDG-022 | Telco outage during recharge interception | Durably queue/reconcile recovery; do not lose event; originations may stop independently. |
| EDG-023 | Funding pool exhausted mid-request | Atomic reservation fails; safe unavailable/decline; no fulfilment. |
| EDG-024 | Configuration error increases approvals/limits | Automated guardrail suspends scope; rollback config; incident and affected-population remediation. |
| EDG-025 | Unbalanced accounting template | Activation and posting fail closed; no financial event emitted. |
| EDG-026 | Wrong-tenant credentials with another telco\_id in payload | Reject, security-alert and retain evidence; no data lookup. |
| EDG-027 | Telco statement and platform ledger disagree | Create recon break; preserve both values; no force match; controlled correction after evidence. |
| EDG-028 | Customer claims no consent | Retrieve disclosure/session/acceptance evidence; pause/review; redress if evidence absent or invalid. |
| EDG-029 | Credit bureau rejects file/record | Exception, correction/resubmission, acknowledgement and population reconciliation. |
| EDG-030 | Customer self-excludes with active advance | Block new credit/marketing as configured; continue lawful recovery and service/complaint access. |

| ID | Scenario | Required behaviour |
| --- | --- | --- |
| EDG-031 | Complaint received by telco but not platform | Contracted intake/API/manual process creates platform case and preserves original received time for SLA. |
| EDG-032 | Notification provider outage | Economic transaction continues; messages queue/retry; customer can query status; alert on material non-delivery. |
| EDG-033 | Mass replay of historical events after DR | Idempotent replay; capacity isolation; ledger and recovery totals unchanged except previously missing valid events. |
| EDG-034 | Clock skew or timezone mismatch | Use trusted UTC event times plus source times; reject impossible sequencing or route to exception. |
| EDG-035 | Manual adjustment requested to hide a break | Prohibit direct mutation; require reasoned reversal/repost and independent approval. |
| EDG-036 | Funder/telco settlement delayed | Stop/throttle originations by policy, continue recoveries, age receivable/payable and escalate. |
| EDG-037 | Customer repays through an unsupported route | Route to suspense/manual matching; do not close until evidence and ledger posting are complete. |
| EDG-038 | Data-subject correction changes a score feature | Correct source/derived data, recompute current offer, assess prior bureau/regulatory impact, retain original decision evidence. |
| EDG-039 | Model version unavailable during replay | Fail evidence control; retrieve archived artifact or mark decision unreplayable and escalate; do not fabricate result. |
| EDG-040 | Telco adapter schema changes without notice | Schema validation rejects/quarantines, opens incident and prevents corrupted processing for that telco only. |

## Appendix B - Enterprise Acceptance Evidence

| Control area | Minimum evidence before R1 production |
| --- | --- |
| Business / legal | Signed programme responsibility matrix; lender/contracting entity approval; funding/fee/tax decisions; customer terms. |
| Product / channel | Approved USSD flows and messages; disclosure/consent evidence; self-service and complaint routes; language approvals. |
| Credit / risk | Policy, tier/limit table, anti-gaming back-test, model validation, guardrail simulation, pilot appetite and re-arm process. |
| Telco integration | Canonical mapping, adapter certification, simulator fault pack, sandbox results, performance/security tests, source-data quality reconciliation. |
| Financial | Balanced posting templates, ledger reconstruction, funding-pool controls, recovery allocation, recon/settlement dry run, GL/accounting sign-off. |
| Regulatory / privacy | Obligation register, registration/approval evidence, privacy/DPIA, retention, bureau connector test, complaints SLA/report, regulatory watch review. |
| Security | Threat model, access matrix, secrets/key design, vulnerability/penetration results, logging/monitoring and incident playbook. |
| Resilience | Capacity test, telco isolation, queue replay, backup restoration, DR exercise, unknown-fulfilment resolution and recovery durability. |
| Migration / launch | Two dress rehearsals, opening exposure reconciliation, routing/cutover approval, rollback, hypercare dashboard and partner contacts. |
| Traceability | Every mandatory R0/R1 requirement mapped to design, implementation, test, owner and operating evidence. |

## Appendix C - Administration Configuration Catalogue

| Domain | Configurable examples | Mandatory governance |
| --- | --- | --- |
| Legal entity / programme | Names, licence refs, complaint/privacy contacts, jurisdiction, effective dates, responsibility matrix. | Legal/compliance approval; versioned disclosure impact. |
| Telco adapter | Endpoint, protocol version, certificates references, timeouts, rate limits, queues, status/reversal support. | Security/technology approval; sandbox certification. |
| Product | Value type, denominations, fee structure, term, concurrency, fulfilment, recovery, statements. | Product/risk/finance/compliance maker-checker. |
| Eligibility / scoring | Minimum tenure, KYC/NIN flag, weights, thresholds, tier mapping, freshness, spike caps, one-tier movement. | Back-test, independent risk approval, canary/rollback. |
| Funding / portfolio | Pools, caps, buffers, product/segment limits, guardrail thresholds, suspension. | Treasury/risk approval; atomic enforcement. |
| Collections | Ageing, reminders, contact caps, write-off, vulnerable/dispute treatment, recovery waterfall. | Compliance/risk/finance approval. |
| Finance / settlement | Posting templates, revenue shares, tax, calendars, netting, thresholds, accounts references. | Balance validation; finance maker-checker. |
| Channels / messages | Menus, languages, templates, sender IDs, quiet hours, retries, opt-out/DND. | Content/legal approval; version/preview. |
| Users / roles | Role permissions, telco/programme scope, approval thresholds. | Least privilege; independent access approval/recertification. |
| Feature flags | Enable/disable telco, product, channel, segment, bureau, experiment. | Scope preview, expiry, audit and kill-switch authority. |