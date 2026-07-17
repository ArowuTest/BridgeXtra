TELCO DIGITAL CREDIT PLATFORM
Software Requirements Specification (SRS) - Version 2.0
Multi-Telco Airtime, Data and Digital Credit Advance Platform
| Document Attribute | Value |
| Document status | Build-ready master requirements baseline |
| Version | 2.0 |
| Date | 16 July 2026 |
| Primary audience | Product, Engineering, Architecture, Risk, Finance, Operations, Security, Compliance, Telco Integration and Delivery teams |
| Core principle | Configuration-first, multi-telco, ledger-led, explainable and resilient |
| Intended use | Coding-agent build specification, vendor RFP baseline, telco integration design and implementation governance |

CONFIDENTIAL - FOR AUTHORISED PROJECT USE

# Document Control

| Version | Date | Author/Owner | Summary |
| 1.0 | July 2026 | Project Team | Initial end-to-end SRS |
| 2.0 | 16 July 2026 | Project Team | Multi-telco architecture, system-of-record boundaries, configuration-first admin, advanced scoring, loan ledger, edge cases, scalability, reconciliation and operational controls |

| Approval Role | Name | Decision | Date |
| Executive Sponsor |  | Pending |  |
| Chief Product Officer |  | Pending |  |
| Chief Technology Officer |  | Pending |  |
| Chief Risk Officer |  | Pending |  |
| Chief Information Security Officer |  | Pending |  |
| Finance Director |  | Pending |  |
| Compliance/Data Protection Officer |  | Pending |  |
| Lead Telco Partner |  | Pending |  |


# 1. Executive Summary

This SRS defines a production-grade digital credit platform that can support multiple telecommunications operators, beginning with airtime and data advances and extending to other operator-distributed credit products. The platform shall ingest behavioural and subscriber-status data from each telco, calculate eligibility and limits at scale, return offers with low latency, instruct or coordinate fulfilment through telco systems, maintain authoritative advance and repayment records, reconcile all activity, and calculate settlements and revenue shares.
The platform shall be operator-neutral. MTN, Airtel, Globacom, 9mobile, MVNOs and future operators shall connect through independently configurable adapters behind a Telco Integration Gateway. Every tenant-owned entity shall carry an immutable telco identifier. A telco outage, data defect or settlement dispute shall be isolated from other operators.
The design is configuration-first. Product rules, tiers, score weights, fee rates, revenue shares, operational thresholds, reconciliation tolerances, workflow rules, feature flags and most integration behaviour shall be manageable through an authorised administration portal with versioning, approval, testing and rollback. Core accounting invariants, security controls and data-integrity rules shall not be bypassable through configuration.

# 2. Purpose and Objectives

The purpose is to provide a single scalable platform for telco-assisted digital credit while preserving clear ownership boundaries between telco systems, the lending platform and finance systems.
Primary objectives are instant customer decisions, controlled credit risk, accurate recovery, complete ledgering, reliable reconciliation, configurable multi-telco onboarding, auditable decisions, safe product experimentation and operational resilience at tens of millions of subscribers and high transaction volumes.

# 3. Scope


## 3.1 In Scope

Multi-telco onboarding and tenant management.
Subscriber data ingestion through batch files, streaming events and synchronous APIs.
Eligibility, scoring, affordability, trust progression, anti-gaming and limit assignment.
Airtime, data, voice bundle and configurable digital advance products.
Offer retrieval and low-latency decisioning.
Advance creation, fulfilment orchestration, exposure management and immutable ledgering.
Recharge-triggered and other configured recovery mechanisms.
Repayments, partial repayments, reversals, adjustments, refunds, write-offs and recoveries.
Multi-party fees, commissions, tax, funding cost and settlement calculations.
Reconciliation, exception management, disputes and operational case management.
Customer, telco, lender/funder, operations, risk, finance, support and administration interfaces.
Security, privacy, audit, observability, disaster recovery and regulatory controls.

## 3.2 Out of Scope for Initial Release

Direct control of a telco subscriber airtime or data balance except through authorised telco APIs.
Replacement of telco CRM, billing, charging, network or identity systems.
General-purpose cash lending unless separately approved as a product.
Credit bureau reporting unless enabled through a later approved integration.
Manual editing of immutable ledger entries.

# 4. Architectural and Product Principles

| Principle | Required Interpretation |
| Multi-telco by design | No core service shall contain MTN-specific logic. Operator differences shall be isolated in adapters and configuration. |
| Configuration-first | Business policies shall be administered without code deployment wherever safe. |
| Ledger-led | Financial truth shall be derived from append-only ledger entries, not mutable balances alone. |
| Telco-assisted, platform-owned credit | Telcos own network-side balances and source events; the lending platform owns credit decisions, advances, exposure and recovery allocation. |
| Idempotent and replay-safe | Duplicate requests and event replays shall not create duplicate loans, credits or repayments. |
| Explainable decisions | Every approval, decline and limit shall be reproducible from stored inputs, rule versions and model versions. |
| Fail safely | Uncertain fulfilment or recovery states shall be quarantined and reconciled, not guessed. |
| Tenant isolation | A failure, attack or misconfiguration for one telco shall not compromise another. |
| Progressive trust | Limit growth shall be earned over time; one scoring cycle may increase a customer by no more than the configured tier movement, default one tier. |
| Privacy minimisation | Use flags and derived attributes rather than raw identity data where sufficient. |


# 5. Systems of Record and Ownership Boundaries

| System | Authoritative For | Not Authoritative For |
| Telco System of Record | MSISDN status, SIM registration/NIN-verification flags, subscriber activity, source usage/recharge data, airtime/data balances, fulfilment and network-side deduction events | Credit score, advance contract, exposure, repayment allocation, platform ledger |
| Lending Platform System of Record | Eligibility, score, limit, offers, advance records, exposure, repayment allocation, ledger, reversals, write-offs, reconciliation and commercial settlement calculations | Telco subscriber balance or statutory GL |
| Finance System of Record | General ledger, cash settlement, revenue recognition, statutory reporting, audited financial statements | Real-time offer and telco fulfilment state |

The platform shall retain complete advance records even when the telco performs the actual airtime/data credit. The telco fulfilment reference and platform advance identifier shall be cross-linked and reconciled.

# 6. Stakeholders and User Roles

| Role | Responsibilities |
| Platform Super Administrator | Cross-telco configuration, tenant onboarding, emergency controls and platform-wide oversight. |
| Telco Administrator | Operator-specific products, users, reports and integration settings within delegated permissions. |
| Risk Analyst/Manager | Rules, scoring weights, tier policies, exposure caps, monitoring and model governance. |
| Finance/Settlement User | Fees, commissions, reconciliation, settlement statements and finance exports. |
| Operations User | Exceptions, fulfilment failures, recovery issues, batch processing and cases. |
| Customer Support Agent | Subscriber history, explanations, complaints and controlled adjustments. |
| Fraud Analyst | Fraud alerts, SIM-swap events, linked identities and account restrictions. |
| Compliance/Audit User | Read-only access to decisions, versions, consents, audit events and reports. |
| Funding Partner User | Portfolio and settlement views limited to funded products and telcos. |
| Subscriber | Requests offers/advances, receives disclosures, views status and receives notifications. |


# 7. Multi-Telco Architecture


## 7.1 Tenant Identification

Every subscriber account, score, offer, advance, repayment, ledger entry, reconciliation item, settlement item, case, audit event and configuration object shall carry an immutable telco_id. MSISDN alone shall never be the primary business key. The canonical subscriber key shall be telco_id + normalised MSISDN + effective service period, with internal subscriber_account_id.

## 7.2 Telco Integration Gateway

Authenticates each telco using operator-specific credentials and certificates.
Resolves telco context from credentials and payload; rejects inconsistent identifiers.
Transforms operator-specific schemas and codes into canonical platform messages.
Applies per-telco rate limits, timeouts, retries, circuit breakers and maintenance modes.
Routes outbound fulfilment, enquiry and acknowledgement messages to the correct adapter.
Maintains independent dead-letter queues and replay controls for each operator.

## 7.3 Messaging and Routing

The platform shall support shared logical topics partitioned by telco_id and dedicated operator topics where volume, security or isolation requires it. The routing strategy shall be configurable by operator and event type. Partition keys shall preserve ordering where needed, especially for subscriber-level repayment and fulfilment events.
| ID | Requirement | Priority | Acceptance Evidence | Configurable? |
| MT-001 | All inbound and outbound requests shall include or resolve an immutable telco_id. | Must | Cross-tenant request tests pass. | No |
| MT-002 | Each telco shall have independently configurable endpoints, credentials, queue/topic names, limits and failure policies. | Must | Onboard second telco without core-code change. | Yes |
| MT-003 | A telco adapter failure shall not consume all shared worker, connection or queue capacity. | Must | Chaos test demonstrates continued service for other telcos. | Partly |
| MT-004 | Administrative users shall be restricted to permitted telco tenants. | Must | RBAC and row-level security tests. | Yes |
| MT-005 | The platform shall support shared, schema-isolated or dedicated database deployment modes by telco. | Should | Deployment runbook and migration test. | Yes |


# 8. Configuration and Rules Management Framework

The platform shall implement governed configuration, not uncontrolled editable values. Every material configuration shall have scope, effective dates, version, owner, approver, status, validation, impact preview, audit history and rollback capability.
| Configuration Domain | Examples |
| Tenant | Telco code, country, currency, time zone, operational status, data residency. |
| Integration | URLs, protocol, auth method, certificates, schemas, timeouts, retries, rate limits, queue names, file locations. |
| Products | Airtime/data/voice/device product, denominations, fee display, repayment method, eligibility. |
| Scoring | Feature definitions, weights, transformations, thresholds, model version, override rules. |
| Tiers and limits | Tier values, max movement, exposure caps, cooling periods, offer denominations. |
| Anti-gaming | Spike thresholds, winsorisation caps, stability windows, suspicious-pattern rules. |
| Recovery | Allocation order, partial recovery, protected recharge amount, grace logic, event sources. |
| Commercials | Fee rate, revenue split, tax, funder return, settlement account and cycle. |
| Reconciliation | Matching keys, tolerance, ageing, auto-resolution and escalation rules. |
| Workflow | Maker-checker, approval levels, manual review triggers, SLA and escalation. |
| Feature flags | Enable product/telco/channel/segment/pilot, kill switches and rollout percentages. |
| Notifications | Templates, language, channels, triggers and quiet hours. |
| Retention | Data retention and archival by data class and telco. |
| Reporting | Scheduled reports, recipients, fields, filters and formats. |

| ID | Requirement | Priority | Acceptance Evidence | Configurable? |
| CFG-001 | Material configuration changes shall require maker-checker approval. | Must | Approval workflow and audit test. | No |
| CFG-002 | Configuration shall support draft, simulation, approved, scheduled, active, superseded and rolled-back states. | Must | Lifecycle tests. | No |
| CFG-003 | The platform shall prevent activation of invalid or internally inconsistent configuration. | Must | Negative validation tests. | No |
| CFG-004 | Each decision and transaction shall retain the exact configuration version used. | Must | Historical replay reproduces result. | No |
| CFG-005 | Emergency kill switches shall be available per telco, product and channel. | Must | Operational drill. | Yes |
| CFG-006 | Secrets shall not be displayed in plain text in the admin portal. | Must | Security test. | No |


# 9. Telco Data Ingestion and Data Contracts


## 9.1 Supported Interfaces

Nightly or intraday batch files for large subscriber feature sets.
Streaming events for recharge, repayment deduction, SIM swap, fraud, barring, NIN-verification changes and fulfilment status.
Synchronous APIs for eligibility checks, offer retrieval, fulfilment and status enquiries.
Secure object storage, SFTP, private API connectivity, VPN or dedicated links as agreed.

## 9.2 Minimum Canonical Subscriber Attributes

| Category | Illustrative Attributes |
| Identity/status | telco_id, normalised MSISDN, subscriber status, prepaid/postpaid, SIM age, registration status, NIN verified flag, KYC tier. |
| Recharge | Counts, amounts, medians, percentiles, recency, channel, reversals and rolling windows. |
| Usage | Voice, data, SMS, VAS usage and active days in rolling windows. |
| Risk events | SIM swap, porting, barring, fraud flag, abnormal recharge, device change and account linkage. |
| Credit history | Prior advances, recovery amounts, repayment speed, delinquency, write-off and dispute outcomes. |
| Data quality | Source timestamp, extract timestamp, schema version, completeness and confidence indicators. |

| ID | Requirement | Priority | Acceptance Evidence | Configurable? |
| DATA-001 | Actual NIN values shall not be required where a verified flag is sufficient. | Must | Data contract review. | Yes |
| DATA-002 | Every feed shall include source time, extract time, telco_id, schema version and unique file/event identifier. | Must | Contract validation. | No |
| DATA-003 | Batch files shall be checksum-validated, decryptable, schema-validated and reconciled to control totals before use. | Must | Failed-file test. | No |
| DATA-004 | Late, duplicate and out-of-order events shall be handled idempotently. | Must | Replay test. | No |
| DATA-005 | Data-quality failures shall not silently overwrite a previously valid subscriber profile. | Must | Quarantine and fallback test. | Partly |


# 10. Eligibility, Scoring, Affordability and Limit Assignment


## 10.1 Processing Model

The default architecture shall use pre-calculated subscriber eligibility and limits on a scheduled basis, combined with real-time overlays for critical changes. The real-time request path shall normally read an approved offer snapshot rather than recompute all historical features. Full or partial recalculation may be triggered by configured events.
Scheduled portfolio scoring: daily by default, configurable by telco and segment.
Event-driven overlays: repayment completion, SIM swap, NIN status change, barring, fraud alert, material recharge, tenure threshold and manual restriction.
On-demand re-score: authorised operations/risk action with full audit.
Stale-score policy: configurable maximum score age and safe fallback behaviour.

## 10.2 Scoring Dimensions

| Dimension | Examples | Role |
| Eligibility gates | Prepaid status, SIM age, active status, NIN verification, fraud/barring, minimum history. | Hard pass/fail before scoring. |
| Behaviour score | Recharge consistency, usage stability, active days, tenure and recency. | Predictive behavioural quality. |
| Trust score | Successful advance cycles, recovery speed, partial repayments and defaults. | Progressive limit growth. |
| Affordability | Stable recharge capacity, expected recovery and exposure-to-spend ratio. | Caps maximum sustainable exposure. |
| Fraud score | Spikes, cycling, linked accounts, SIM changes, device patterns and suspicious channels. | Decline, reduce or review. |
| Portfolio controls | Telco/product/funder exposure, vintage performance and concentration. | Portfolio-level cap. |


## 10.3 Anti-Gaming Requirements

A single large recharge shall not immediately increase a limit materially.
Features shall use rolling windows, median/trimmed mean, percentiles, variance and stability measures.
Recharge spikes shall be compared with the subscriber baseline, peer segment and channel behaviour.
Configured outliers may be capped, discounted, quarantined or excluded from affordability.
Circular or refunded recharges, agent self-funding patterns and rapid recharge-borrow sequences shall be detected.
Tier increases shall be limited to the configured maximum per scoring cycle; default one tier.
Tier decreases may occur immediately where risk increases, subject to customer-treatment rules.
New subscribers shall begin at conservative starter tiers and earn trust through successful cycles.
| ID | Requirement | Priority | Acceptance Evidence | Configurable? |
| SCR-001 | A one-off recharge spike shall not by itself move a subscriber more than the configured maximum tier movement. | Must | Synthetic spike scenario. | Yes |
| SCR-002 | Default maximum upward tier movement shall be one tier per scoring cycle. | Must | Tier transition tests. | Yes |
| SCR-003 | Scoring shall store input snapshot, feature values, score contributions, rules, model and configuration versions. | Must | Decision replay. | No |
| SCR-004 | Scoring shall support champion/challenger models and controlled A/B allocation. | Should | Model governance test. | Yes |
| SCR-005 | Limits shall be the minimum of risk limit, affordability limit, product cap, telco cap, funder cap and portfolio cap. | Must | Boundary tests. | Partly |
| SCR-006 | The platform shall support reason codes understandable to operations and customers. | Must | Reason-code review. | Yes |


# 11. Product and Offer Management

Products shall be data-driven and scoped by telco, country, segment, channel and effective date.
A product may represent airtime, data, voice, device or another approved digital credit item.
Offer denominations, fee treatment, net value, repayment value, expiry and disclosure shall be configurable.
Offers shall be generated only from currently active products and the subscriber current available exposure.
The platform shall support a catalogue of permitted denominations rather than arbitrary values where required by the telco.
Product changes shall be versioned; existing advances retain the terms accepted at origination.
| ID | Requirement | Priority | Acceptance Evidence | Configurable? |
| PRD-001 | Admin users shall create and version products without code deployment. | Must | Create pilot product in test. | Yes |
| PRD-002 | Offer responses shall show gross advance, service fee, net delivered value, total repayment and expiry. | Must | API/UI acceptance test. | Yes |
| PRD-003 | An expired or superseded offer shall not be accepted. | Must | Expiry race test. | No |
| PRD-004 | The platform shall prevent a product from being activated without required settlement and fulfilment configuration. | Must | Validation test. | No |


# 12. Advance Lifecycle and Fulfilment Orchestration


## 12.1 State Model

| State | Meaning |
| OFFERED | Offer is available but not accepted. |
| REQUESTED | Customer request received and authenticated. |
| RESERVED | Exposure reserved; duplicate/concurrent requests blocked. |
| PENDING_FULFILMENT | Fulfilment instruction submitted or awaiting submission. |
| ACTIVE | Telco confirmed credit and advance is outstanding. |
| PARTIALLY_REPAID | Some recovery received. |
| REPAID | Principal/repayment obligation fully satisfied. |
| FULFILMENT_FAILED | Telco confirmed no credit; reservation released. |
| FULFILMENT_UNKNOWN | Outcome uncertain; no retry that could duplicate credit without status enquiry. |
| REVERSED | Original credit and advance reversed under controlled process. |
| WRITTEN_OFF | Accounting/risk write-off recorded; later recoveries still supported. |
| CANCELLED | Request cancelled before fulfilment. |
| DISPUTED | Operational or customer dispute attached; financial state remains explicit. |


## 12.2 Origination Flow

Receive telco/channel request with telco_id, MSISDN, product/amount, request ID and timestamp.
Authenticate telco/channel and validate payload.
Resolve subscriber account and current offer snapshot.
Apply real-time overlay, exposure and product validations.
Acquire subscriber-level concurrency control and idempotency record.
Reserve exposure and create pending advance record.
Post provisional ledger/control entries where applicable.
Submit fulfilment to telco or return approval for telco-managed fulfilment according to integration mode.
Process definitive telco acknowledgement.
Activate or fail the advance and post final ledger entries.
Send response/notification and emit downstream events.
| ID | Requirement | Priority | Acceptance Evidence | Configurable? |
| ADV-001 | The platform shall maintain an advance record for every approved fulfilment attempt. | Must | Traceability test. | No |
| ADV-002 | Duplicate requests with the same idempotency key shall return the original outcome. | Must | Duplicate storm test. | No |
| ADV-003 | Concurrent requests shall not exceed the subscriber available limit. | Must | Concurrency test. | No |
| ADV-004 | Unknown fulfilment outcomes shall use status enquiry/reconciliation before any repeat credit instruction. | Must | Timeout-after-success simulation. | No |
| ADV-005 | Exposure reservations shall expire or be repaired according to configured policies without losing audit history. | Must | Stuck-reservation test. | Yes |


# 13. Loan Records, Ledger and Exposure

The lending platform shall be the authoritative system for advance records and exposure. It shall maintain both lifecycle records and an append-only financial/sub-ledger. Mutable balance fields may be cached for performance but must be reproducible from ledger entries.
| Ledger Event | Illustrative Accounting Meaning |
| ADVANCE_ORIGINATED | Principal/exposure recognised after confirmed fulfilment. |
| SERVICE_FEE_RECOGNISED/DEFERRED | Fee treatment based on policy. |
| REPAYMENT_RECEIVED | Recovery applied to an advance. |
| REPAYMENT_REVERSAL | Previously reported recovery reversed. |
| FULFILMENT_REVERSAL | Telco reverses credited value. |
| ADJUSTMENT | Authorised non-standard correction with reason and approval. |
| WRITE_OFF | Exposure moved to written-off status. |
| POST_WRITE_OFF_RECOVERY | Recovery received after write-off. |
| COMMISSION_ACCRUAL | Telco/funder/platform commercial share calculated. |
| SETTLEMENT | Obligation settled or netted. |

| ID | Requirement | Priority | Acceptance Evidence | Configurable? |
| LED-001 | Ledger entries shall be append-only and balanced according to configured accounting event templates. | Must | Ledger integrity tests. | Partly |
| LED-002 | No user shall edit or delete posted ledger entries. Corrections shall use reversal and replacement entries. | Must | Permission and audit test. | No |
| LED-003 | Every entry shall contain telco_id, advance_id, event_id, currency, amount, event time, posting time and source reference. | Must | Schema validation. | No |
| LED-004 | Exposure shall be available by subscriber, telco, product, funder, vintage and portfolio. | Must | Reporting test. | No |
| LED-005 | The platform shall prevent negative exposure or over-repayment except through explicit adjustment workflows. | Must | Boundary tests. | No |


# 14. Recharge Recovery and Repayment Allocation

The telco may intercept or deduct value from recharges and report the recovery, or the platform may calculate a requested deduction that the telco executes. The integration mode shall be configured per telco/product. In both modes, the platform must receive definitive recovery events and maintain the authoritative allocation to advances.
Support full, partial and multi-event repayment.
Support configurable allocation order: oldest first, earliest due, product priority, pro-rata or other approved method.
Support protected recharge portions or minimum retained balances where required.
Support repayment events from voucher, bank top-up, transfer or other channels as configured.
Support recovery reversals caused by top-up reversal, chargeback or telco correction.
Prevent double allocation of replayed events.
Reopen or adjust advances after a repayment reversal.
Apply repayment first to components according to configured waterfall, e.g., principal, fee, tax, penalty where legally permitted.
| ID | Requirement | Priority | Acceptance Evidence | Configurable? |
| REP-001 | Every recovery event shall be idempotently matched and allocated once. | Must | Replay test. | No |
| REP-002 | Partial recovery shall update outstanding exposure immediately. | Must | Partial repayment test. | No |
| REP-003 | A reversal shall restore the appropriate outstanding amount and retain full history. | Must | Reversal test. | No |
| REP-004 | Unmatched recovery events shall enter an exception queue and never be discarded. | Must | Unknown subscriber/advance test. | No |
| REP-005 | Allocation rules shall be configurable by telco/product with version control. | Must | Policy switch test. | Yes |


# 15. Reconciliation, Settlement and Commercial Accounting


## 15.1 Reconciliation Layers

| Layer | Comparison |
| Request/decision | Telco requests versus platform responses. |
| Fulfilment | Platform-approved advances versus telco credits. |
| Recovery | Telco deductions/recharge recoveries versus platform repayments. |
| Ledger | Operational records versus sub-ledger totals. |
| Commercial | Fees and revenue shares versus contract terms. |
| Settlement | Calculated obligations versus statements and cash received/paid. |
| Finance | Platform settlement exports versus finance system postings. |


## 15.2 Settlement

Separate settlement by telco, product, funder, currency and contractual period.
Support gross, net and hybrid settlement models.
Support service fee, telco share, platform share, funder return, tax and other deductions.
Generate statements, supporting transaction files and signed control totals.
Track payable/receivable ageing, disputes and settlement adjustments.
Prevent configuration changes from retroactively changing closed settlement periods unless a controlled restatement is approved.
| ID | Requirement | Priority | Acceptance Evidence | Configurable? |
| REC-001 | Matching shall use stable external references plus controlled secondary matching rules. | Must | Reconciliation test pack. | Yes |
| REC-002 | Exceptions shall have type, value, owner, age, SLA, status, evidence and resolution. | Must | Case workflow test. | Yes |
| REC-003 | Tolerance-based auto-resolution shall be configurable but fully audited. | Should | Tolerance scenario. | Yes |
| SET-001 | Settlement calculations shall be reproducible from transaction-level data and configuration versions. | Must | Recalculation test. | No |
| SET-002 | Closed periods shall be locked; later changes shall post as adjustments in an open period. | Must | Period-lock test. | Yes |


# 16. Front-End Portals and User Experience


## 16.1 Administration Portal

Telco onboarding wizard and tenant status.
Product, tier, fee, scoring, anti-gaming, recovery, settlement and notification configuration.
Integration endpoint, schema, certificate metadata, queue/topic and health configuration.
Feature flags, pilot cohorts, rollout percentages and emergency suspension.
Maker-checker approvals, effective dating, simulation, impact preview and rollback.
User, role, telco scope and privileged-access management.
Audit and configuration-difference views.

## 16.2 Operations Portal

Cross-telco dashboard with authorised filters and tenant-specific views.
Advance and repayment search; subscriber timeline.
Fulfilment-unknown, stuck reservations, unmatched repayments and batch failures.
Retry/status-enquiry controls that are safe and idempotent.
Case assignment, notes, attachments, SLA and escalation.
Operational health, queue lag and telco connectivity.

## 16.3 Risk Portal

Portfolio exposure, approval, take-up, recovery, delinquency and vintage views.
Tier migration, score distribution, drift and anti-gaming alerts.
Rule/model simulation before activation.
Champion/challenger experiment results.
Overrides, blacklists, whitelists and exposure caps with approvals.

## 16.4 Finance and Settlement Portal

Transaction, fee, commission and settlement dashboards.
Reconciliation exceptions and statements.
Period close, lock, adjustment and export controls.
Telco/funder receivable and payable ageing.

## 16.5 Customer Support and Customer Channels

Support agents see only necessary masked identity and full transaction history.
Customer-facing channels may be USSD, SMS, telco app, web, IVR or API.
Disclosures shall show advance value, fee, net benefit, repayment obligation and recovery mechanism before acceptance.
Customers shall receive confirmations, repayment notifications, outstanding balance information and complaint channels.

# 17. API and Integration Requirements

| API/Event | Purpose |
| Get Offers | Return current eligible products and denominations. |
| Create Advance | Accept selected offer and initiate/reserve fulfilment. |
| Fulfilment Callback | Receive definitive telco outcome. |
| Advance Status | Resolve timeouts and unknown outcomes. |
| Recharge/Recovery Event | Report deduction/recovery. |
| Recovery Reversal | Reverse previously reported recovery. |
| Subscriber Status Event | Update barring, NIN, SIM swap, porting or fraud status. |
| Batch Feature Feed | Provide large-scale scoring inputs. |
| Reconciliation File | Exchange daily control totals and transaction records. |
| Settlement Statement | Exchange calculated and acknowledged obligations. |

| ID | Requirement | Priority | Acceptance Evidence | Configurable? |
| API-001 | All mutation APIs shall require idempotency keys and signed/authenticated requests. | Must | Security and duplicate tests. | No |
| API-002 | Canonical APIs shall be versioned and backward-compatible for an agreed support period. | Must | Contract tests. | Partly |
| API-003 | Responses shall include stable error codes, correlation ID and retry guidance. | Must | Negative API tests. | Partly |
| API-004 | Sensitive fields shall be encrypted in transit and masked in logs. | Must | Security test. | No |
| API-005 | Per-telco rate limiting and quotas shall be configurable. | Must | Load/rate test. | Yes |


# 18. Scalability, Performance and Availability

| Area | Requirement |
| Subscriber scale | Support at least 100 million subscriber profiles across telcos, with horizontal growth. |
| Scoring throughput | Score tens of millions of subscribers within the configured batch window using partitioned/distributed processing. |
| Offer latency | Target p95 <= 150 ms and p99 <= 300 ms within the platform boundary for cached offer retrieval, excluding telco network latency. |
| Advance decision latency | Target p95 <= 300 ms before external fulfilment call for normal path. |
| Event throughput | Scale horizontally for burst recharge and repayment events; exact capacity shall be established through telco forecasts. |
| Availability | Core real-time decision services target 99.99% availability; batch/reporting services may have lower defined SLOs. |
| RPO/RTO | Ledger/advance RPO near zero through synchronous replication or durable log; target RTO <= 30 minutes for critical services, subject to deployment design. |
| Isolation | Independent autoscaling, quotas and circuit breakers per telco adapter. |
| Backpressure | Queues and APIs shall shed or defer non-critical work before compromising financial correctness. |
| Data partitioning | Partition high-volume data by telco_id, time and/or subscriber hash to avoid hotspots. |

| ID | Requirement | Priority | Acceptance Evidence | Configurable? |
| NFR-001 | No single application node shall be required for correctness or availability. | Must | Failure test. | No |
| NFR-002 | Critical services shall be stateless where practical and horizontally scalable. | Must | Autoscaling test. | Partly |
| NFR-003 | Subscriber-level ordering shall be preserved for fulfilment and repayment events where required. | Must | Ordering test. | No |
| NFR-004 | The platform shall survive at least a 5x forecast burst without data loss, using queues and backpressure. | Must | Stress test. | Partly |
| NFR-005 | Capacity dashboards shall show usage and headroom by telco and service. | Must | Observability test. | Yes |


# 19. Security, Privacy and Compliance

Zero-trust service authentication; mTLS for telco and internal high-trust integrations where appropriate.
Encryption at rest and in transit; tenant-specific keys where required.
Secrets managed through a vault/KMS, rotated without code change.
Strong RBAC/ABAC using role, telco scope, product scope and action.
MFA for privileged users; just-in-time and break-glass access controls.
Tamper-evident audit logs and security monitoring.
Tokenisation or hashing of MSISDN in analytical environments where feasible.
Data minimisation, retention, deletion and legal-hold controls.
Consent/disclosure records where required.
Secure SDLC, dependency scanning, SAST, DAST, penetration testing and threat modelling.
Fraud monitoring for credential abuse, replay, enumeration and automated request attacks.
| ID | Requirement | Priority | Acceptance Evidence | Configurable? |
| SEC-001 | Cross-tenant data access shall be prevented at API, service and database layers. | Must | Penetration and RLS tests. | No |
| SEC-002 | Privileged configuration changes shall be signed, approved and audited. | Must | Audit test. | No |
| SEC-003 | Logs shall not contain raw secrets, full NIN or unmasked sensitive fields. | Must | Log scan. | No |
| SEC-004 | The platform shall support configurable retention by data class and tenant within legal constraints. | Must | Retention test. | Yes |
| SEC-005 | Security incidents shall be isolatable by telco without shutting the entire platform where safe. | Should | Incident drill. | Partly |


# 20. Observability and Operational Resilience

Distributed tracing using correlation IDs spanning telco gateway, decision, fulfilment, ledger and notification.
Metrics by telco, product, channel, adapter and status.
Structured logs with masking and traceability.
Business controls: approvals, fulfilments, recoveries, exposure, settlement variance and reconciliation ageing.
Technical controls: latency, error rate, queue lag, retry rate, circuit-breaker state, saturation and database health.
Synthetic probes for telco endpoints and internal critical paths.
Runbooks for telco outage, batch failure, unknown fulfilment, ledger imbalance, queue backlog, data corruption and cyber incident.
Regular disaster-recovery and reconciliation-rebuild exercises.

# 21. Edge Cases and Required Behaviour

| Scenario | Trigger | Required Behaviour |
| Duplicate advance request | Same request ID replayed | Return original result; do not create second reservation/advance. |
| Concurrent requests | Two channels request simultaneously | Serialize or atomically reserve exposure; total cannot exceed limit. |
| Telco timeout after credit | Platform does not receive response | Mark FULFILMENT_UNKNOWN; query status/reconcile before retry. |
| Platform crash after telco success | Credit succeeded before local activation | Recover via outbox/inbox/reconciliation; activate exactly once. |
| Platform approves but telco rejects | No airtime/data delivered | Mark failed, reverse reservation and provisional entries. |
| Partial fulfilment | Telco delivers different amount | Quarantine; either recognise actual amount under approved rule or reverse/correct. |
| Duplicate recovery event | Telco replays recharge deduction | Idempotency prevents double repayment. |
| Out-of-order recovery/reversal | Reversal arrives first | Park until original arrives or resolve through exception process. |
| Recovery exceeds outstanding | Overpayment | Cap allocation; create unapplied item/refund/credit according to policy. |
| Recharge reversal after repayment | Previously recovered value reversed | Reverse allocation and reopen exposure. |
| SIM swap after score | Risk changes before borrow | Real-time overlay blocks/reduces offer for configured cooling period. |
| NIN flag changes to unverified | Eligibility lost | Block new advances; existing obligations remain recoverable. |
| Number porting | MSISDN moves operator | Close old telco service account, preserve history, create new telco account; no cross-tenant assumption. |
| MSISDN recycled | Number assigned to new person | Use telco effective dates/identity confidence; do not expose predecessor history. |
| One-off large recharge | Attempt to game limit | Spike discounted; max upward movement one configured tier. |
| Stale score | Batch delayed | Apply score-age policy: allow conservative offer, re-score or decline safely. |
| Missing feed fields | Partial telco data | Use approved fallback only; otherwise quarantine and protect prior valid data. |
| Corrupt batch file | Checksum/schema mismatch | Reject whole file or safe partition; alert; retain last valid snapshot. |
| Telco sends wrong tenant ID | Credentials and payload disagree | Reject and raise security alert. |
| Product disabled mid-request | Kill switch activated | Requests not yet fulfilled fail safely; active obligations unchanged. |
| Tier configuration gap | No matching tier | Fail configuration validation before activation; never guess. |
| Fee change during accepted offer | Offer accepted under old terms | Honour accepted offer version until expiry. |
| Funder limit exhausted | Portfolio capacity unavailable | Decline new originations or route to approved alternative funding pool. |
| Telco outage | Fulfilment unavailable | Open circuit breaker; suspend offers or queue according to configured safe mode. |
| Queue backlog | Events delayed | Scale workers, apply backpressure, preserve ordering and monitor SLA. |
| Database failover | Primary unavailable | Fail over without duplicate posting; validate ledger sequence and replay outbox. |
| Ledger imbalance | Posting template defect | Stop affected product/telco posting, quarantine transactions and alert finance. |
| Settlement dispute | Telco rejects statement item | Create case, freeze disputed amount only, preserve undisputed settlement. |
| Manual adjustment abuse | Agent attempts unauthorised change | Maker-checker, limits, reason/evidence, no ledger edits. |
| Customer dispute after repayment | Customer claims wrongful deduction | Provide trace, telco evidence and controlled refund/reversal workflow. |
| Time-zone/day boundary | Telcos use different zones | Store UTC plus telco-local business date; settlement based on configured calendar. |
| Currency mismatch | Wrong-currency event | Reject/quarantine; never auto-convert unless an approved FX process exists. |
| Schema version change | Telco sends new payload | Version negotiation; unsupported versions rejected with clear error. |
| Certificate expiry | Telco credential expires | Advance alerting and rotation; fail securely. |
| Notification failure | SMS/app message unavailable | Financial transaction remains valid; retry notification separately. |
| Analytics lag | Warehouse delayed | Operational decisions continue from operational store; dashboards show freshness. |
| Write-off then recovery | Late recharge received | Post post-write-off recovery; do not erase write-off history. |
| Death/inactive account/legal hold | Special treatment required | Apply approved status workflow and recovery restrictions. |
| Mass configuration error | Risk rule would over-approve | Simulation, change approval, canary rollout, automatic guardrails and rollback. |
| Cross-telco coincidence | Same MSISDN appears in historical data | Treat telco_id + effective period as distinct subscriber accounts. |


# 22. Core Data Model

| Entity | Key Attributes |
| TelcoTenant | telco_id, country, currency, time zone, status, deployment mode. |
| SubscriberAccount | subscriber_account_id, telco_id, normalised MSISDN token, effective dates, status. |
| SubscriberFeatureSnapshot | snapshot_id, source period, feature values, quality, schema version. |
| ScoreDecision | score_id, model/rule/config versions, contributions, reason codes, decision time. |
| CreditTier | tenant/product tier number, amount, movement rules, effective dates. |
| Offer | offer_id, subscriber, product, gross/net/fee/repayment, expiry, terms version. |
| Advance | advance_id, request/idempotency keys, product, amount, state, fulfilment reference. |
| ExposureReservation | reservation_id, amount, expiry, status. |
| LedgerEntry | entry_id, event, debit/credit account, amount, currency, references. |
| RecoveryEvent | recovery_id, telco event reference, source recharge, amount, status. |
| RepaymentAllocation | allocation_id, recovery_id, advance_id, component, amount. |
| ReconciliationItem | source, expected, actual, variance, match status and case. |
| SettlementPeriod | tenant/product/funder dates, status, totals and statement. |
| ConfigurationVersion | domain, scope, values, lifecycle, approvals and hash. |
| AuditEvent | actor, action, object, before/after hashes, time, reason, source IP/device. |


# 23. Testing and Quality Assurance

Unit and property-based testing for scoring, tier movement, fees, allocation and ledger invariants.
Contract tests for each telco adapter and schema version.
End-to-end tests covering USSD/app request through fulfilment, repayment, reconciliation and settlement.
Load, soak, burst and capacity tests using realistic telco distributions.
Chaos tests for endpoint timeout, duplicate events, queue failure, database failover and partial region loss.
Security, penetration, tenant-isolation and privilege-abuse tests.
Model validation, back-testing, bias/fairness review where applicable, drift and stability tests.
Financial reconciliation tests proving transaction-level totals.
Disaster-recovery, restore and historical replay tests.
UAT by risk, operations, finance, support, security and each telco.
| ID | Requirement | Priority | Acceptance Evidence | Configurable? |
| TST-001 | No release shall proceed with unresolved critical ledger, tenant-isolation or duplicate-processing defects. | Must | Release gate. | No |
| TST-002 | Each telco adapter shall have automated certification tests before production activation. | Must | Certification report. | No |
| TST-003 | Configuration changes shall be testable in simulation against historical or synthetic data. | Must | Simulation evidence. | Yes |
| TST-004 | Performance tests shall use agreed peak TPS and 5x burst scenarios. | Must | Performance report. | Yes |


# 24. Telco Onboarding and Migration

Commercial and regulatory readiness confirmed.
Tenant created with country/currency/time zone and data-residency settings.
Data contract and canonical mappings agreed.
Connectivity, authentication and certificate exchange completed.
Historical subscriber features and incumbent loan data mapped and quality-assessed.
Adapter contract tests and security tests passed.
Products, tiers, fees, scoring, recovery and settlement configured in draft.
Historical back-testing and limit distribution approved by risk and telco.
Shadow scoring and reconciliation run without customer impact.
Pilot cohort enabled using feature flags and conservative caps.
Ramp-up by segment/region/percentage with live monitoring.
Full launch after operational, financial and risk acceptance.
Migration from an incumbent provider shall preserve external references, outstanding advances, repayment history, customer treatment and settlement positions. The platform shall support dual-running and cutover reconciliation.

# 25. Reporting and Analytics

Daily operational report by telco/product/channel.
Portfolio exposure, approvals, take-up, utilisation and available funding.
Repayment curves, delinquency, roll rates, write-offs and recoveries.
Vintage/cohort analysis and tier migration.
Fee, revenue-share and profitability reporting.
Reconciliation and settlement variance ageing.
Data quality and feed freshness.
Model performance, drift and reason-code distribution.
Fraud alerts and suspicious recharge/borrow behaviour.
Regulatory/consumer complaint and service-availability reports.

# 26. Regulatory and Policy Considerations

Final legal classification, licensing, customer-disclosure, fee, consent, data-processing, complaints, credit reporting and collection requirements shall be confirmed for each jurisdiction and product before launch. The system shall therefore support configurable disclosures, consent capture, fee caps, cooling-off or grace rules, complaint SLAs, data-retention and audit evidence. Legal requirements shall not be assumed from a prior airtime-advance model.

# 27. Deployment and Technical Architecture Guidance

Containerised services orchestrated across multiple availability zones.
API gateway/WAF, Telco Integration Gateway and independently deployable telco adapters.
Separate services for subscriber profile, scoring, offer, advance, fulfilment, ledger, recovery, reconciliation, settlement, configuration, notifications and identity/access.
Durable event bus with partitioning, schema registry, dead-letter queues and replay controls.
Relational transactional store for advances/ledger; distributed cache for offers; object storage/data lake for raw feeds; warehouse/lakehouse for analytics.
Outbox/inbox patterns for reliable state-event consistency.
Immutable backups, point-in-time recovery and cross-region disaster recovery for critical data.
Infrastructure as code and environment-specific configuration; no secrets in source control.

# 28. Release Acceptance Criteria

At least one telco completes end-to-end certification and a second telco can be configured without core-code changes.
All financial transactions reconcile from request through settlement.
Duplicate, timeout-after-success and recovery-reversal scenarios pass.
One-tier maximum movement and anti-gaming scenarios pass.
Tenant isolation, RBAC and security tests pass.
Capacity and latency targets are achieved at forecast peak and burst load.
Disaster recovery and ledger reconstruction are demonstrated.
Admin configuration lifecycle, approval, simulation and rollback are demonstrated.
Operational teams have dashboards, alerts, runbooks and trained users.
Risk, Finance, Security, Compliance, Product and Telco stakeholders sign off.

# 29. Delivery Workstreams

| Workstream | Principal Deliverables |
| Platform Foundation | Tenant model, identity, configuration, audit, observability and deployment. |
| Telco Integration | Gateway, adapters, batch/stream/API contracts and certification harness. |
| Data and Scoring | Feature store, scoring pipeline, anti-gaming, tiering and explainability. |
| Credit Core | Offers, advance lifecycle, exposure, concurrency and fulfilment orchestration. |
| Ledger and Recovery | Sub-ledger, recovery allocation, reversals and write-offs. |
| Reconciliation and Settlement | Matching, exceptions, commissions, statements and finance exports. |
| Portals | Admin, operations, risk, finance, support and telco views. |
| Security and Compliance | IAM, encryption, privacy, regulatory controls and testing. |
| Operations Readiness | Runbooks, monitoring, DR, support model, SLAs and training. |
| Telco Launch | Data mapping, back-test, shadow run, pilot, ramp-up and cutover. |


# 30. Decisions Required Before Detailed Design Freeze

| Decision | Options/Questions |
| Legal lender/funder | Platform company, licensed lender, bank, telco or multi-party structure? |
| Fulfilment mode | Platform calls telco credit API or telco fulfils after platform approval? |
| Recovery mode | Telco autonomously deducts and reports, or platform instructs deduction? |
| Fee structure | Upfront deduction, added repayment amount, bundle-specific pricing, tax treatment? |
| Data frequency | Daily full feed, incremental intraday, streaming events and API fallback? |
| Infrastructure location | Cloud, telco-hosted, hybrid, country-specific or dedicated tenant? |
| Database isolation | Shared partitioned, schema-per-telco or dedicated for major telcos? |
| Initial products | Airtime only, airtime + data, or broader digital credit catalogue? |
| Target scale/TPS | Subscriber counts, peak USSD/app TPS, recharge event peaks and scoring windows? |
| Regulatory controls | Required licences, disclosures, consent, complaints and reporting? |


# Appendix A - Glossary

| Term | Meaning |
| Advance | Credit value delivered now and recovered later. |
| MSISDN | Subscriber mobile number identifier. |
| Telco | Mobile network operator or MVNO connected to the platform. |
| Tenant | Logically isolated telco/operator configuration and data scope. |
| Offer Snapshot | Pre-calculated, time-bound set of amounts available to a subscriber. |
| Exposure | Outstanding unrecovered obligation. |
| Idempotency | Repeated processing produces only one financial effect. |
| NIN Verification Flag | Boolean/status confirmation without necessarily sharing the actual NIN. |
| Reconciliation | Comparison of records between systems to identify and resolve differences. |
| System of Record | Authoritative source for a defined data domain. |
| Tier | Configured credit-limit band used for progressive trust. |
| Outbox/Inbox | Reliable messaging patterns linking database state and event publication/consumption. |


# Appendix B - Configuration Guardrails

No configuration may bypass ledger balancing, tenant isolation, idempotency or authentication.
High-risk changes require dual approval and may require risk/finance/security approval based on domain.
All values must have allowed ranges and semantic validation.
Changes affecting approval rates or exposure require impact simulation and optional canary rollout.
Rollback must restore the previous version without deleting historical decisions.
Emergency suspension is immediate but reactivation requires normal approval unless an approved incident procedure applies.