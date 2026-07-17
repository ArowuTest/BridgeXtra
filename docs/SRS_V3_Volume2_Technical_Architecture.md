# Telco Digital Credit Platform

Enterprise Blueprint & Software Requirements Specification v3.0

VOLUME 2

# TECHNICAL ARCHITECTURE & BUILD SPECIFICATION

Build baseline for engineering, solution architecture, data, security, QA, DevSecOps, SRE, telco integration and technical assurance teams.

| Document attribute | Value |
| --- | --- |
| Document status | Authoritative technical baseline - subject to design-freeze decisions |
| Version | 3.0 - Volume 2 |
| Classification | Confidential - Controlled Document |
| Primary market | Nigeria; architecture remains multi-country capable |
| Business model | Platform operates directly as a lender of record; telcos provide rails and source data |
| Companion document | Volume 1 - Enterprise & Business Architecture |
| Intended readers | CTO, architects, engineering, data, security, operations, risk, finance, compliance, telco integration and QA |

Governing principle — The platform shall be configuration-first but not configuration-unbounded. Security, tenant isolation, ledger balance, audit immutability, idempotency and state-machine invariants are non-configurable controls.

## Document Control

| Field | Detail |
| --- | --- |
| Document owner | Chief Product / Technology Office |
| Architecture owner | Chief Architect / Architecture Review Board |
| Approval bodies | Executive Steering Committee, Risk Committee, Finance Control, Security and Compliance |
| Review cycle | At each material architecture decision, regulatory change or release boundary |
| Change control | All changes require a versioned change record and requirement-impact assessment |
| Source hierarchy | Volume 1 enterprise requirements; this Volume 2; approved ADRs; interface contracts; test evidence |
| Precedence | Non-configurable financial and security invariants prevail over tenant configuration and workflow rules |

## Version History

| Version | Date | Summary | Status |
| --- | --- | --- | --- |
| 3.0 Vol. 2 | 16 July 2026 | Initial technical architecture and build specification aligned to Volume 1 and the external review closure | Issued for detailed design and build |

## How to Use This Document

This volume translates the enterprise baseline into implementable technical requirements. It is intentionally detailed enough to guide a coding agent and engineering team, while preserving the distinction between mandatory behaviour and replaceable implementation technology.

-   Requirements marked Must are release-gating unless an approved waiver exists.
    
-   Should requirements are expected for the target release but may be phased through an approved roadmap decision.
    
-   Could requirements are extensibility provisions that must not weaken the core architecture.
    
-   Every requirement has an acceptance-evidence expectation. Test plans shall reference the requirement ID directly.
    
-   Concrete cloud products, programming languages and database brands shall be selected through ADRs. The canonical interfaces and invariants in this document remain binding.
    

Scope boundary — Volume 2 specifies how the platform is built. Volume 3 will specify production operations, support, treasury operations, service management, migration execution, rollout governance and final acceptance packs in greater operational depth.

## Table of Contents

| Sections 1-20 | Sections 21 onward |
| --- | --- |
| 1\. Technical Executive Summary | 21\. Portal and Front-End Technical Architecture |
| 2\. Scope, Architecture Boundaries and Conformance | 22\. Security, Privacy and Trust Architecture |
| 3\. Target Logical and Service Architecture | 23\. Infrastructure and Deployment Architecture |
| 4\. Multi-Telco Tenancy, Routing and Isolation | 24\. Scalability, Performance and Capacity Engineering |
| 5\. Telco Integration Gateway and Adapter Framework | 25\. Resilience, Failure Handling and Degraded Modes |
| 6\. API Architecture and Contract Standards | 26\. Observability, Audit and SRE Telemetry |

| 7. Event-Driven Architecture and Messaging | 27. Telco Simulator, Sandbox and Certification Harness |  
| 8. Data Architecture, Storage and Lifecycle | 28. Testing, Verification and Quality Engineering |  
| 9. Subscriber Identity, Portability and Lifecycle | 29. DevSecOps, Release and Environment Management |  
| 10. Configuration, Product and Rules Platform | 30. Migration, Dual Run and Cutover Technical Design |  
| 11. Feature Engineering, Scoring and Real-Time Decisioning | 31. Non-Functional Requirement Catalogue |  
| 12. Offer Management and Price Disclosure | 32. Requirement Traceability and Build Work Packages |  
| 13. Advance Origination, Exposure Reservation and Fulfilment Saga | Appendix A - Canonical API and Event Conventions |  
| 14. USSD and Channel Orchestration | Appendix B - Canonical State and Invariant Register |  
| 15. Notification and Communication Service | Appendix C - Core Relational Schema Outline |  
| 16. Recovery, Delinquency and Collections Engine | Appendix D - Error, Retry and Ambiguity Matrix |  
| 17. Ledger, Accounting and Financial Invariants | Appendix E - Minimum Production Readiness Evidence |  
| 18. Reconciliation, Settlement and Financial Control | Appendix F - Requirement Inventory |  
| 19. Treasury, Funding Pools and Portfolio Guardrails | Volume 2 Conclusion |  
| 20. Credit Bureau, Regulatory Evidence and Complaints Interfaces | |

## 1\. Technical Executive Summary

The target solution is a multi-telco, multi-product digital credit platform in which the platform is authoritative for offers, credit decisions, advances, exposure, recoveries, the financial ledger, reconciliation and compliance evidence. Telcos remain authoritative for subscriber/network attributes, channel delivery, airtime and data fulfilment, recharge events and garnishment execution. The technical design therefore treats each credit advance as a distributed business transaction across independently controlled systems.

![image](https://static-us-img.skywork.ai/prod/nexus/1784236741/cropped_image_3_1784236741242157003.jpg)

**Figure 1 - System context and principal systems-of-record boundaries.**

The architecture uses precomputed subscriber features and approved limits for low-latency channel responses, enriched by real-time overlays for exposure, fraud, funding availability, consent status and telco service health. It uses a ledger-led financial core, idempotent APIs, an event log, transactional outbox patterns and explicit ambiguous-fulfilment states to prevent double credit and financial divergence.

**Executive technical requirements**

| ID | Requirement | Priority | Acceptance evidence |
| --- | --- | --- | --- |
| TAR-001 | The platform shall implement the system-of-record boundaries established in Volume 1 and shall not make the telco subscriber balance its authoritative loan ledger. | Must | Architecture review and integration tests |
| TAR-002 | The platform shall support multiple telcos without embedding operator-specific fields, codes or workflows in core credit-domain services. | Must | Second-adapter certification test |
| TAR-003 | Every financially material command and event shall be idempotent, traceable and recoverable after process or network failure. | Must | Duplicate and crash-recovery tests |
| TAR-004 | The synchronous customer path shall read precomputed decision data and shall not execute unbounded historical aggregation. | Must | Performance test and query plan evidence |
| TAR-005 | No component shall report an advance as successful until telco fulfilment is confirmed or subsequently resolved by status enquiry or reconciliation. | Must | Timeout-after-success test |
| TAR-006 | All configurable decisions shall retain the exact effective configuration, model, rule and feature versions used. | Must | Decision replay test |

## 2\. Scope, Architecture Boundaries and Conformance

### 2.1 In-Scope Technical Capabilities

-   Channel orchestration for USSD, telco applications, API clients, web self-service and assisted support channels.
    
-   Telco integration gateway and independently deployable operator adapters.
    
-   Subscriber-account, product, configuration, scoring, offer, advance, fulfilment, recovery, collections, ledger, reconciliation, settlement, treasury, bureau and regulatory services.
    
-   Administrative, risk, finance, operations, support, compliance and technical operations portals.
    
-   Data ingestion, batch feature computation, streaming overlays, online decision stores, analytical stores and controlled data exports.
    
-   Security, tenant isolation, audit, evidence, observability, simulator, test automation and DevSecOps controls.
    

### 2.2 Architecture Conformance

## Architecture governance requirements

| ID | Requirement | Priority | Acceptance evidence |
| --- | --- | --- | --- |
| ARC-001 | Every deployable component shall have a named business capability, owning team, data ownership statement, interface contract and service-level objective. | Must | Service catalogue review |
| ARC-002 | Each material design choice shall be recorded in an Architecture Decision Record with options, rationale, risks and rollback implications. | Must | ADR repository audit |
| ARC-003 | Services shall communicate only through documented synchronous APIs, asynchronous events or approved batch interfaces; direct cross-service database reads are prohibited. | Must | Code and database-access scan |
| ARC-004 | Core domain services shall not depend on telco-specific SDKs; such dependencies shall be isolated within adapter boundaries. | Must | Dependency review |
| ARC-005 | The platform shall maintain a machine-readable interface catalogue covering API versions, event schemas, batch layouts and owning services. | Must | Catalogue export |
| ARC-006 | Breaking changes shall require a version transition plan with parallel support, consumer impact analysis and retirement date. | Must | Contract compatibility tests |
| ARC-007 | Technical debt that weakens a financial, security or tenant-isolation invariant shall not be accepted through ordinary backlog prioritisation. | Must | Architecture waiver process |

## 3\. Target Logical and Service Architecture

![image](https://static-us-img.skywork.ai/prod/nexus/1784236742/cropped_image_9_1784236742766669821.jpg)

**Figure 2 - Logical service architecture and principal domain boundaries.**

### 3.1 Service Decomposition Principles

The service map is a logical decomposition, not a mandate to create a separate network process for every box on day one. Release 1 may combine closely coupled capabilities in a modular monolith or a smaller number of services, provided module boundaries, data ownership and interface contracts preserve a clean path to independent scaling and deployment.

**Service architecture requirements**

| ID | Requirement | Priority | Acceptance evidence |
| --- | --- | --- | --- |
| SRV-001 | A single service or module shall own each mutable aggregate and its state transitions. | Must | Domain ownership matrix |
| SRV-002 | Ledger journals shall be owned exclusively by the ledger service and shall not be directly inserted or updated by other services. | Must | Database permission test |
| SRV-003 | The advance orchestrator shall coordinate the business saga but shall not implement telco-specific transport logic. | Must | Component test |
| SRV-004 | The configuration service shall publish immutable effective versions; consumers shall not read mutable draft configuration. | Must | Activation and rollback tests |
| SRV-005 | The decisioning service shall be stateless for synchronous evaluation except for access to approved feature, rule, model and exposure snapshots. | Should | Load and failover test |
| SRV-006 | Read-heavy portals shall use purpose-built read models or query services rather than loading transactional aggregates repeatedly. | Should | Performance test |
| SRV-007 | Long-running work shall execute asynchronously with resumable job state, checkpoints and deterministic retry behaviour. | Must | Worker crash test |
| SRV-008 | Every service shall expose health, readiness, dependency and version endpoints appropriate to its runtime. | Must | Platform health test |
| SRV-009 | Business commands shall return stable business error codes separate from transport status codes. | Must | Contract tests |
| SRV-010 | Shared libraries shall be limited to technical concerns; domain logic shall not be duplicated through shared packages across services. | Should | Static architecture test |

### 3.2 Recommended Initial Deployment Units

| Deployment unit | Primary capabilities | Independent scaling trigger |
| --- | --- | --- |
| Edge and Channel | API gateway, USSD, portal BFF, request authentication | USSD/session TPS or portal traffic |
| Credit Core | Subscriber account, product/config, decisioning, offers | Offer enquiries and scoring volume |
| Advance Core | Advance, fulfilment saga, recovery coordination | Advance and recharge-event TPS |
| Financial Core | Ledger, reconciliation, settlement, treasury | Journal and reconciliation volume |
| Compliance Core | Bureau, complaints, disclosure evidence, regulatory exports | Reporting and case volumes |
| Data Platform | Feed ingestion, feature pipelines, analytics | Subscriber population and batch window |

| Integration Adapters | One or more independently scaled adapters per telco | Operator-specific load and outages |

## 4\. Multi-Telco Tenancy, Routing and Isolation

### 4.1 Tenant Resolution

A request must be bound to a trusted telco context before any subscriber, offer, advance or financial lookup occurs. The tenant cannot be selected solely from an untrusted payload field. It shall be resolved from authenticated client credentials, network route, certificate identity or an approved gateway mapping and then compared with the payload telco identifier.

**Tenant and routing requirements**

| ID | Requirement | Priority | Acceptance evidence |
| --- | --- | --- | --- |
| TEN-001 | Every tenant-owned record shall include an immutable telco\_id and, where applicable, programme\_id and legal\_entity\_id. | Must | Schema inspection |
| TEN-002 | Inbound credentials shall resolve to an authorised telco context before payload processing. | Must | Wrong-tenant credential test |
| TEN-003 | A payload telco\_id that conflicts with the authenticated context shall be rejected and security-alerted. | Must | Negative integration test |
| TEN-004 | The canonical subscriber key shall not be MSISDN alone; it shall include telco and an effective identity period or internal subscriber\_account\_id. | Must | Porting and recycling tests |
| TEN-005 | All caches, topics, object paths, search documents and metrics dimensions containing tenant data shall include telco scope. | Must | Tenant leakage test |
| TEN-006 | Portal authorisation shall apply both functional permissions and an explicit set of permitted telco/programme scopes. | Must | RBAC/ABAC test |
| TEN-007 | One telco adapter outage, backlog or circuit-breaker state shall not consume all shared worker, connection or queue capacity. | Must | Fault-isolation load test |
| TEN-008 | Encryption keys and secrets shall be separately identifiable by telco and environment; dedicated keys shall be supported where contractually required. | Must | Key inventory review |
| TEN-009 | Cross-telco analytics shall use approved de-identified or aggregated data products and shall not bypass transactional tenant controls. | Must | Data-access audit |
| TEN-010 | The platform shall support promotion of a telco from shared to dedicated infrastructure without changing canonical business interfaces. | Should | Deployment portability exercise |
| TEN-011 | Tenant deletion or offboarding shall be a governed lifecycle that preserves legally required financial and audit records. | Must | Offboarding test |

### 4.2 Data Isolation Strategy

| Layer | Release 1 approach | Scale/dedicated option |
| --- | --- | --- |
| Application | Mandatory telco context propagated in every request and event | Dedicated tenant deployments supported |
| Database | Shared clusters with partitioning, row-level controls and service-owned schemas | Separate database or cluster per major telco |
| Messaging | Shared logical categories partitioned by telco; dedicated topics for high-volume or sensitive programmes | Dedicated broker namespace or cluster |
| Cache | Tenant-prefixed keys and per-tenant quotas | Dedicated cache pools |
| Object storage | Tenant/programme prefixes, policy-enforced access and separate retention | Dedicated buckets/accounts |
| Observability | Tenant-safe labels; PII excluded from metrics and general logs | Dedicated telemetry workspace where required |

## 5\. Telco Integration Gateway and Adapter Framework

### 5.1 Canonical Integration Model

The gateway shall expose stable canonical operations to the core platform while operator adapters translate authentication, field names, error codes, transport protocols, file layouts and timing behaviour. Canonical operations include subscriber-attribute retrieval, offer presentation, fulfilment, fulfilment status enquiry, recharge/recovery event ingestion, reversal, notification delivery status and reconciliation file exchange.

**Telco integration requirements**

| ID | Requirement | Priority | Acceptance evidence |
| --- | --- | --- | --- |
| TEL-001 | Each adapter shall implement a published canonical interface and a capability manifest declaring supported functions and versions. | Must | Adapter certification |
| TEL-002 | Adapter mappings shall preserve the original telco request, response, code and timestamp as evidence while emitting normalised platform fields. | Must | Evidence inspection |
| TEL-003 | Transport retries shall occur only where the operation is proven idempotent or a status-enquiry mechanism resolves ambiguity. | Must | Timeout and duplicate test |
| TEL-004 | Each telco shall have independent connection pools, concurrency limits, rate limits, retry budgets and circuit breakers. | Must | Isolation test |
| TEL-005 | Credentials, certificates and endpoint configurations shall be environment-specific and rotated without code deployment. | Must | Secret rotation test |
| TEL-006 | Adapter configuration changes shall use maker-checker approval and pre-activation connectivity tests. | Must | Configuration workflow test |
| TEL-007 | All inbound telco events shall have a stable event identity or a platform-derived deduplication fingerprint. | Must | Duplicate replay test |
| TEL-008 | Batch files shall use manifest, checksum, record count, schema version and control-total validation before acceptance. | Must | Corrupt and partial-file tests |

| TEL-009 | Unknown or newly introduced telco codes shall be quarantined rather than silently mapped to success or failure. | Must | Unknown-code test |  
| TEL-010 | The platform shall provide per-adapter dashboards for latency, success, ambiguity, retries, throttling and backlog. | Must | Dashboard review |  
| TEL-011 | Adapters shall support status enquiry by platform request ID and telco transaction reference where the telco offers such capability. | Must | Certification test |  
| TEL-012 | The adapter framework shall support synchronous APIs, asynchronous events and secure batch/file exchange without changing core domain contracts. | Must | Multiple transport test |  
| TEL-013 | The platform shall retain evidence needed to prove whether a fulfilment instruction was sent, acknowledged, credited, rejected, reversed or remained unknown. | Must | Dispute evidence test |

### 5.2 Canonical Fulfilment Contract

```http
POST /v1/telcos/{telcoId}/fulfilments
Idempotency-Key: 8d798d35-...
{
    "platform_request_id": "ADVREQ-01J...",
    "subscriber_account_id": "SUB-01J...",
    "msisdn_token": "tok_...",
    "product_type": "AIRTIME_ADVANCE",
    "face_value_minor": 10000,
    "currency": "NGN",
    "offer_snapshot_id": "OFS-01J...",
    "callback_url": "https://.../fulfilment-events"
}
Accepted
{
    "telco_transaction_reference": "MNO-8837...",
    "status": "PENDING"
}
```

No blind retry — A timeout after submitting a fulfilment command is not a confirmed failure. The advance moves to FULFILMENT\_UNKNOWN, and the platform must query or reconcile before any repeat instruction.

## 6\. API Architecture and Contract Standards

### 6.1 API Design

**API requirements**

| ID | Requirement | Priority | Acceptance evidence |
| --- | --- | --- | --- |
| API-001 | External and inter-service APIs shall be versioned, documented in a machine-readable specification and validated in CI. | Must | OpenAPI/contract validation |
| API-002 | Commands that can create financial or customer effects shall require an idempotency key scoped to client, tenant and operation. | Must | Duplicate command test |

| API-003 | The server shall persist the idempotency outcome before returning a final response and shall return the original result for valid retries. | Must | Crash-after-commit test |  
| API-004 | Request IDs, correlation IDs and causation IDs shall be propagated across synchronous and asynchronous boundaries. | Must | Trace inspection |  
| API-005 | Monetary values shall be represented as integer minor units plus ISO currency; binary floating-point shall not be used for accounting values. | Must | Static and unit tests |  
| API-006 | Timestamps shall be UTC, include timezone designators and preserve telco event time separately from receipt and processing time. | Must | Schema tests |  
| API-007 | Sensitive subscriber identifiers shall be tokenised or masked outside authorised service boundaries. | Must | Payload and log scan |  
| API-008 | Pagination shall use stable cursor-based pagination for high-volume mutable datasets. | Should | Pagination consistency test |  
| API-009 | Filtering and sorting fields shall be explicitly allowed to prevent unbounded query construction. | Must | Security test |  
| API-010 | Bulk endpoints shall define maximum items, partial-success semantics, per-item errors and retry guidance. | Must | Contract tests |  
| API-011 | Error responses shall contain stable error\_code, safe message, retryable flag and correlation\_id without stack traces or secrets. | Must | Negative tests |  
| API-012 | API clients shall use bounded timeouts and shall not hold database transactions open across remote calls. | Must | Code review and chaos test |  
| API-013 | Backward-compatible additive changes shall be preferred; field removal or semantic reinterpretation requires a new major version. | Must | Compatibility test |  
| API-014 | Webhook receivers shall authenticate signatures or mTLS identities, validate replay windows and deduplicate events. | Must | Security certification |  
| API-015 | Administrative mutation APIs shall require explicit reason, change ticket or approval reference where policy requires. | Must | Audit test |

### 6.2 Standard Business Error Families

| Family | Examples | Retry guidance |
| --- | --- | --- |
| AUTH\_\* | AUTH\_INVALID\_CLIENT, AUTH\_SCOPE\_DENIED | Do not retry without correcting identity or scope |
| TENANT\_\* | TENANT\_CONTEXT\_MISMATCH | Security event; do not retry unchanged |
| SUBSCRIBER\_\* | SUBSCRIBER\_INACTIVE, NIN\_NOT\_VERIFIED | Retry only after source status changes |

| OFFER\_\* | OFFER\_EXPIRED, OFFER\_SNAPSHOT\_MISMATCH | Refresh offers |  
| ADVANCE\_\* | ADVANCE\_LIMIT\_EXCEEDED,CONCURRENT\_ADVANCE\_BLOCK | Do not retry unchanged |  
| FULFILMENT\_\* | FULFILMENT\_PENDING,FULFILMENT\_UNKNOWN | Use status enquiry; never blind-retry |  
| FUNDING\_\* | FUNDING\_POOL\_EXHAUSTED | Retry only after treasury state changes |  
| RATE\_\* | RATE\_LIMITED | Retry after server-provided delay |  
| SYSTEM\_\* | SYSTEM\_TEMPORARILY\_UNAVAILABLE | Bounded retry with jitter |

## 7\. Event-Driven Architecture and Messaging

![image](https://static-us-img.skywork.ai/prod/nexus/1784236742/cropped_image_3_1784236742643736941.jpg)

Figure 3 - Event publication, retry, dead-letter and controlled replay architecture.

**Messaging requirements**

| ID | Requirement | Priority | Acceptance evidence |
| --- | --- | --- | --- |
| EVT-001 | Domain events shall describe facts that have committed, use past-tense names and include event\_id, event\_type, schema\_version, telco\_id, aggregate\_id and occurred\_at. | Must | Schema validation |
| EVT-002 | A transactional outbox or equivalent atomic mechanism shall ensure database state and event publication cannot diverge silently. | Must | Crash and recovery test |
| EVT-003 | Consumers shall be idempotent and store processing outcomes or deduplication keys for the required replay window. | Must | Duplicate event test |
| EVT-004 | Ordering shall be guaranteed only within a documented partition key, normally telco\_id plus aggregate or subscriber account. | Must | Ordering test |
| EVT-005 | Consumers shall handle out-of-order events using sequence numbers, effective timestamps or pending-match logic where order cannot be guaranteed. | Must | Reversal-before-original test |
| EVT-006 | Schemas shall be governed in a registry and compatibility checked before deployment. | Must | CI schema compatibility |
| EVT-007 | Retry topics shall use bounded attempts, exponential backoff and reason-specific retry policies. | Must | Failure injection test |
| EVT-008 | Dead-letter records shall retain original payload, headers, failure reason, attempt history and consumer version. | Must | DLQ inspection |
| EVT-009 | Replay shall require authorised initiation, bounded scope, dry-run counts and idempotency assurance. | Must | Replay control test |

| EVT-010 | PII shall be minimised in event payloads; tokens or internal identifiers shall be preferred. | Must | Event catalogue review |  
| EVT-011 | Event retention shall support reconciliation, incident investigation and legally required evidence without turning the broker into the long-term archive. | Must | Retention configuration review |  
| EVT-012 | Backpressure controls shall prioritise financial recovery and ledger events above analytics and non-critical notification workloads. | Must | Load-shedding test |  
| EVT-013 | Event lag and oldest-unprocessed-event age shall be monitored per telco, partition and consumer. | Must | Dashboard and alert test |  
| EVT-014 | A canonical event envelope shall permit transport migration without changing domain semantics. | Should | Adapter test |

### 7.1 Core Event Catalogue

| Event | Producer | Primary consumers | Partition key |
| --- | --- | --- | --- |
| SubscriberFeaturesUpdated | Feature Pipeline | Decision Store, Analytics | telco\_id + subscriber\_account\_id |
| OfferGenerated / OfferExpired | Offer Service | Channel, Analytics, Audit | telco\_id + subscriber\_account\_id |
| AdvanceRequested | Advance Service | Fraud, Audit | telco\_id + subscriber\_account\_id |
| ExposureReserved | Advance/Treasury | Ledger, Risk | telco\_id + programme\_id |
| FulfilmentSubmitted | Fulfilment Saga | Reconciliation, Audit | telco\_id + advance\_id |
| FulfilmentConfirmed / Unknown / Failed | Fulfilment Saga | Advance, Ledger, Support | telco\_id + advance\_id |
| RechargeReceived | Telco Adapter | Recovery | telco\_id + subscriber\_account\_id |
| RecoveryApplied / Reversed | Recovery | Ledger, Collections, Reconciliation | telco\_id + advance\_id |
| LedgerJournalPosted | Ledger | Reporting, Reconciliation | legal\_entity\_id + journal\_id |
| SettlementCalculated | Settlement | Finance Portal, Treasury | telco\_id + settlement\_cycle |
| BureauRecordPrepared | Bureau Service | Compliance, Export | legal\_entity\_id + subscriber\_account\_id |

## 8\. Data Architecture, Storage and Lifecycle

![image](https://static-us-img.skywork.ai/prod/nexus/1784236743/cropped_image_5_1784236743166136831.jpg)

**Figure 4 - Purpose-specific data stores and analytical flow.**

### 8.1 Data Ownership and Store Selection

**Data architecture requirements**

| ID | Requirement | Priority | Acceptance evidence |
| --- | --- | --- | --- |
| DAT-001 | Each data entity shall have a named system-of-record service, retention class, classification, legal basis and permitted consumers. | Must | Data catalogue audit |
| DAT-002 | Transactional stores shall enforce tenant and aggregate integrity through keys, constraints and service-level permissions, not application convention alone. | Must | Schema and permission test |
| DAT-003 | Financial journals shall be append-only and protected from ordinary update/delete permissions. | Must | Database permission test |
| DAT-004 | Derived balances and summaries shall be reproducible from authoritative journals or source events. | Must | Rebuild test |
| DAT-005 | Online decision data shall be separated from raw historical feeds and optimised for predictable point lookup. | Must | Performance test |
| DAT-006 | Raw inbound files and records shall be retained immutably with checksum, source, ingestion time and schema version for the approved evidence period. | Must | Evidence retrieval test |
| DAT-007 | Data corrections shall create new versions or compensating records rather than overwrite evidence of prior received values. | Must | Correction test |
| DAT-008 | Subscriber MSISDN values shall be encrypted or tokenised and displayed masked by default. | Must | Security test |
| DAT-009 | Date-partitioned high-volume tables shall support pruning, retention and archival without locking the active workload. | Should | Partition maintenance test |
| DAT-010 | Database migrations shall be backward-compatible with rolling deployment and include tested rollback or roll-forward procedures. | Must | Migration rehearsal |
| DAT-011 | Analytical data shall be sourced through governed replication/events rather than queries against production OLTP at scale. | Must | Architecture and load test |
| DAT-012 | Search indexes and caches shall be treated as derived, rebuildable stores and shall never be the sole record of a financial fact. | Must | Disaster rebuild test |
| DAT-013 | Retention and deletion jobs shall be idempotent, auditable and tenant-scoped, with legal holds overriding automated deletion. | Must | Retention test |
| DAT-014 | Backups shall be encrypted, integrity-tested and periodically restored into an isolated environment. | Must | Restore evidence |
| DAT-015 | Data quality metrics shall include completeness, timeliness, uniqueness, validity and reconciliation to telco controls. | Must | Data-quality dashboard |

### 8.2 Principal Data Entities

| Entity | Authoritative owner | Key identifiers | Notes |
| --- | --- | --- | --- |
| Telco / Programme | Configuration Service | telco\_id, programme\_id | Commercial, risk and integration scope |
| Subscriber Account | Subscriber Service | subscriber\_account\_id, telco\_id, effective period | Protects against porting/recycling ambiguity |
| Feature Snapshot | Feature Platform | feature\_snapshot\_id, as\_of\_time | Immutable model inputs |
| Decision Record | Decisioning | decision\_id, config/model versions | Explainable and replayable |
| Offer | Offer Service | offer\_id, offer\_snapshot\_id | Separate lifecycle from advance |
| Advance | Advance Service | advance\_id, external request IDs | Business state and exposure |
| Fulfilment Attempt | Fulfilment Saga | attempt\_id, telco reference | Evidence of distributed transaction |
| Recovery Allocation | Recovery Service | recovery\_id, recharge event ID | Supports partial and multi-advance allocation |
| Journal / Entry | Ledger | journal\_id, entry\_id | Append-only double entry |
| Reconciliation Item | Reconciliation | recon\_item\_id, source refs | Match, break and resolution |
| Settlement Statement | Settlement | statement\_id, cycle | Party obligations and taxes |
| Disclosure Acknowledgement | Compliance | ack\_id, content hash/version | Retained per advance |

## 9\. Subscriber Identity, Portability and Lifecycle

## Subscriber identity requirements

| ID | Requirement | Priority | Acceptance evidence |
| --- | --- | --- | --- |
| SUB-001 | A subscriber account shall represent the relationship between an MSISDN and a telco for an effective period, not an eternal identity. | Must | Porting/recycling test |
| SUB-002 | The platform shall support MSISDN port-out, port-in, disconnection, recycle and reactivation events without merging unrelated customer histories. | Must | Lifecycle test |
| SUB-003 | A recycled number shall not inherit offers, consent, outstanding exposure or adverse history without an approved verified identity-linking rule. | Must | Recycle edge-case test |
| SUB-004 | NIN status shall be represented as source-provided verification flags and timestamps; the platform shall not require raw NIN where a verified flag suffices. | Must | Data-minimisation review |
| SUB-005 | Subscriber status changes that create immediate risk shall be available as real-time overlays or frequent delta feeds. | Must | SIM-swap/barred status test |
| SUB-006 | The platform shall preserve source-system timestamps and distinguish unknown from false for identity and eligibility attributes. | Must | Schema and rule test |
| SUB-007 | Subscriber merge or split operations shall require privileged approval, reason, before/after evidence and financial impact checks. | Must | Administrative workflow test |

| SUB-008 | A support user shall view a masked subscriber profile, offers, advance history and evidence only within authorised telco scope. | Must | Portal access test |  
| SUB-009 | The platform shall support subscriber self-exclusion and programme opt-out states that immediately suppress new offers. | Must | Channel and decision test |  
| SUB-010 | Identity-resolution confidence shall be retained where cross-telco or bureau matching is performed. | Should | Matching test |

## 10\. Configuration, Product and Rules Platform

### 10.1 Configuration Lifecycle

Configuration is a governed product capability. Drafts are editable; approved versions are immutable and effective-dated. Activation is preceded by validation, simulation and, for high-risk changes, canary exposure. Every decision stores the effective version identifiers.

**Configuration requirements**

| ID | Requirement | Priority | Acceptance evidence |
| --- | --- | --- | --- |
| CFG-001 | Configuration shall support Draft, Submitted, Approved, Scheduled, Active, Superseded, RolledBack and Rejected states. | Must | Workflow test |
| CFG-002 | The maker of a material configuration change shall not approve the same change. | Must | Segregation-of-duties test |
| CFG-003 | Activation shall be atomic from the perspective of decisioning; a request shall not observe a mixture of configuration versions. | Must | Concurrent activation test |
| CFG-004 | Every active configuration shall have effective\_from, optional effective\_to, owner, approval, reason and content hash. | Must | Schema inspection |
| CFG-005 | High-risk changes shall require simulation against historical cohorts before approval. | Must | Simulation evidence |
| CFG-006 | The system shall detect impossible or unsafe values, overlapping effective periods and missing dependencies before activation. | Must | Validation tests |
| CFG-007 | Rollback shall activate a prior approved version or a new corrective version without mutating history. | Must | Rollback test |
| CFG-008 | Feature flags and kill switches shall be scoped by environment, telco, programme, product, channel and cohort where applicable. | Must | Feature-flag test |
| CFG-009 | Secrets shall never be stored as ordinary product configuration values. | Must | Secret scan |
| CFG-010 | Configuration export/import shall be signed, versioned and environment-controlled to prevent accidental production promotion. | Must | Promotion test |

| CFG-011 | Rule and score configuration shall have human-readable explanations suitable for audit and customer-support interpretation. | Must | Explainability review |  
| CFG-012 | Ledger posting templates shall not activate unless debits equal credits per currency under all permitted branches. | Must | Template balance validation |  
| CFG-013 | Portfolio guardrail thresholds shall include safe defaults and cannot be disabled without elevated approval. | Must | Control override test |  
| CFG-014 | A complete configuration dependency graph shall be available to show which products, rules, channels and programmes use a version. | Should | Impact analysis demonstration |

### 10.2 Configurable Product Definition

| Domain | Examples of configurable values | Non-configurable boundary |
| --- | --- | --- |
| Eligibility | SIM tenure, NIN flag, active status, delinquency block | Tenant isolation and audit cannot be bypassed |
| Tiers and limits | Denominations, max exposure, one-tier movement | No negative limits; exposure cannot exceed funding/portfolio caps |
| Fees | Percentage, flat, inclusive/upfront treatment, taxes | Money represented in minor units; posting must balance |
| Concurrency | max\_concurrent\_advances default 1 | Race-free exposure reservation required |
| Recovery | priority, allocation, partial recovery, grace | No over-recovery; reversals preserved |
| Notifications | templates, languages, quiet hours, sender IDs | Consent/DND and conduct controls prevail |
| Settlement | cycles, revenue shares, tax lines, tolerances | Journal and statement totals must reconcile |

## 11\. Feature Engineering, Scoring and Real-Time Decisioning

![image](https://static-us-img.skywork.ai/prod/nexus/1784236745/cropped_image_5_1784236745847024875.jpg)

> Figure 5 - Batch feature computation with anti-gaming controls and real-time overlays.

### 11.1 Feature Pipeline

**Feature and scoring requirements**

| ID | Requirement | Priority | Acceptance evidence |
| --- | --- | --- | --- |
| SCR-001 | The feature platform shall compute configurable windows including recent, medium-term and long-term recharge, usage and activity measures. | Must | Feature calculation tests |
| SCR-002 | Features shall record source coverage, as-of time, missingness and quality flags. | Must | Feature-store inspection |

| SCR-003 | Single-period recharge totals shall not directly determine limits without stability, historical and anomaly controls. | Must | Gaming simulation |  
| SCR-004 | Anti-gaming processing shall support medians, trimmed means, winsorisation, percentile caps, spike ratios and repeated-behaviour validation. | Must | Model test pack |  
| SCR-005 | The platform shall distinguish behavioural risk, affordability, trust/repayment and fraud dimensions rather than collapse all inputs into an opaque score. | Must | Decision explanation test |  
| SCR-006 | The assigned limit shall be constrained by product, subscriber, programme, telco, funding and portfolio caps. | Must | Limit waterfall tests |  
| SCR-007 | Upward tier movement shall default to at most one tier per scoring cycle and be configurable through approved policy. | Must | Tier movement test |  
| SCR-008 | Downward movement or suspension may occur immediately when risk overlays require it. | Must | Fraud/funding overlay test |  
| SCR-009 | A new or thin-file subscriber shall use a configurable starter policy and progressive-trust path. | Must | Cold-start tests |  
| SCR-010 | Model/rule execution shall generate reason codes, component contributions and policy constraints for every decision. | Must | Explainability output |  
| SCR-011 | Decision replay shall reproduce the original result from retained feature, rule, model and configuration versions, subject to deterministic implementation. | Must | Replay test |  
| SCR-012 | Champion/challenger evaluation shall not change customer outcomes unless the challenger is explicitly authorised for a controlled cohort. | Must | Experiment isolation test |  
| SCR-013 | Training and analytical datasets shall prevent leakage from future repayment outcomes into historical features. | Must | Data-science validation |  
| SCR-014 | Model deployment shall require validation evidence, approval, monitoring thresholds and rollback readiness. | Must | Model governance pack |  
| SCR-015 | Real-time decision evaluation shall apply current exposure, outstanding advances, self-exclusion, telco status, fraud flags, funding and guardrail states. | Must | Overlay tests |  
| SCR-016 | A stale feature snapshot shall be accepted, degraded or rejected according to explicit product policy and age thresholds. | Must | Staleness tests |  
| SCR-017 | Missing required features shall not be silently imputed unless the approved model explicitly defines the imputation. | Must | Missing-data tests |

| SCR-018 | The platform shall monitor approval rate, limit distribution, delinquency, recovery and drift by telco, cohort and model version. | Must | Monitoring dashboard |

### 11.2 Canonical Decision Result

```json
{
  "decision_id": "DEC-01J...",
  "eligible": true,
  "maximum_face_value_minor": 50000,
  "tier_code": "TIER_04",
  "permitted_denominations_minor": [10000, 20000, 50000],
  "reason_codes": ["TENURE_OK", "REPAYMENT_TRUST_HIGH", "SPIKE_DISCOUNT_APPLIED"],
  "feature_snapshot_id": "FTR-01J...",
  "model_version": "risk-3.2.1",
  "rule_set_version": "mtn-ng-airtime-17",
  "configuration_version": "cfg-01J...",
  "valid_until": "2026-07-17T00:00:00Z"
}
```

## 12\. Offer Management and Price Disclosure

**Offer requirements**

| ID | Requirement | Priority | Acceptance evidence |
| --- | --- | --- | --- |
| OFR-001 | An offer shall be a separate entity from an advance and shall have Generated, Presented, Accepted, Expired, Withdrawn and Superseded states. | Must | State-machine tests |
| OFR-002 | The offer snapshot shall freeze denomination, fee, taxes, net value, repayment amount, product and disclosure version for the acceptance window. | Must | Snapshot test |
| OFR-003 | Offer acceptance shall fail safely when the offer has expired, been withdrawn or no longer passes critical real-time overlays. | Must | Acceptance race test |
| OFR-004 | A material price or disclosure change shall generate a new offer and require fresh acceptance. | Must | Disclosure version test |
| OFR-005 | Offer lists shall contain only denominations within the approved maximum and current product configuration. | Must | Offer-generation test |
| OFR-006 | The platform shall retain evidence of what was displayed or delivered, in which language, through which channel and when. | Must | Evidence retrieval |
| OFR-007 | Where USSD cannot display all disclosure text, the channel shall present mandatory summary fields and deliver the retained full disclosure through an approved complementary method. | Must | USSD compliance test |
| OFR-008 | An offer shall not reserve funding or exposure until the configured reservation point, normally acceptance or advance request. | Should | Concurrency test |
| OFR-009 | Offer generation shall be idempotent within the configured request context and may return an existing still-valid offer snapshot. | Should | Duplicate enquiry test |

## 13\. Advance Origination, Exposure Reservation and Fulfilment Saga

![image](https://static-us-img.skywork.ai/prod/nexus/1784236744/cropped_image_2_1784236744143808628.jpg)

**Figure 6 - Advance and fulfilment saga, including ambiguous outcomes.**

### 13.1 Advance State Model

| State | Entered when | Permitted exits | Financial effect |
| --- | --- | --- | --- |
| REQUESTED | A unique customer acceptance/request is received | VALIDATED, DECLINED | No journal; idempotency record created |
| VALIDATED | Offer, consent and current overlays pass | EXPOSURE\_RESERVED, DECLINED | No customer receivable yet |
| EXPOSURE\_RESERVED | Subscriber/programme/funding capacity reserved | PENDING\_FULFILMENT, CANCELLED | Memo or reservation record only |
| PENDING\_FULFILMENT | Instruction submitted or ready for telco | ACTIVE, FULFILMENT\_FAILED, FULFILMENT\_UNKNOWN | No principal receivable until confirmed according to accounting policy |
| FULFILMENT\_UNKNOWN | Outcome cannot be determined safely | ACTIVE, FULFILMENT\_FAILED, MANUAL\_RESOLUTION | Reservation remains; blind retry prohibited |
| ACTIVE | Telco credit confirmed | PARTIALLY\_RECOVERED, CLOSED, WRITTEN\_OFF, REVERSED | Advance-issued journal and receivable recognised |
| PARTIALLY\_RECOVERED | One or more recoveries applied | CLOSED, WRITTEN\_OFF, REVERSED | Receivable reduced through journals |
| CLOSED | Outstanding amount is zero | REOPENED only by controlled reversal | No outstanding receivable |
| FULFILMENT\_FAILED | No credit occurred | Terminal or REQUESTED via new request | Reservation released; no principal receivable |

**Advance and saga requirements**

| ID | Requirement | Priority | Acceptance evidence |
| --- | --- | --- | --- |
| ADV-001 | Advance creation shall require an accepted valid offer snapshot and retained disclosure acknowledgement. | Must | Origination test |
| ADV-002 | The advance service shall enforce max\_concurrent\_advances and total exposure atomically under concurrent requests. | Must | High-concurrency test |
| ADV-003 | Exposure and funding reservation shall use atomic conditional updates or serialisable controls that prevent over-allocation. | Must | Race test |
| ADV-004 | A duplicate request with the same idempotency key shall return the same advance and outcome. | Must | Duplicate test |
| ADV-005 | The platform shall preserve separate client request ID, platform advance ID, fulfilment attempt ID and telco transaction reference. | Must | Traceability test |

| ADV-006 | Remote fulfilment shall not occur inside an open local database transaction. | Must | Code review |  
| ADV-007 | State transitions shall use optimistic version checks or equivalent concurrency controls. | Must | Concurrent update test |  
| ADV-008 | Only permitted transitions shall be accepted; invalid transitions shall be rejected and audited. | Must | State invariant test |  
| ADV-009 | FULFILMENT\_UNKNOWN shall trigger status enquiry and reconciliation workflow with configurable escalation timers. | Must | Ambiguous outcome test |  
| ADV-010 | A confirmed failed fulfilment shall release reservations exactly once. | Must | Failure and duplicate callback test |  
| ADV-011 | A late success received after a local failure assumption shall activate the same advance and reverse any released reservation effect without creating a second advance. | Must | Late callback test |  
| ADV-012 | Partial fulfilment shall be rejected unless the product explicitly supports it; supported partial value shall create a correctly repriced or adjusted advance with fresh evidence. | Must | Partial fulfilment test |  
| ADV-013 | Reversal of telco value after activation shall follow a controlled reversal state and financial compensation flow. | Must | Fulfilment reversal test |  
| ADV-014 | A manual resolution action shall require privileged approval, evidence, reason and balanced financial treatment. | Must | Manual repair test |  
| ADV-015 | Advance timestamps shall retain customer acceptance, platform receipt, validation, fulfilment submission, telco event and activation times. | Must | Audit inspection |  
| ADV-016 | The customer-facing response shall not expose internal ambiguity details but shall provide a safe status and SMS fallback where required. | Must | Channel test |

## 14\. USSD and Channel Orchestration

### 14.1 USSD Session Architecture

USSD is treated as a first-class stateful channel over an unreliable and time-bounded transport. The channel orchestrator stores a short-lived session context, but all financially significant actions are executed through idempotent domain commands so a session loss cannot duplicate credit.

**Channel and USSD requirements**

| ID | Requirement | Priority | Acceptance evidence |
| --- | --- | --- | --- |
| CHN-001 | USSD menu flows shall be versioned by telco, shortcode, language, product and effective period. | Must | Flow-version test |

| CHN-002 | Session state shall include tenant, session ID, subscriber account, selected offer snapshot, step, language and expiry, but shall not itself be the financial record. | Must | Session inspection |  
| CHN-003 | The session timeout budget shall be configurable by telco and monitored at each step. | Must | Timeout simulation |  
| CHN-004 | The confirm action shall submit an idempotent advance command before rendering the final response. | Must | Duplicate confirm test |  
| CHN-005 | When the session ends after confirmation but before response, the platform shall complete safely and send an SMS or approved fallback notification. | Must | Session-drop test |  
| CHN-006 | Repeated user input caused by gateway retries shall not repeat advance creation. | Must | Gateway replay test |  
| CHN-007 | The menu shall present total repayment, value received, fee/cost and confirmation wording within regulatory and channel constraints. | Must | Content review |  
| CHN-008 | Language packs shall support English and configurable local languages without embedding business values in text templates. | Should | Localisation test |  
| CHN-009 | A customer shall be able to check eligibility, available offers, outstanding balance and recent advance status where the telco channel permits. | Must | Journey tests |  
| CHN-010 | USSD inputs shall be constrained to expected values and lengths; free text shall not reach domain queries or logs unsanitised. | Must | Security test |  
| CHN-011 | The platform shall distinguish a new session from a continuation and reject stale continuation tokens. | Must | Session replay test |  
| CHN-012 | Session data shall expire automatically and shall not be used as consent evidence after expiry. | Must | TTL test |  
| CHN-013 | Channel analytics shall track abandonment, step latency, failures, session cost, conversion and post-confirmation drops per telco. | Must | Analytics dashboard |  
| CHN-014 | Channel availability controls shall allow offer enquiries to be disabled independently from recovery ingestion and finance processing. | Must | Kill-switch test |  
| CHN-015 | A telco app or API journey shall consume the same offer and advance domain APIs as USSD and shall not implement separate credit logic. | Must | Cross-channel parity test |  
| CHN-016 | Accessibility and plain-language standards shall apply to web/app surfaces, including clear cost and repayment information. | Must | UX/accessibility test |

### 14.2 Canonical USSD Flow

| Step | Request | Platform behaviour | Failure-safe response |
| --- | --- | --- | --- |
| 1\. Start | Session + MSISDN | Resolve tenant and subscriber; load eligible products | Generic unavailable or no-offer response |
| 2\. Offer menu | Selection context | Return active offer snapshot(s) | Refresh or end if expired |
| 3\. Disclosure | Offer ID | Render total cost and terms; record presentation | SMS full terms if configured |
| 4\. Confirm | Offer ID + confirmation | Idempotent advance request and reservation | Processing message; never invite immediate blind retry |
| 5\. Fulfil | Internal saga | Submit to telco and resolve outcome | SMS fallback on session loss |
| 6\. Complete | Outcome | Show success, decline or safe pending status | Support reference and notification |

## 15\. Notification and Communication Service

**Notification requirements**

| ID | Requirement | Priority | Acceptance evidence |
| --- | --- | --- | --- |
| NOT-001 | Notification templates shall be versioned, language-specific, programme-scoped and approved before activation. | Must | Template workflow test |
| NOT-002 | Messages shall separate transactional, servicing, collections and marketing purposes for consent and DND enforcement. | Must | Policy test |
| NOT-003 | The service shall support SMS at minimum and permit push, email, WhatsApp or IVR adapters without changing domain events. | Should | Adapter test |
| NOT-004 | Delivery attempts, provider references, statuses and final outcomes shall be retained. | Must | Delivery evidence |
| NOT-005 | Retry policy shall depend on message criticality and provider error; permanent failures shall not loop. | Must | Failure simulation |
| NOT-006 | Quiet hours and contact-frequency caps shall be configurable and conduct-controlled. | Must | Scheduling test |
| NOT-007 | Sender identity shall be configured per telco/programme and validated against approved registrations. | Must | Configuration test |
| NOT-008 | Templates shall use allowlisted variables and escape untrusted values. | Must | Injection test |
| NOT-009 | A notification failure shall not roll back a completed advance or recovery; it shall create a servicing exception and fallback where configured. | Must | Failure test |
| NOT-010 | Customer opt-out and self-exclusion shall be enforced immediately for non-mandatory communications. | Must | Preference test |

## 16\. Recovery, Delinquency and Collections Engine

### 16.1 Recharge and Garnishment Processing

**Recovery requirements**

| ID | Requirement | Priority | Acceptance evidence |
| --- | --- | --- | --- |
| COL-001 | Every recharge, deduction, recovery and reversal event shall be deduplicated by telco and source event identity. | Must | Duplicate event test |
| COL-002 | Recovery allocation shall be deterministic and versioned, with default oldest-due-first unless programme policy states otherwise. | Must | Allocation tests |
| COL-003 | A recovery shall not exceed total outstanding exposure; excess shall remain with the telco/customer according to the agreed rail behaviour. | Must | Over-recovery test |
| COL-004 | Partial recovery shall reduce the correct principal, fee and tax components according to configured waterfall and accounting policy. | Must | Partial repayment test |
| COL-005 | Recovery posting and advance balance update shall be atomic within the platform financial boundary. | Must | Crash test |
| COL-006 | An out-of-order reversal received before the original recovery shall be held and matched or quarantined without creating a negative recovery. | Must | Ordering edge-case test |
| COL-007 | Recovery after write-off shall post to the approved recovery income/receivable treatment while preserving original write-off history. | Must | Post-write-off recovery test |
| COL-008 | The platform shall support one active advance by default and configurable multiple advances, with explicit allocation across them. | Must | Concurrency/allocation test |
| COL-009 | Recharge events that cannot be linked confidently to a subscriber account shall enter an exception queue and shall not be guessed. | Must | Identity exception test |
| COL-010 | Collections aging shall use configurable buckets and event time rules while preserving original due/activation dates. | Must | Aging test |
| COL-011 | Dunning schedules shall observe quiet hours, frequency caps, language, self-exclusion scope and conduct rules. | Must | Campaign simulation |
| COL-012 | The product shall not escalate to field or harassing collection methods; permitted strategies shall be explicitly configured and approved. | Must | Conduct review |
| COL-013 | Write-off shall require policy eligibility, maker-checker approval or authorised automated threshold and balanced journal posting. | Must | Write-off test |

| COL-014 | A recovery reversal that reopens a closed advance shall reinstate the correct outstanding amount and delinquency position. | Must | Reopen test |  
| COL-015 | Customer balance enquiries shall derive from authoritative recovery and ledger state, not stale telco-only balances. | Must | Balance reconciliation test |

### 16.2 Delinquency States

| State | Illustrative trigger | System action |
| --- | --- | --- |
| CURRENT | Within configured initial period | No dunning or service message only |
| EARLY | Age exceeds early threshold | Reminder cadence and risk-overlay downgrade |
| DELINQUENT | Age exceeds delinquent threshold | Block new advances; collections workflow |
| LATE | Age exceeds late threshold | Enhanced reporting and write-off assessment |
| WRITTEN\_OFF | Approved accounting trigger | Write-off journal; continue passive recovery if policy permits |
| RECOVERED\_AFTER\_WRITE\_OFF | Subsequent garnishment received | Recovery journal and portfolio reporting |

## 17\. Ledger, Accounting and Financial Invariants

### 17.1 Ledger Model

The financial core uses immutable double-entry journals. Business services request posting of a named accounting event with validated dimensions; the ledger resolves an approved posting template, validates balance and writes the journal atomically. Corrections occur only through linked reversing and replacement journals.

## Ledger requirements

| ID | Requirement | Priority | Acceptance evidence |
| --- | --- | --- | --- |
| LED-001 | Every journal shall balance debits and credits per currency before commit. | Must | Property and database tests |
| LED-002 | An unbalanced or incomplete posting template shall fail validation and cannot activate. | Must | Configuration activation test |
| LED-003 | Posted journals and entries shall be immutable; corrections shall use linked reversal and replacement journals. | Must | Permission and reversal tests |
| LED-004 | Journal posting shall be idempotent using a unique business event key and event type. | Must | Duplicate posting test |
| LED-005 | The ledger shall retain legal entity, telco, programme, product, advance, subscriber token, funding source, currency and accounting period dimensions where applicable. | Must | Journal inspection |
| LED-006 | No journal shall be created for a fulfilment that is merely pending or unknown unless the approved accounting policy explicitly requires a memorandum/reservation entry. | Must | Saga accounting test |

| LED-007 | The ledger shall support principal receivable, fee income, tax, telco share, platform share, funder payable/cost, cash/settlement and write-off accounts. | Must | Chart-of-accounts mapping |  
| LED-008 | Derived account balances shall be rebuildable from entries and compared periodically with stored summaries. | Must | Rebuild reconciliation |  
| LED-009 | Posting templates shall be effective-dated and the journal shall retain the template version used. | Must | Version trace test |  
| LED-010 | A closed accounting period shall reject ordinary backdated posting and require controlled adjustment-period treatment. | Must | Period-close test |  
| LED-011 | Manual journals shall be exceptional, maker-checker approved, reasoned and separately reported. | Must | Manual journal workflow |  
| LED-012 | The ledger shall preserve source event, causation, correlation and reversal links. | Must | Audit trace test |  
| LED-013 | Currency rounding shall use an approved deterministic policy and shall post explicit rounding differences where unavoidable. | Must | Rounding tests |  
| LED-014 | Ledger queries used for statements and reconciliation shall be reproducible as-of a specified cutoff. | Must | As-of query test |  
| LED-015 | Database permissions shall prevent application users and non-ledger services from modifying journal tables. | Must | Access-control test |  
| LED-016 | Journal throughput shall scale independently from customer channel services. | Should | Load test |

### 17.2 Illustrative Accounting Events

| Accounting event | Illustrative debit | Illustrative credit | Trigger |
| --- | --- | --- | --- |
| ADVANCE\_ISSUED | Subscriber receivable / advance asset | Telco inventory/funding payable or clearing | Confirmed fulfilment |
| FEE\_RECOGNISED | Subscriber receivable or net funding value | Fee income / deferred fee according to policy | Confirmed fulfilment |
| TAX\_ACCRUED | Subscriber receivable or fee expense | Tax payable | Confirmed fulfilment or settlement policy |
| RECOVERY\_APPLIED | Telco settlement receivable / cash clearing | Subscriber receivable | Confirmed garnishment |
| RECOVERY\_REVERSED | Subscriber receivable | Telco settlement receivable / cash clearing | Recovery reversal |
| WRITE\_OFF | Credit loss expense / allowance | Subscriber receivable | Approved write-off |
| POST\_WRITE\_OFF\_RECOVERY | Cash/telco receivable | Recovery income / written-off asset recovery | Subsequent recovery |
| FUNDING\_COST\_ACCRUED | Funding cost expense | Funder payable | Accrual cycle |
| REVENUE\_SHARE\_ACCRUED | Revenue-share expense or allocation | Telco/funder payable | Settlement cycle |

## 18\. Reconciliation, Settlement and Financial Control

**Reconciliation requirements**

| ID | Requirement | Priority | Acceptance evidence |
| --- | --- | --- | --- |
| REC-001 | The platform shall reconcile advance requests, telco fulfilments, active advances, recoveries, reversals, ledger journals and settlement statements through separate but linked controls. | Must | Daily reconciliation pack |
| REC-002 | Every reconciliation process shall record source cutoffs, control totals, records compared, match rules, tolerances and run version. | Must | Run evidence |
| REC-003 | Exact reference matching shall precede tolerant or composite matching; fuzzy matching shall not automatically resolve financial breaks. | Must | Match-rule test |
| REC-004 | Breaks shall have type, value, age, owner, status, evidence, resolution and financial-impact fields. | Must | Exception workflow test |
| REC-005 | A reconciliation rerun shall be idempotent and shall preserve prior run evidence. | Must | Rerun test |
| REC-006 | Late telco records shall update the relevant reconciliation period without deleting prior break history. | Must | Late file test |
| REC-007 | Control totals shall include counts, face value, fees, recoveries, reversals and settlement amounts by telco/programme/currency. | Must | Control-total test |
| REC-008 | Settlement calculation shall use approved revenue-share, funding, tax and rounding configuration effective for the underlying transaction. | Must | Settlement calculation test |
| REC-009 | Settlement statements shall be versioned and adjustments shall be separately identifiable rather than silently changing issued statements. | Must | Adjustment test |
| REC-010 | Finance users shall be able to trace a settlement line to journals, recoveries, advances and telco source records. | Must | Drill-down test |
| REC-011 | Tolerance-based auto-resolution shall be configurable, capped and reported; material breaks require approval. | Must | Tolerance test |
| REC-012 | Reconciliation backlogs and unquantified breaks shall trigger escalation and may suspend originations where exposure cannot be trusted. | Must | Guardrail test |
| REC-013 | Files and API extracts used for settlement shall be signed/encrypted and include manifests and checksums. | Must | Exchange certification |
| REC-014 | The platform shall support telco, platform, funder, tax and legal-entity views of settlement obligations. | Must | Statement test |

### 18.1 Reconciliation Layers

| Layer | Comparison | Example break |
| --- | --- | --- |
| Instruction | Platform fulfilment submissions vs telco acknowledgements | Submitted but no telco reference |
| Fulfilment | Platform active advances vs telco credited value | Credit confirmed by one side only |
| Recovery | Recharge/garnishment events vs platform allocations | Deducted by telco but not applied |
| Ledger | Domain events vs journals and balances | Active advance without journal |
| Settlement | Ledger/recovery values vs party statement | Revenue share or tax variance |
| Cash | Settlement statement vs bank receipt/payment | Statement paid short or late |

## 19\. Treasury, Funding Pools and Portfolio Guardrails

**Treasury requirements**

| ID | Requirement | Priority | Acceptance evidence |
| --- | --- | --- | --- |
| TRE-001 | Each programme shall be linked to one or more funding pools with currency, owner, available amount, committed amount, exposure cap and status. | Must | Funding configuration test |
| TRE-002 | Exposure reservation shall reduce available capacity atomically and release exactly once on failure or cancellation. | Must | Concurrency test |
| TRE-003 | Originations shall stop automatically when funding capacity, programme exposure or portfolio guardrail thresholds are breached. | Must | Circuit-breaker test |
| TRE-004 | Recoveries, settlement receipts and approved replenishments shall update funding availability through auditable events and journals. | Must | Funding lifecycle test |
| TRE-005 | Funding cost accrual shall support fixed, variable, tiered or statement-driven arrangements and retain effective terms. | Should | Accrual test |
| TRE-006 | The platform shall prevent one programme or telco from consuming another programme's restricted funding pool. | Must | Isolation test |
| TRE-007 | Treasury dashboards shall display committed, utilised, available, overdue and recovered amounts by source and programme. | Must | Dashboard test |
| TRE-008 | Manual funding adjustments shall require maker-checker approval and balanced financial treatment. | Must | Adjustment workflow |
| TRE-009 | Funding-pool status shall be a real-time overlay in decisioning. | Must | Decision overlay test |
| TRE-010 | The system shall forecast expected recoveries and liquidity utilisation using versioned assumptions without changing ledger truth. | Should | Forecast reconciliation |

### 19.1 Automatic Portfolio Guardrails

| Guardrail | Illustrative metric | Automatic action |
| --- | --- | --- |
| Approval-rate anomaly | Approval rate deviates by configured percentage from baseline | Suspend affected programme/cohort and alert Risk |
| Limit-distribution anomaly | Average or upper-percentile limit rises unexpectedly | Freeze config version; stop new offers |
| Early delinquency spike | First-payment/recharge recovery deteriorates | Reduce tiers or suspend originations |
| Funding exhaustion | Available capacity below reserve threshold | Stop originations; continue recovery |
| Fulfilment ambiguity | FULFILMENT\_UNKNOWN rate exceeds threshold | Open telco circuit breaker |
| Reconciliation backlog | Material breaks exceed age/value threshold | Restrict originations or settlement |

## 20\. Credit Bureau, Regulatory Evidence and Complaints Interfaces

**Bureau and regulatory technical requirements**

| ID | Requirement | Priority | Acceptance evidence |
| --- | --- | --- | --- |
| BUR-001 | Release 1 shall include a configurable bureau-export capability even where live submission is initially disabled. | Must | Dormant pipeline demonstration |
| BUR-002 | Bureau mappings shall support multiple bureaux through adapters and a canonical credit-reporting record. | Must | Adapter test |
| BUR-003 | Every submitted bureau record shall retain source ledger cutoff, mapping version, submission batch, acknowledgement and correction history. | Must | Trace test |
| BUR-004 | Rejected bureau records shall enter an exception workflow and shall not be silently omitted. | Must | Rejection test |
| BUR-005 | Corrections and disputes shall create versioned replacement/correction records linked to the original submission. | Must | Correction test |
| BUR-006 | Bureau export shall apply programme/legal-entity enablement, consent/lawful-basis and minimum-reporting rules. | Must | Policy test |
| REG-001 | The platform shall retain disclosure content, rendered values, language, channel, customer action, timestamp and cryptographic content hash per advance. | Must | Evidence retrieval |
| REG-002 | Complaint cases shall support intake, categorisation, SLA clocks, ownership, correspondence, redress, root cause and regulatory export. | Must | Case workflow test |
| REG-003 | Customer data-subject requests shall be tracked with identity verification, scope, deadlines, decisions and evidence. | Must | DSR workflow test |

| REG-004 | Regulatory exports shall be generated from versioned, reproducible queries and retained with control totals and approval. | Must | Export evidence |  
| REG-005 | The platform shall support legal holds that suspend deletion for affected records without changing ordinary retention policy. | Must | Legal hold test |  
| REG-006 | Regulatory configuration shall be scoped by jurisdiction and programme, but mandatory Nigeria deployment constraints cannot be left unset. | Must | Configuration validation |

### 20.1 Canonical Bureau Record

```json
{
    "reporting_entity_id": "LENDER_NG_001",
    "bureau_subject_reference": "hashed-or-approved-reference",
    "account_reference": "ADV-01J...",
    "product_code": "AIRTIME_ADVANCE",
    "opened_at": "2026-07-16T18:20:00Z",
    "original_amount_minor": 10000,
    "outstanding_amount_minor": 5000,
    "currency": "NGN",
    "status": "ACTIVE",
    "days_past_due": 12,
    "as_of_date": "2026-07-31",
    "mapping_version": "bureau-map-4"
}
```

## 21\. Portal and Front-End Technical Architecture

### 21.1 Portal Segmentation

| Portal / workspace | Primary users | Core capabilities |
| --- | --- | --- |
| Administration | Platform administrators and authorised tenant admins | Telcos, products, configuration, users, feature flags, integration settings |
| Risk | Credit risk, fraud and portfolio teams | Policies, models, limits, guardrails, cohorts, suspensions, decision explanations |
| Operations | NOC/business operations | Advance search, fulfilment exceptions, queue health, manual resolution |
| Finance | Finance control, reconciliation and settlement | Journals, breaks, statements, funding pools, period close |
| Compliance | Compliance, complaints, privacy | Disclosures, complaints, bureau, DSR, regulatory exports |
| Customer Support | Authorised support agents | Masked subscriber timeline, balance, advance status, evidence and cases |
| Technical Operations | SRE/integration teams | Adapter health, rate limits, certificates, replay and simulator tools |

**Portal requirements**

| ID | Requirement | Priority | Acceptance evidence |
| --- | --- | --- | --- |
| UI-001 | The front end shall use tenant-aware backend-for-frontend or APIs that enforce server-side authorisation; UI hiding alone is insufficient. | Must | Authorisation test |

| UI-002 | All high-risk actions shall show the affected telco, programme, environment and effective time before confirmation. | Must | UX test |  
| UI-003 | Maker-checker workflows shall prevent the same identity from creating and approving a controlled change. | Must | Workflow test |  
| UI-004 | Search results shall mask MSISDN and sensitive fields by default, with audited step-up reveal where authorised. | Must | Privacy test |  
| UI-005 | Tables shall support server-side pagination, export controls and bounded filters for very large datasets. | Must | Performance test |  
| UI-006 | Exports shall be asynchronous, access-controlled, watermarked/labelled, time-limited and audited. | Must | Export test |  
| UI-007 | Every material entity shall expose a chronological audit timeline with source references and state changes. | Must | Timeline test |  
| UI-008 | Decision explanations shall show inputs, reason codes, constraints and versions without exposing proprietary or unsafe detail to unauthorised roles. | Must | Role test |  
| UI-009 | Manual repair actions shall display predicted financial and state impact before submission. | Must | Repair simulation test |  
| UI-010 | Dashboards shall indicate data freshness and cutoff times. | Must | Freshness test |  
| UI-011 | The interface shall meet applicable accessibility requirements for keyboard access, contrast, labels and screen-reader structure. | Must | Accessibility audit |  
| UI-012 | Session timeout, reauthentication and step-up authentication shall depend on role and action sensitivity. | Must | Security test |  
| UI-013 | The portal shall not cache sensitive responses in shared browsers or intermediary proxies. | Must | Header and browser test |  
| UI-014 | Configuration forms shall use typed controls, range validation, dependency warnings and simulation links rather than unrestricted JSON for routine users. | Must | Usability test |  
| UI-015 | Bulk actions shall show selection scope, item count, impact and partial-failure results. | Must | Bulk operation test |

## 22\. Security, Privacy and Trust Architecture

### 22.1 Identity and Access

Security requirements

ID

| ID | Requirement | Priority | Acceptance evidence |
| --- | --- | --- | --- |
| SEC-001 | All external and privileged interfaces shall use strong authenticated identities; anonymous access is limited to explicitly approved public content. | Must | Penetration test |
| SEC-002 | Service-to-service authentication shall use short-lived workload identities or mutually authenticated certificates rather than shared static credentials. | Must | Identity review |
| SEC-003 | Human access shall use central identity, MFA and role plus attribute-based telco/programme scope. | Must | Access test |
| SEC-004 | Privileged access shall be time-bound, approved and recorded; standing production administrator access shall be minimised. | Must | PAM audit |
| SEC-005 | Secrets shall be stored in an approved secret manager, rotated and never logged or embedded in source/config files. | Must | Secret scan and rotation test |
| SEC-006 | Data shall be encrypted in transit and at rest using organisation-approved cryptography and key management. | Must | Configuration audit |
| SEC-007 | Key use shall be separated by environment and support telco/legal-entity separation where required. | Must | KMS inventory |
| SEC-008 | Application and database logs shall exclude raw NIN, authentication secrets, full MSISDN and unnecessary customer content. | Must | Log scan |
| SEC-009 | PII reveal, export and manual adjustment actions shall generate immutable audit events. | Must | Audit test |
| SEC-010 | All input shall be validated and output encoded; APIs and portals shall implement protection against common web/API attack classes. | Must | SAST/DAST/pen test |
| SEC-011 | Rate limiting and abuse controls shall apply by client, telco, subscriber token, session and operation as appropriate. | Must | Abuse test |
| SEC-012 | Financial commands shall include replay protection and bounded timestamp/nonces where signatures are used. | Must | Replay test |
| SEC-013 | Software dependencies and container images shall be scanned, signed and promoted through controlled registries. | Must | Supply-chain evidence |
| SEC-014 | Production data shall not be copied to non-production environments without approved masking or synthetic replacement. | Must | Data environment audit |
| SEC-015 | Security events shall feed central detection with telco context, severity and response playbooks. | Must | SIEM test |
| SEC-016 | Tenant isolation shall be tested through automated negative tests in CI and periodic independent assessment. | Must | Isolation report |

| SEC-017 | Administrative APIs shall support step-up authentication for high-impact actions such as rearming guardrails, manual journals and tenant-wide changes. | Must | Step-up test |  
| SEC-018 | The platform shall support secure deletion of keys and derived data while retaining legally required financial records. | Must | Crypto-shredding/retention test |  
| SEC-019 | Threat modelling shall be performed for each major release and material telco integration. | Must | Threat model review |  
| SEC-020 | A vulnerability shall not be accepted solely because an internal network boundary exists. | Must | Security governance review |

### 22.2 Security Zones

| Zone | Examples | Key controls |
| --- | --- | --- |
| Public/Partner Edge | API gateway, USSD ingress, telco callbacks | WAF, mTLS/OAuth, rate limits, DDoS protection |
| Application | Domain services and workers | Workload identity, network policy, least privilege |
| Financial Core | Ledger, settlement, treasury | Stricter access, immutable audit, controlled administration |
| Data Platform | Raw feeds, features, analytics | Classification, encryption, purpose-based access |
| Management | CI/CD, observability, secrets, admin | MFA, PAM, isolated control plane |

## 23\. Infrastructure and Deployment Architecture

![image](https://static-us-img.skywork.ai/prod/nexus/1784236746/cropped_image_5_1784236746361870758.jpg)

**Figure 7 - Multi-zone primary deployment with recovery-region replication.**

### 23.1 Platform Runtime

The reference implementation assumes containerised workloads orchestrated across multiple availability zones, managed relational databases for transactional and ledger stores, a durable event-stream platform, distributed cache, object storage and central observability. Equivalent technologies are acceptable if they meet the requirements and ADR process.

## Infrastructure requirements

ID

| ID | Requirement | Priority | Acceptance evidence |
| --- | --- | --- | --- |
| INF-001 | Production services shall run across at least two failure zones with automated rescheduling and health-based traffic removal. | Must | Zone failure test |
| INF-002 | Stateful components shall use supported high-availability configurations and documented failover procedures. | Must | Failover test |
| INF-003 | Infrastructure shall be defined as code, peer-reviewed and promoted through controlled environments. | Must | IaC audit |
| INF-004 | Environment configuration and secrets shall be externalised from application images. | Must | Image inspection |
| INF-005 | Autoscaling shall use service-appropriate metrics and bounded limits to prevent runaway cost or dependency overload. | Must | Load test |
| INF-006 | Resource quotas and priority classes shall protect financial recovery and ledger workloads from non-critical workloads. | Must | Resource starvation test |
| INF-007 | Database connections shall be pooled and capped per service and tenant-sensitive workload. | Must | Connection exhaustion test |
| INF-008 | Maintenance and deployment shall preserve at least the minimum healthy capacity required by SLOs. | Must | Rolling deployment test |
| INF-009 | Recovery-region data replication shall meet the defined RPO by data class; ledger data requires near-zero loss objective. | Must | DR evidence |
| INF-010 | Production backups shall be isolated from ordinary application credentials and protected against destructive compromise. | Must | Backup security test |
| INF-011 | Network egress shall be restricted to approved destinations and observable by service. | Must | Network policy test |
| INF-012 | Telco private connectivity, VPN or dedicated links shall terminate in controlled integration zones with redundant paths where contracted. | Must | Connectivity failover test |
| INF-013 | Capacity reservations or pre-scaled headroom shall exist for predictable recharge and campaign peaks. | Should | Peak rehearsal |
| INF-014 | Platform components shall expose version and build provenance to operations without revealing sensitive internals publicly. | Must | Release inspection |
| INF-015 | Infrastructure cost shall be attributable by telco/programme using tags, metrics or allocation models. | Must | Cost dashboard |

## 24\. Scalability, Performance and Capacity Engineering

## Scalability and performance requirements

ID

| ID | Requirement | Priority | Acceptance evidence |
| --- | --- | --- | --- |
| SCL-001 | The data platform shall support at least 100 million active subscriber profiles and configurable historical feature windows. | Must | Scale test or validated benchmark |
| SCL-002 | Offer enquiry p95 service time, excluding telco channel transit, shall target 300 ms and p99 750 ms under agreed load. | Must | Performance test |
| SCL-003 | Advance validation and exposure reservation p95 shall target 500 ms excluding telco fulfilment latency. | Must | Performance test |
| SCL-004 | The platform shall support horizontal scaling of channel, decision, adapter, recovery and event-consumer workloads independently. | Must | Scaling demonstration |
| SCL-005 | Daily or scheduled feature computation shall complete within the agreed scoring window with restartable partitions and no all-or-nothing rerun. | Must | Batch scale test |
| SCL-006 | Hot subscriber or telco partitions shall not create unbounded skew; partition strategy shall be load-tested. | Must | Skew test |
| SCL-007 | Caches shall use bounded TTL, quotas and stampede protection; cache loss shall degrade performance but not correctness. | Must | Cache failure test |
| SCL-008 | Large exports, reports and reconciliation jobs shall not execute on synchronous portal request threads. | Must | Load isolation test |
| SCL-009 | Rate limits shall protect downstream telco APIs and shall support fair allocation among programmes. | Must | Throttling test |
| SCL-010 | Capacity models shall include USSD peaks, bulk recharge events, scoring windows, statement cycles and incident replay. | Must | Capacity plan |
| SCL-011 | Performance tests shall include duplicate events, retries and degraded dependencies, not only clean steady-state traffic. | Must | Resilience performance test |
| SCL-012 | Cost-per-offer, cost-per-advance, cost-per-active-subscriber and cost-per-recovery shall be measured by programme. | Must | Unit economics dashboard |

### 24.1 Initial SLO Targets

| Capability | Release 1 target | Scale-phase target |
| --- | --- | --- |
| Core decision/offer API availability | 99.9% monthly | 99.99% where commercially justified |
| Recovery event durability | No acknowledged event loss | No acknowledged event loss |
| Ledger RPO | Near zero | Near zero |
| Core platform RTO | 30 minutes | Single-digit minutes for critical services |
| Offer enquiry p95 | (\\leq) 300 ms platform time | (\\leq) 200 ms |
| Advance validation p95 | (\\leq) 500 ms excluding telco | (\\leq) 300 ms excluding telco |

| Batch feature completion | Within agreed overnight window | Continuous/near-real-time for selected features |

## 25\. Resilience, Failure Handling and Degraded Modes

**Resilience requirements**

| ID | Requirement | Priority | Acceptance evidence |
| --- | --- | --- | --- |
| RES-001 | Every dependency shall have explicit timeout, retry, circuit-breaker and fallback policy. | Must | Chaos test |
| RES-002 | The system shall fail closed for new credit when identity, exposure, funding or decision integrity cannot be established. | Must | Dependency failure test |
| RES-003 | Recovery ingestion and ledger posting shall remain prioritised when offer generation is disabled or degraded. | Must | Degraded-mode test |
| RES-004 | A telco fulfilment outage shall open only the affected telco/programme circuit and shall not stop other telcos. | Must | Isolation test |
| RES-005 | Unknown fulfilments shall be durable across restarts and automatically re-enquired or reconciled. | Must | Restart test |
| RES-006 | Worker processing shall use leases or visibility timeouts that permit safe reassignment after failure. | Must | Worker crash test |
| RES-007 | Jobs shall checkpoint and resume without duplicating financial effects. | Must | Batch restart test |
| RES-008 | DR failover shall preserve idempotency stores, event offsets and configuration versions needed to prevent duplicate processing. | Must | DR exercise |
| RES-009 | Clock skew shall be monitored; business expiry shall use trusted server time and preserve source timestamps. | Must | Clock-skew test |
| RES-010 | Runbooks shall define safe-mode behaviour for database, event bus, cache, telco link, bureau, notification and observability failures. | Must | Runbook review |
| RES-011 | Load shedding shall reject low-priority work explicitly rather than allow uncontrolled timeouts and resource exhaustion. | Must | Overload test |
| RES-012 | The platform shall support controlled pausing and draining of individual consumers before deployment or incident repair. | Must | Operational test |

### 25.1 Degraded-Mode Matrix

| Failure | Customer origination | Recovery/ledger | Required behaviour |
| --- | --- | --- | --- |
| Decision store unavailable | Stop or use explicitly approved short-lived safe cache | Continue | Alert and fail closed for new offers |

| Telco fulfilment API unavailable | Pause affected telco | Continue | Open circuit; no blind retry |  
| Notification provider unavailable | Continue where disclosure evidence is complete | Continue | Queue/Fallback; do not reverse financial event |  
| Event analytics consumer down | Continue | Continue | Backlog and catch up |  
| Ledger unavailable | Stop financially material commands | Queue only if atomic correctness preserved | No unjournalled active advance |  
| Reconciliation delayed | May continue below risk threshold | Continue | Escalate and suspend if material threshold breached |  
| Funding state unavailable | Stop new credit | Continue | Fail closed |

## 26\. Observability, Audit and SRE Telemetry

**Observability requirements**

| ID | Requirement | Priority | Acceptance evidence |
| --- | --- | --- | --- |
| OBS-001 | Every request shall produce structured logs, metrics and traces linked by correlation ID without exposing prohibited PII. | Must | Trace inspection |
| OBS-002 | Technical telemetry shall be complemented by business metrics for offers, approvals, fulfilments, unknowns, recoveries, exposure and breaks. | Must | Dashboard review |
| OBS-003 | Metrics shall be segmented by telco, programme, product, channel and environment where cardinality remains controlled. | Must | Metrics audit |
| OBS-004 | Alerts shall be symptom- and business-impact-oriented, deduplicated and tied to owned runbooks. | Must | Alert review |
| OBS-005 | Audit events shall record actor, action, target, before/after or version references, reason, source IP/device context and time. | Must | Audit test |
| OBS-006 | Audit storage shall be tamper-evident and access-separated from ordinary application administration. | Must | Control assessment |
| OBS-007 | Financial reconciliation and ledger control failures shall have higher severity than analytics freshness failures. | Must | Alert priority test |
| OBS-008 | SLOs shall be calculated from defined service-level indicators and error budgets. | Must | SLO report |
| OBS-009 | The platform shall monitor FULFILMENT\_UNKNOWN age and value, not only count. | Must | Dashboard test |
| OBS-010 | Batch pipelines shall expose input count, rejected count, output count, control totals, partition progress and estimated completion. | Must | Pipeline dashboard |
| OBS-011 | Cost telemetry shall attribute compute, messaging, storage, SMS and USSD cost where data is available. | Must | Cost dashboard |

| OBS-012 | Operational data retention shall balance investigation needs, privacy and cost; long-term evidence belongs in governed archives. | Must | Retention review |

## 27\. Telco Simulator, Sandbox and Certification Harness

![image](https://static-us-img.skywork.ai/prod/nexus/1784236747/cropped_image_3_1784236747586335729.jpg)

**Figure 8 - Simulator, fault injection and certification evidence flow.**

**Simulator requirements**

| ID | Requirement | Priority | Acceptance evidence |
| --- | --- | --- | --- |
| SIM-001 | A standing simulator shall implement the canonical telco APIs, events and batch interfaces before live telco connectivity is available. | Must | End-to-end demo |
| SIM-002 | The simulator shall support configurable success, decline, pending, timeout, malformed response, throttling and unavailability behaviours. | Must | Fault catalogue test |
| SIM-003 | It shall reproduce timeout-after-success and delayed callback scenarios. | Must | Saga certification |
| SIM-004 | It shall emit duplicate, missing, out-of-order and reversal-before-original events. | Must | Event certification |
| SIM-005 | It shall generate batch files with valid manifests and inject checksum, count, schema and truncation faults. | Must | File certification |
| SIM-006 | It shall support deterministic seeded scenarios so failures are reproducible. | Must | Repeatability test |
| SIM-007 | The harness shall validate requests and responses against canonical and telco-specific schemas. | Must | Schema test |
| SIM-008 | Certification results shall produce a signed evidence pack with test IDs, payloads, outcomes and timestamps. | Must | Evidence-pack generation |
| SIM-009 | The simulator shall support realistic latency distributions and load generation. | Must | Performance test |
| SIM-010 | Telco adapter certification shall include all applicable Volume 1 edge cases before production approval. | Must | Coverage matrix |
| SIM-011 | Simulator environments shall contain only synthetic subscriber data. | Must | Data audit |

| SIM-012 | The same contract tests shall run against simulator and telco sandbox endpoints where possible. | Must | Contract test report |

## 28\. Testing, Verification and Quality Engineering

### 28.1 Test Pyramid and Evidence

## Testing requirements

| ID | Requirement | Priority | Acceptance evidence |
| --- | --- | --- | --- |
| TST-001 | Every numbered requirement shall map to one or more automated tests, controlled manual tests or review evidence. | Must | Traceability matrix |
| TST-002 | Domain state machines and ledger rules shall have exhaustive transition and invariant tests. | Must | Test coverage report |
| TST-003 | Property-based tests shall cover ledger balance, idempotency, recovery caps and allocation invariants. | Must | Property test report |
| TST-004 | Contract tests shall validate provider and consumer compatibility for APIs and events. | Must | CI test report |
| TST-005 | Tenant-isolation tests shall attempt cross-tenant reads, writes, cache access, event consumption and export. | Must | Isolation test report |
| TST-006 | Concurrency tests shall cover duplicate confirmations, simultaneous advances, recovery during fulfilment and configuration activation. | Must | Concurrency test report |
| TST-007 | Chaos tests shall cover dependency timeout, partial outage, worker death, message duplication and database failover. | Must | Chaos evidence |
| TST-008 | Performance tests shall use realistic data volumes, partition skew and telco latency. | Must | Performance report |
| TST-009 | Security testing shall include SAST, dependency scanning, secrets scanning, DAST, API testing and penetration testing. | Must | Security evidence |
| TST-010 | Data pipeline tests shall reconcile input/output counts and known expected features over golden datasets. | Must | Golden data report |
| TST-011 | Model tests shall cover drift, fairness/segment outcomes, explainability, missingness, leakage and rollback. | Must | Model validation pack |
| TST-012 | Reconciliation tests shall include late, duplicate, missing, contradictory and corrected source records. | Must | Recon test pack |
| TST-013 | USSD tests shall cover session expiry at every step, repeated input and post-confirmation session loss. | Must | USSD test pack |

| TST-014 | DR tests shall demonstrate recovery of configuration, idempotency, event positions and ledger integrity. | Must | DR exercise report |  
| TST-015 | Accessibility tests shall cover the administrative and customer-facing web/app interfaces. | Must | Accessibility report |  
| TST-016 | Production deployment shall be blocked when release-gating tests or evidence are missing. | Must | Pipeline policy test |

### 28.2 Minimum Edge-Case Test Families

| Family | Required examples |
| --- | --- |
| Identity | Porting, recycled MSISDN, unknown NIN flag, SIM swap, barred subscriber |
| Offer | Expired offer, price changed, stale decision, repeated presentation |
| Advance | Concurrent confirm, duplicate key, crash after reserve, late telco success |
| Fulfilment | Timeout-after-success, duplicate callback, partial value, reversal |
| Recovery | Duplicate recharge, over-recovery, reversal-before-original, recovery after write-off |
| Configuration | Overlapping versions, unsafe value, mass approval anomaly, rollback |
| Tenant | Wrong credential/payload tenant, cache key collision, topic leakage |
| Financial | Unbalanced template, closed period, duplicate journal, settlement variance |
| Operations | Backlog, circuit breaker, replay, DR failover, clock skew |

## 29\. DevSecOps, Release and Environment Management

**DevSecOps requirements**

| ID | Requirement | Priority | Acceptance evidence |
| --- | --- | --- | --- |
| DEV-001 | Source code, infrastructure, schemas and configuration definitions shall be version controlled with protected branches and review. | Must | Repository settings audit |
| DEV-002 | Builds shall be reproducible and produce signed artefacts with source revision, dependency and provenance metadata. | Must | Build provenance |
| DEV-003 | CI shall run linting, unit, contract, architecture, security and schema compatibility checks. | Must | Pipeline report |
| DEV-004 | Deployment shall use automated pipelines with environment approvals and no manual modification of production nodes. | Must | Deployment audit |
| DEV-005 | Database changes shall use expand-migrate-contract patterns or equivalent rolling-compatible techniques. | Must | Migration rehearsal |
| DEV-006 | Feature flags shall separate deployment from release and include owner, expiry/review date and rollback plan. | Must | Flag inventory |

| DEV-007 | Production configuration shall be promoted from reviewed definitions or created through governed portal workflows, not edited directly in databases. | Must | Change audit |  
| DEV-008 | Non-production environments shall use synthetic/masked data and independent credentials. | Must | Environment audit |  
| DEV-009 | Canary or progressive delivery shall be available by telco, programme, cohort or traffic percentage for material changes. | Should | Canary demonstration |  
| DEV-010 | Rollback shall consider code, schema, configuration, event compatibility and in-flight sagas. | Must | Rollback exercise |  
| DEV-011 | Release notes shall list requirements delivered, migrations, configuration dependencies, known risks and operational actions. | Must | Release pack |  
| DEV-012 | Emergency changes shall follow an expedited but auditable approval and retrospective process. | Must | Emergency change audit |  
| DEV-013 | End-of-life dependencies and runtimes shall be identified and remediated through a supported-technology lifecycle. | Must | Technology lifecycle report |

## 30\. Migration, Dual Run and Cutover Technical Design

**Migration requirements**

| ID | Requirement | Priority | Acceptance evidence |
| --- | --- | --- | --- |
| MIG-001 | Incumbent migration shall define authoritative source fields, mapping, transformation, reconciliation and acceptance for subscriber eligibility, limits, active advances, recoveries and financial balances. | Must | Migration design |
| MIG-002 | Historical data shall be classified into required operational history, regulatory evidence, analytical history and archive-only data. | Must | Data migration scope |
| MIG-003 | Every migration load shall have manifests, checksums, record counts, value totals, rejects and rerun identity. | Must | Load evidence |
| MIG-004 | Active advances shall be migrated with exact outstanding components, dates, telco references and reconciliation status. | Must | Balance reconciliation |
| MIG-005 | The platform shall support shadow scoring against incumbent outputs before decision cutover. | Must | Shadow comparison report |
| MIG-006 | Dual run shall define which system is authoritative for offers, fulfilment, recovery and ledger at each stage to prevent double lending or collection. | Must | Cutover RACI |

| MIG-007 | A subscriber shall not be active for origination in both incumbent and new platform unless a controlled split-cohort design proves mutual exclusion. | Must | Cohort control test |  
| MIG-008 | Cutover shall include a freeze/cutoff, final delta, control totals, smoke tests and rollback criteria. | Must | Cutover rehearsal |  
| MIG-009 | In-flight unknown fulfilments and unmatched recoveries shall have explicit ownership and migration treatment. | Must | Exception migration test |  
| MIG-010 | Post-cutover hypercare shall compare telco, platform, ledger and settlement totals at increased frequency. | Must | Hypercare dashboard |  
| MIG-011 | Rollback shall not lose advances or recoveries created after cutover and shall be financially reconciled. | Must | Rollback rehearsal |  
| MIG-012 | Migration data and tooling shall be access-controlled, time-limited and retired after approved retention. | Must | Security audit |

## 31\. Non-Functional Requirement Catalogue

| NFR domain | Binding expectation |
| --- | --- |
| Availability | 99.9% Release 1 for core decisioning; higher targets require corresponding automated failover design |
| Durability | No acknowledged loss of recovery or ledger events; near-zero ledger RPO |
| Performance | Offer p95 \<=300ms platform time; advance validation p95 \<=500ms excluding telco |
| Scalability | 100M+ subscriber profiles and independently scalable high-volume workloads |
| Security | Strong identity, tenant isolation, encryption, least privilege, secure SDLC and tamper-evident audit |
| Privacy | Data minimisation, purpose limitation, masking, retention, DSR and legal hold |
| Maintainability | Clear ownership, automated tests, ADRs, observability and backward-compatible evolution |
| Portability | Canonical domain contracts independent of telco and replaceable infrastructure products |
| Auditability | End-to-end correlation, immutable evidence, decision replay and financial traceability |
| Cost efficiency | Per-programme cost attribution and measurable cost per decision/advance/recovery |
| Accessibility | Applicable accessible design for portals and customer web/app surfaces |

**Cross-cutting NFR requirements**

| ID | Requirement | Priority | Acceptance evidence |
| --- | --- | --- | --- |
| NFR-001 | All SLOs shall define measurement point, exclusions, window and data source. | Must | SLO specification |

| NFR-002 | A higher availability commitment shall not be approved without costed architecture and tested failover consistent with the target. | Must | Architecture approval |  
| NFR-003 | Data loss tolerance shall be defined per store; ledger and acknowledged recovery events have the strictest class. | Must | Data-class matrix |  
| NFR-004 | The design shall prefer correctness over availability for new lending when exposure or financial integrity is uncertain. | Must | Failure-mode test |  
| NFR-005 | The design shall prefer continued durable ingestion over real-time presentation for recovery events during partial outages. | Must | Degraded-mode test |  
| NFR-006 | All critical services shall have capacity, resilience, security and support ownership before production. | Must | Production readiness review |

## 32\. Requirement Traceability and Build Work Packages

### 32.1 Recommended Engineering Workstreams

| Workstream | Primary sections | Indicative deliverables |
| --- | --- | --- |
| Platform Foundations | 3, 4, 6, 7, 22, 23, 29 | Runtime, identity, gateway, event bus, CI/CD, observability |
| Telco Integration | 5, 14, 15, 27 | Adapter SDK, USSD, notification, simulator, certification |
| Credit Decisioning | 9-12 | Subscriber, feature store, scoring, configuration, offers |
| Advance & Recovery | 13, 16 | Saga, exposure, fulfilment, recharge/recovery, collections |
| Financial Core | 17-19 | Ledger, reconciliation, settlement, treasury |
| Compliance & Customer | 20-21 | Bureau, evidence, complaints, privacy, portals |
| Data Platform | 8, 11, 24 | Ingestion, features, analytical stores, capacity |
| Quality & Migration | 25, 26, 28, 30, 31 | Resilience, SRE, test packs, migration, NFR evidence |

### 32.2 Build Definition of Done

-   Code, schema and interface contract reviewed and merged through controlled workflow.
    
-   Applicable numbered requirements mapped to passing automated or controlled manual evidence.
    
-   Tenant isolation, idempotency, state transition and financial invariants tested.
    
-   Operational telemetry, alerting and runbook created.
    
-   Security scans passed or approved risk treatment recorded.
    
-   Backward compatibility and migration impact assessed.
    
-   Configuration and feature flags documented with safe defaults.
    
-   Performance impact measured under representative volume.
    
-   Audit and evidence records demonstrably retrievable.
    

## Appendix A - Canonical API and Event Conventions

### A.1 Canonical Headers

| Header | Purpose | Required |
| --- | --- | --- |
| Authorization / mTLS identity | Authenticates calling client or workload | Yes |
| X-Correlation-ID | End-to-end trace identity | Yes |
| X-Request-ID | Unique request instance | Yes |
| Idempotency-Key | Deduplicates material commands | For material commands |
| X-Telco-ID | Payload context; verified against authenticated scope | For telco-scoped calls |
| X-Schema-Version | Explicit file/event schema version where transport requires | As applicable |

### A.2 Canonical Event Envelope

```json
{
"event_id": "01J...",
"event_type": "RecoveryApplied",
"schema_version": 2,
"occurred_at": "2026-07-16T18:30:04.123Z",
"received_at": "2026-07-16T18:30:04.600Z",
"telco_id": "MTN_NG",
"programme_id": "AIRTIME_NG_01",
"aggregate_type": "Advance",
"aggregate_id": "ADV-01J...",
"sequence": 17,
"correlation_id": "COR-...",
"causation_id": "EVT-...",
"payload": {}
}
```

## Appendix B - Canonical State and Invariant Register

| Invariant ID | Invariant |
| --- | --- |
| INV-001 | No active advance exists without confirmed telco fulfilment or approved manual evidence resolution. |
| INV-002 | No subscriber/programme exposure exceeds the most restrictive applicable cap. |
| INV-003 | A financial business event posts at most once. |
| INV-004 | Every posted journal balances per currency. |
| INV-005 | Posted journals are never updated or deleted by ordinary operations. |
| INV-006 | A recovery cannot reduce total outstanding below zero. |
| INV-007 | One telco credential cannot access another telco's data or commands. |
| INV-008 | A configuration decision references one immutable effective version set. |
| INV-009 | A FULFILMENT\_UNKNOWN record cannot be blindly resubmitted. |
| INV-010 | A recycled MSISDN does not inherit a prior subscriber account without approved identity linkage. |
| INV-011 | A closed period cannot be silently rewritten. |
| INV-012 | A manual repair retains actor, approval, reason, evidence and financial impact. |

## Appendix C - Core Relational Schema Outline

| Table / aggregate | Selected fields | Key constraints |
| --- | --- | --- |
| telcos | telco\_id, code, name, status | code unique |
| programmes | programme\_id, telco\_id, legal\_entity\_id, status | tenant-scoped unique code |
| subscriber\_accounts | subscriber\_account\_id, telco\_id, msisdn\_token, effective\_from/to, status | no overlapping active identity period for same telco/token unless approved |
| feature\_snapshots | feature\_snapshot\_id, subscriber\_account\_id, as\_of, feature\_version, quality | immutable |
| decisions | decision\_id, feature\_snapshot\_id, model/rule/config versions, result | immutable replay record |
| offers | offer\_id, decision\_id, amounts, fee/tax, disclosure\_version, expiry, state | accepted once; immutable price snapshot |
| advances | advance\_id, offer\_id, subscriber\_account\_id, state, face value, outstanding, version | state transition/version constraints |
| fulfilment\_attempts | attempt\_id, advance\_id, idempotency key, telco ref, status, evidence | unique telco/programme idempotency key |
| recovery\_events | recovery\_event\_id, source\_event\_id, subscriber\_account\_id, amount, status | unique source identity per telco |
| recovery\_allocations | allocation\_id, recovery\_event\_id, advance\_id, component, amount | sum \<= event and outstanding |
| journals | journal\_id, business\_event\_key, event\_type, period, status | unique business event |
| journal\_entries | entry\_id, journal\_id, account, debit, credit, dimensions | journal total balance |
| reconciliation\_items | item\_id, run\_id, sources, value, status, owner | versioned resolution history |
| configuration\_versions | version\_id, domain, scope, state, effective dates, content hash | immutable when approved |

## Appendix D - Error, Retry and Ambiguity Matrix

| Condition | Immediate state | Retry policy | Resolution |
| --- | --- | --- | --- |
| Connection failed before request write | No fulfilment submitted | Bounded retry permitted if proven not sent | Adapter telemetry |
| Timeout after request may have been sent | FULFILMENT\_UNKNOWN | No blind retry | Status enquiry then reconciliation |
| Telco explicit technical rejection | FULFILMENT\_FAILED or retryable pending | Retry only for allowlisted transient code | Release reservation if final |
| Duplicate callback | No state duplication | Acknowledge original result | Idempotent consumer |
| Late success after unknown | ACTIVE | No new request | Activate same advance and journal |
| Recovery duplicate | No second allocation | Acknowledge duplicate | Retain dedupe evidence |
| Reversal before recovery | Pending unmatched reversal | Do not post negative recovery | Match on original arrival or exception |
| Ledger unavailable | No financially complete transition | Do not claim success | Retry/repair under invariant |

## Appendix E - Minimum Production Readiness Evidence

| Evidence pack | Minimum contents |
| --- | --- |
| Architecture | Approved logical/deployment diagrams, ADRs, ownership and interface catalogue |
| Functional | Requirement traceability, state/invariant tests, channel journeys |

| Financial | Ledger balance tests, reconciliation controls, settlement samples, period-close controls |  
| Integration | Adapter certification, telco sandbox results, security certificates and rate limits |  
| Security | Threat model, SAST/DAST/dependency/pen test, access model, key/secret controls |  
| Performance | Representative load, batch window, skew, failover and cost results |  
| Resilience | Chaos tests, DR exercise, degraded-mode and runbook evidence |  
| Data | Data catalogue, quality controls, retention, backup restore and migration reconciliation |  
| Regulatory | Disclosure evidence, complaints, bureau pipeline, privacy workflows and exports |  
| Operations | Dashboards, alerts, on-call ownership, capacity and release rollback |

## Appendix F - Requirement Inventory

This volume contains 396 numbered technical requirements. The table below provides a compact inventory for import into a requirements-management or test-management tool.

| ID | Priority | Requirement |
| --- | --- | --- |
| TAR-001 | Must | The platform shall implement the system-of-record boundaries established in Volume 1 and shall not make the telco subscriber balance its authoritative loan ledger. |
| TAR-002 | Must | The platform shall support multiple telcos without embedding operator-specific fields, codes or workflows in core credit-domain services. |
| TAR-003 | Must | Every financially material command and event shall be idempotent, traceable and recoverable after process or network failure. |
| TAR-004 | Must | The synchronous customer path shall read precomputed decision data and shall not execute unbounded historical aggregation. |
| TAR-005 | Must | No component shall report an advance as successful until telco fulfilment is confirmed or subsequently resolved by status enquiry or reconciliation. |
| TAR-006 | Must | All configurable decisions shall retain the exact effective configuration, model, rule and feature versions used. |
| ARC-001 | Must | Every deployable component shall have a named business capability, owning team, data ownership statement, interface contract and service-level objective. |
| ARC-002 | Must | Each material design choice shall be recorded in an Architecture Decision Record with options, rationale, risks and rollback implications. |
| ARC-003 | Must | Services shall communicate only through documented synchronous APIs, asynchronous events or approved batch interfaces; direct cross-service database reads are prohibited. |
| ARC-004 | Must | Core domain services shall not depend on telco-specific SDKs; such dependencies shall be isolated within adapter boundaries. |
| ARC-005 | Must | The platform shall maintain a machine-readable interface catalogue covering API versions, event schemas, batch layouts and owning services. |
| ARC-006 | Must | Breaking changes shall require a version transition plan with parallel support, consumer impact analysis and retirement date. |
| ARC-007 | Must | Technical debt that weakens a financial, security or tenant-isolation invariant shall not be accepted through ordinary backlog prioritisation. |
| SRV-001 | Must | A single service or module shall own each mutable aggregate and its state transitions. |

| SRV-002 | Must | Ledger journals shall be owned exclusively by the ledger service and shall not be directly inserted or updated by other services. |  
| SRV-003 | Must | The advance orchestrator shall coordinate the business saga but shall not implement telco-specific transport logic. |  
| SRV-004 | Must | The configuration service shall publish immutable effective versions; consumers shall not read mutable draft configuration. |  
| SRV-005 | Should | The decisioning service shall be stateless for synchronous evaluation except for access to approved feature, rule, model and exposure snapshots. |  
| SRV-006 | Should | Read-heavy portals shall use purpose-built read models or query services rather than loading transactional aggregates repeatedly. |  
| SRV-007 | Must | Long-running work shall execute asynchronously with resumable job state, checkpoints and deterministic retry behaviour. |  
| SRV-008 | Must | Every service shall expose health, readiness, dependency and version endpoints appropriate to its runtime. |  
| SRV-009 | Must | Business commands shall return stable business error codes separate from transport status codes. |  
| SRV-010 | Should | Shared libraries shall be limited to technical concerns; domain logic shall not be duplicated through shared packages across services. |  
| TEN-001 | Must | Every tenant-owned record shall include an immutable telco\_id and, where applicable, programme\_id and legal\_entity\_id. |  
| TEN-002 | Must | Inbound credentials shall resolve to an authorised telco context before payload processing. |  
| TEN-003 | Must | A payload telco\_id that conflicts with the authenticated context shall be rejected and security-alerted. |  
| TEN-004 | Must | The canonical subscriber key shall not be MSISDN alone; it shall include telco and an effective identity period or internal subscriber\_account\_id. |  
| TEN-005 | Must | All caches, topics, object paths, search documents and metrics dimensions containing tenant data shall include telco scope. |  
| TEN-006 | Must | Portal authorisation shall apply both functional permissions and an explicit set of permitted telco/programme scopes. |  
| TEN-007 | Must | One telco adapter outage, backlog or circuit-breaker state shall not consume all shared worker, connection or queue capacity. |  
| TEN-008 | Must | Encryption keys and secrets shall be separately identifiable by telco and environment; dedicated keys shall be supported where contractually required. |  
| TEN-009 | Must | Cross-telco analytics shall use approved de-identified or aggregated data products and shall not bypass transactional tenant controls. |  
| TEN-010 | Should | The platform shall support promotion of a telco from shared to dedicated infrastructure without changing canonical business interfaces. |  
| TEN-011 | Must | Tenant deletion or offboarding shall be a governed lifecycle that preserves legally required financial and audit records. |  
| TEL-001 | Must | Each adapter shall implement a published canonical interface and a capability manifest declaring supported functions and versions. |  
| TEL-002 | Must | Adapter mappings shall preserve the original telco request, response, code and timestamp as evidence while emitting normalised platform fields. |  
| TEL-003 | Must | Transport retries shall occur only where the operation is proven idempotent or a status-enquiry mechanism resolves ambiguity. |  
| TEL-004 | Must | Each telco shall have independent connection pools, concurrency limits, rate limits, retry budgets and circuit breakers. |

| TEL-005 | Must | Credentials, certificates and endpoint configurations shall be environment-specific and rotated without code deployment. |  
| TEL-006 | Must | Adapter configuration changes shall use maker-checker approval and pre-activation connectivity tests. |  
| TEL-007 | Must | All inbound telco events shall have a stable event identity or a platform-derived deduplication fingerprint. |  
| TEL-008 | Must | Batch files shall use manifest, checksum, record count, schema version and control-total validation before acceptance. |  
| TEL-009 | Must | Unknown or newly introduced telco codes shall be quarantined rather than silently mapped to success or failure. |  
| TEL-010 | Must | The platform shall provide per-adapter dashboards for latency, success, ambiguity, retries, throttling and backlog. |  
| TEL-011 | Must | Adapters shall support status enquiry by platform request ID and telco transaction reference where the telco offers such capability. |  
| TEL-012 | Must | The adapter framework shall support synchronous APIs, asynchronous events and secure batch/file exchange without changing core domain contracts. |  
| TEL-013 | Must | The platform shall retain evidence needed to prove whether a fulfilment instruction was sent, acknowledged, credited, rejected, reversed or remained unknown. |  
| API-001 | Must | External and inter-service APIs shall be versioned, documented in a machine-readable specification and validated in CI. |  
| API-002 | Must | Commands that can create financial or customer effects shall require an idempotency key scoped to client, tenant and operation. |  
| API-003 | Must | The server shall persist the idempotency outcome before returning a final response and shall return the original result for valid retries. |  
| API-004 | Must | Request IDs, correlation IDs and causation IDs shall be propagated across synchronous and asynchronous boundaries. |  
| API-005 | Must | Monetary values shall be represented as integer minor units plus ISO currency; binary floating-point shall not be used for accounting values. |  
| API-006 | Must | Timestamps shall be UTC, include timezone designators and preserve telco event time separately from receipt and processing time. |  
| API-007 | Must | Sensitive subscriber identifiers shall be tokenised or masked outside authorised service boundaries. |  
| API-008 | Should | Pagination shall use stable cursor-based pagination for high-volume mutable datasets. |  
| API-009 | Must | Filtering and sorting fields shall be explicitly allowed to prevent unbounded query construction. |  
| API-010 | Must | Bulk endpoints shall define maximum items, partial-success semantics, per-item errors and retry guidance. |  
| API-011 | Must | Error responses shall contain stable error\_code, safe message, retryable flag and correlation\_id without stack traces or secrets. |  
| API-012 | Must | API clients shall use bounded timeouts and shall not hold database transactions open across remote calls. |  
| API-013 | Must | Backward-compatible additive changes shall be preferred; field removal or semantic reinterpretation requires a new major version. |  
| API-014 | Must | Webhook receivers shall authenticate signatures or mTLS identities, validate replay windows and deduplicate events. |  
| API-015 | Must | Administrative mutation APIs shall require explicit reason, change ticket or approval reference where policy requires. |

| EVT-001 | Must | Domain events shall describe facts that have committed, use past-tense names and include event\_id, event\_type, schema\_version, telco\_id, aggregate\_id and occurred\_at. |  
| EVT-002 | Must | A transactional outbox or equivalent atomic mechanism shall ensure database state and event publication cannot diverge silently. |  
| EVT-003 | Must | Consumers shall be idempotent and store processing outcomes or deduplication keys for the required replay window. |  
| EVT-004 | Must | Ordering shall be guaranteed only within a documented partition key, normally telco\_id plus aggregate or subscriber account. |  
| EVT-005 | Must | Consumers shall handle out-of-order events using sequence numbers, effective timestamps or pending-match logic where order cannot be guaranteed. |  
| EVT-006 | Must | Schemas shall be governed in a registry and compatibility checked before deployment. |  
| EVT-007 | Must | Retry topics shall use bounded attempts, exponential backoff and reason-specific retry policies. |  
| EVT-008 | Must | Dead-letter records shall retain original payload, headers, failure reason, attempt history and consumer version. |  
| EVT-009 | Must | Replay shall require authorised initiation, bounded scope, dry-run counts and idempotency assurance. |  
| EVT-010 | Must | PII shall be minimised in event payloads; tokens or internal identifiers shall be preferred. |  
| EVT-011 | Must | Event retention shall support reconciliation, incident investigation and legally required evidence without turning the broker into the long-term archive. |  
| EVT-012 | Must | Backpressure controls shall prioritise financial recovery and ledger events above analytics and non-critical notification workloads. |  
| EVT-013 | Must | Event lag and oldest-unprocessed-event age shall be monitored per telco, partition and consumer. |  
| EVT-014 | Should | A canonical event envelope shall permit transport migration without changing domain semantics. |  
| DAT-001 | Must | Each data entity shall have a named system-of-record service, retention class, classification, legal basis and permitted consumers. |  
| DAT-002 | Must | Transactional stores shall enforce tenant and aggregate integrity through keys, constraints and service-level permissions, not application convention alone. |  
| DAT-003 | Must | Financial journals shall be append-only and protected from ordinary update/delete permissions. |  
| DAT-004 | Must | Derived balances and summaries shall be reproducible from authoritative journals or source events. |  
| DAT-005 | Must | Online decision data shall be separated from raw historical feeds and optimised for predictable point lookup. |  
| DAT-006 | Must | Raw inbound files and records shall be retained immutably with checksum, source, ingestion time and schema version for the approved evidence period. |  
| DAT-007 | Must | Data corrections shall create new versions or compensating records rather than overwrite evidence of prior received values. |  
| DAT-008 | Must | Subscriber MSISDN values shall be encrypted or tokenised and displayed masked by default. |  
| DAT-009 | Should | Date-partitioned high-volume tables shall support pruning, retention and archival without locking the active workload. |  
| DAT-010 | Must | Database migrations shall be backward-compatible with rolling deployment and include tested rollback or roll-forward procedures. |  
| DAT-011 | Must | Analytical data shall be sourced through governed replication/events rather than queries against production OLTP at scale. |

| DAT-012 | Must | Search indexes and caches shall be treated as derived, rebuildable stores and shall never be the sole record of a financial fact. |  
| DAT-013 | Must | Retention and deletion jobs shall be idempotent, auditable and tenant-scoped, with legal holds overriding automated deletion. |  
| DAT-014 | Must | Backups shall be encrypted, integrity-tested and periodically restored into an isolated environment. |  
| DAT-015 | Must | Data quality metrics shall include completeness, timeliness, uniqueness, validity and reconciliation to telco controls. |  
| SUB-001 | Must | A subscriber account shall represent the relationship between an MSISDN and a telco for an effective period, not an eternal identity. |  
| SUB-002 | Must | The platform shall support MSISDN port-out, port-in, disconnection, recycle and reactivation events without merging unrelated customer histories. |  
| SUB-003 | Must | A recycled number shall not inherit offers, consent, outstanding exposure or adverse history without an approved verified identity-linking rule. |  
| SUB-004 | Must | NIN status shall be represented as source-provided verification flags and timestamps; the platform shall not require raw NIN where a verified flag suffices. |  
| SUB-005 | Must | Subscriber status changes that create immediate risk shall be available as real-time overlays or frequent delta feeds. |  
| SUB-006 | Must | The platform shall preserve source-system timestamps and distinguish unknown from false for identity and eligibility attributes. |  
| SUB-007 | Must | Subscriber merge or split operations shall require privileged approval, reason, before/after evidence and financial impact checks. |  
| SUB-008 | Must | A support user shall view a masked subscriber profile, offers, advance history and evidence only within authorised telco scope. |  
| SUB-009 | Must | The platform shall support subscriber self-exclusion and programme opt-out states that immediately suppress new offers. |  
| SUB-010 | Should | Identity-resolution confidence shall be retained where cross-telco or bureau matching is performed. |  
| CFG-001 | Must | Configuration shall support Draft, Submitted, Approved, Scheduled, Active, Superseded, RolledBack and Rejected states. |  
| CFG-002 | Must | The maker of a material configuration change shall not approve the same change. |  
| CFG-003 | Must | Activation shall be atomic from the perspective of decisioning; a request shall not observe a mixture of configuration versions. |  
| CFG-004 | Must | Every active configuration shall have effective\_from, optional effective\_to, owner, approval, reason and content hash. |  
| CFG-005 | Must | High-risk changes shall require simulation against historical cohorts before approval. |  
| CFG-006 | Must | The system shall detect impossible or unsafe values, overlapping effective periods and missing dependencies before activation. |  
| CFG-007 | Must | Rollback shall activate a prior approved version or a new corrective version without mutating history. |  
| CFG-008 | Must | Feature flags and kill switches shall be scoped by environment, telco, programme, product, channel and cohort where applicable. |  
| CFG-009 | Must | Secrets shall never be stored as ordinary product configuration values. |  
| CFG-010 | Must | Configuration export/import shall be signed, versioned and environment-controlled to prevent accidental production promotion. |  
| CFG-011 | Must | Rule and score configuration shall have human-readable explanations suitable for audit and customer-support interpretation. |

| CFG-012 | Must | Ledger posting templates shall not activate unless debits equal credits per currency under all permitted branches. |  
| CFG-013 | Must | Portfolio guardrail thresholds shall include safe defaults and cannot be disabled without elevated approval. |  
| CFG-014 | Should | A complete configuration dependency graph shall be available to show which products, rules, channels and programmes use a version. |  
| SCR-001 | Must | The feature platform shall compute configurable windows including recent, medium-term and long-term recharge, usage and activity measures. |  
| SCR-002 | Must | Features shall record source coverage, as-of time, missingness and quality flags. |  
| SCR-003 | Must | Single-period recharge totals shall not directly determine limits without stability, historical and anomaly controls. |  
| SCR-004 | Must | Anti-gaming processing shall support medians, trimmed means, winsorisation, percentile caps, spike ratios and repeated-behaviour validation. |  
| SCR-005 | Must | The platform shall distinguish behavioural risk, affordability, trust/repayment and fraud dimensions rather than collapse all inputs into an opaque score. |  
| SCR-006 | Must | The assigned limit shall be constrained by product, subscriber, programme, telco, funding and portfolio caps. |  
| SCR-007 | Must | Upward tier movement shall default to at most one tier per scoring cycle and be configurable through approved policy. |  
| SCR-008 | Must | Downward movement or suspension may occur immediately when risk overlays require it. |  
| SCR-009 | Must | A new or thin-file subscriber shall use a configurable starter policy and progressive-trust path. |  
| SCR-010 | Must | Model/rule execution shall generate reason codes, component contributions and policy constraints for every decision. |  
| SCR-011 | Must | Decision replay shall reproduce the original result from retained feature, rule, model and configuration versions, subject to deterministic implementation. |  
| SCR-012 | Must | Champion/challenger evaluation shall not change customer outcomes unless the challenger is explicitly authorised for a controlled cohort. |  
| SCR-013 | Must | Training and analytical datasets shall prevent leakage from future repayment outcomes into historical features. |  
| SCR-014 | Must | Model deployment shall require validation evidence, approval, monitoring thresholds and rollback readiness. |  
| SCR-015 | Must | Real-time decision evaluation shall apply current exposure, outstanding advances, self-exclusion, telco status, fraud flags, funding and guardrail states. |  
| SCR-016 | Must | A stale feature snapshot shall be accepted, degraded or rejected according to explicit product policy and age thresholds. |  
| SCR-017 | Must | Missing required features shall not be silently imputed unless the approved model explicitly defines the imputation. |  
| SCR-018 | Must | The platform shall monitor approval rate, limit distribution, delinquency, recovery and drift by telco, cohort and model version. |  
| OFR-001 | Must | An offer shall be a separate entity from an advance and shall have Generated, Presented, Accepted, Expired, Withdrawn and Superseded states. |  
| OFR-002 | Must | The offer snapshot shall freeze denomination, fee, taxes, net value, repayment amount, product and disclosure version for the acceptance window. |  
| OFR-003 | Must | Offer acceptance shall fail safely when the offer has expired, been withdrawn or no longer passes critical real-time overlays. |  
| OFR-004 | Must | A material price or disclosure change shall generate a new offer and require fresh acceptance. |

| OFR-005 | Must | Offer lists shall contain only denominations within the approved maximum and current product configuration. |  
| OFR-006 | Must | The platform shall retain evidence of what was displayed or delivered, in which language, through which channel and when. |  
| OFR-007 | Must | Where USSD cannot display all disclosure text, the channel shall present mandatory summary fields and deliver the retained full disclosure through an approved complementary method. |  
| OFR-008 | Should | An offer shall not reserve funding or exposure until the configured reservation point, normally acceptance or advance request. |  
| OFR-009 | Should | Offer generation shall be idempotent within the configured request context and may return an existing still-valid offer snapshot. |  
| ADV-001 | Must | Advance creation shall require an accepted valid offer snapshot and retained disclosure acknowledgement. |  
| ADV-002 | Must | The advance service shall enforce max\_concurrent\_advances and total exposure atomically under concurrent requests. |  
| ADV-003 | Must | Exposure and funding reservation shall use atomic conditional updates or serialisable controls that prevent over-allocation. |  
| ADV-004 | Must | A duplicate request with the same idempotency key shall return the same advance and outcome. |  
| ADV-005 | Must | The platform shall preserve separate client request ID, platform advance ID, fulfilment attempt ID and telco transaction reference. |  
| ADV-006 | Must | Remote fulfilment shall not occur inside an open local database transaction. |  
| ADV-007 | Must | State transitions shall use optimistic version checks or equivalent concurrency controls. |  
| ADV-008 | Must | Only permitted transitions shall be accepted; invalid transitions shall be rejected and audited. |  
| ADV-009 | Must | FULFILMENT\_UNKNOWN shall trigger status enquiry and reconciliation workflow with configurable escalation timers. |  
| ADV-010 | Must | A confirmed failed fulfilment shall release reservations exactly once. |  
| ADV-011 | Must | A late success received after a local failure assumption shall activate the same advance and reverse any released reservation effect without creating a second advance. |  
| ADV-012 | Must | Partial fulfilment shall be rejected unless the product explicitly supports it; supported partial value shall create a correctly repriced or adjusted advance with fresh evidence. |  
| ADV-013 | Must | Reversal of telco value after activation shall follow a controlled reversal state and financial compensation flow. |  
| ADV-014 | Must | A manual resolution action shall require privileged approval, evidence, reason and balanced financial treatment. |  
| ADV-015 | Must | Advance timestamps shall retain customer acceptance, platform receipt, validation, fulfilment submission, telco event and activation times. |  
| ADV-016 | Must | The customer-facing response shall not expose internal ambiguity details but shall provide a safe status and SMS fallback where required. |  
| CHN-001 | Must | USSD menu flows shall be versioned by telco, shortcode, language, product and effective period. |  
| CHN-002 | Must | Session state shall include tenant, session ID, subscriber account, selected offer snapshot, step, language and expiry, but shall not itself be the financial record. |  
| CHN-003 | Must | The session timeout budget shall be configurable by telco and monitored at each step. |  
| CHN-004 | Must | The confirm action shall submit an idempotent advance command before rendering the final response. |

| CHN-005 | Must | When the session ends after confirmation but before response, the platform shall complete safely and send an SMS or approved fallback notification. |  
| CHN-006 | Must | Repeated user input caused by gateway retries shall not repeat advance creation. |  
| CHN-007 | Must | The menu shall present total repayment, value received, fee/cost and confirmation wording within regulatory and channel constraints. |  
| CHN-008 | Should | Language packs shall support English and configurable local languages without embedding business values in text templates. |  
| CHN-009 | Must | A customer shall be able to check eligibility, available offers, outstanding balance and recent advance status where the telco channel permits. |  
| CHN-010 | Must | USSD inputs shall be constrained to expected values and lengths; free text shall not reach domain queries or logs unsanitised. |  
| CHN-011 | Must | The platform shall distinguish a new session from a continuation and reject stale continuation tokens. |  
| CHN-012 | Must | Session data shall expire automatically and shall not be used as consent evidence after expiry. |  
| CHN-013 | Must | Channel analytics shall track abandonment, step latency, failures, session cost, conversion and post-confirmation drops per telco. |  
| CHN-014 | Must | Channel availability controls shall allow offer enquiries to be disabled independently from recovery ingestion and finance processing. |  
| CHN-015 | Must | A telco app or API journey shall consume the same offer and advance domain APIs as USSD and shall not implement separate credit logic. |  
| CHN-016 | Must | Accessibility and plain-language standards shall apply to web/app surfaces, including clear cost and repayment information. |  
| NOT-001 | Must | Notification templates shall be versioned, language-specific, programme-scoped and approved before activation. |  
| NOT-002 | Must | Messages shall separate transactional, servicing, collections and marketing purposes for consent and DND enforcement. |  
| NOT-003 | Should | The service shall support SMS at minimum and permit push, email, WhatsApp or IVR adapters without changing domain events. |  
| NOT-004 | Must | Delivery attempts, provider references, statuses and final outcomes shall be retained. |  
| NOT-005 | Must | Retry policy shall depend on message criticality and provider error; permanent failures shall not loop. |  
| NOT-006 | Must | Quiet hours and contact-frequency caps shall be configurable and conduct-controlled. |  
| NOT-007 | Must | Sender identity shall be configured per telco/programme and validated against approved registrations. |  
| NOT-008 | Must | Templates shall use allowlisted variables and escape untrusted values. |  
| NOT-009 | Must | A notification failure shall not roll back a completed advance or recovery; it shall create a servicing exception and fallback where configured. |  
| NOT-010 | Must | Customer opt-out and self-exclusion shall be enforced immediately for non-mandatory communications. |  
| COL-001 | Must | Every recharge, deduction, recovery and reversal event shall be deduplicated by telco and source event identity. |  
| COL-002 | Must | Recovery allocation shall be deterministic and versioned, with default oldest-due-first unless programme policy states otherwise. |  
| COL-003 | Must | A recovery shall not exceed total outstanding exposure; excess shall remain with the telco/customer according to the agreed rail behaviour. |

| COL-004 | Must | Partial recovery shall reduce the correct principal, fee and tax components according to configured waterfall and accounting policy. |  
| COL-005 | Must | Recovery posting and advance balance update shall be atomic within the platform financial boundary. |  
| COL-006 | Must | An out-of-order reversal received before the original recovery shall be held and matched or quarantined without creating a negative recovery. |  
| COL-007 | Must | Recovery after write-off shall post to the approved recovery income/receivable treatment while preserving original write-off history. |  
| COL-008 | Must | The platform shall support one active advance by default and configurable multiple advances, with explicit allocation across them. |  
| COL-009 | Must | Recharge events that cannot be linked confidently to a subscriber account shall enter an exception queue and shall not be guessed. |  
| COL-010 | Must | Collections aging shall use configurable buckets and event time rules while preserving original due/activation dates. |  
| COL-011 | Must | Dunning schedules shall observe quiet hours, frequency caps, language, self-exclusion scope and conduct rules. |  
| COL-012 | Must | The product shall not escalate to field or harassing collection methods; permitted strategies shall be explicitly configured and approved. |  
| COL-013 | Must | Write-off shall require policy eligibility, maker-checker approval or authorised automated threshold and balanced journal posting. |  
| COL-014 | Must | A recovery reversal that reopens a closed advance shall reinstate the correct outstanding amount and delinquency position. |  
| COL-015 | Must | Customer balance enquiries shall derive from authoritative recovery and ledger state, not stale telco-only balances. |  
| LED-001 | Must | Every journal shall balance debits and credits per currency before commit. |  
| LED-002 | Must | An unbalanced or incomplete posting template shall fail validation and cannot activate. |  
| LED-003 | Must | Posted journals and entries shall be immutable; corrections shall use linked reversal and replacement journals. |  
| LED-004 | Must | Journal posting shall be idempotent using a unique business event key and event type. |  
| LED-005 | Must | The ledger shall retain legal entity, telco, programme, product, advance, subscriber token, funding source, currency and accounting period dimensions where applicable. |  
| LED-006 | Must | No journal shall be created for a fulfilment that is merely pending or unknown unless the approved accounting policy explicitly requires a memorandum/reservation entry. |  
| LED-007 | Must | The ledger shall support principal receivable, fee income, tax, telco share, platform share, funder payable/cost, cash/settlement and write-off accounts. |  
| LED-008 | Must | Derived account balances shall be rebuildable from entries and compared periodically with stored summaries. |  
| LED-009 | Must | Posting templates shall be effective-dated and the journal shall retain the template version used. |  
| LED-010 | Must | A closed accounting period shall reject ordinary backdated posting and require controlled adjustment-period treatment. |  
| LED-011 | Must | Manual journals shall be exceptional, maker-checker approved, reasoned and separately reported. |  
| LED-012 | Must | The ledger shall preserve source event, causation, correlation and reversal links. |  
| LED-013 | Must | Currency rounding shall use an approved deterministic policy and shall post explicit rounding differences where unavoidable. |

| LED-014 | Must | Ledger queries used for statements and reconciliation shall be reproducible as-of a specified cutoff. |  
| LED-015 | Must | Database permissions shall prevent application users and non-ledger services from modifying journal tables. |  
| LED-016 | Should | Journal throughput shall scale independently from customer channel services. |  
| REC-001 | Must | The platform shall reconcile advance requests, telco fulfilments, active advances, recoveries, reversals, ledger journals and settlement statements through separate but linked controls. |  
| REC-002 | Must | Every reconciliation process shall record source cutoffs, control totals, records compared, match rules, tolerances and run version. |  
| REC-003 | Must | Exact reference matching shall precede tolerant or composite matching; fuzzy matching shall not automatically resolve financial breaks. |  
| REC-004 | Must | Breaks shall have type, value, age, owner, status, evidence, resolution and financial-impact fields. |  
| REC-005 | Must | A reconciliation rerun shall be idempotent and shall preserve prior run evidence. |  
| REC-006 | Must | Late telco records shall update the relevant reconciliation period without deleting prior break history. |  
| REC-007 | Must | Control totals shall include counts, face value, fees, recoveries, reversals and settlement amounts by telco/programme/currency. |  
| REC-008 | Must | Settlement calculation shall use approved revenue-share, funding, tax and rounding configuration effective for the underlying transaction. |  
| REC-009 | Must | Settlement statements shall be versioned and adjustments shall be separately identifiable rather than silently changing issued statements. |  
| REC-010 | Must | Finance users shall be able to trace a settlement line to journals, recoveries, advances and telco source records. |  
| REC-011 | Must | Tolerance-based auto-resolution shall be configurable, capped and reported; material breaks require approval. |  
| REC-012 | Must | Reconciliation backlogs and unquantified breaks shall trigger escalation and may suspend originations where exposure cannot be trusted. |  
| REC-013 | Must | Files and API extracts used for settlement shall be signed/encrypted and include manifests and checksums. |  
| REC-014 | Must | The platform shall support telco, platform, funder, tax and legal-entity views of settlement obligations. |  
| TRE-001 | Must | Each programme shall be linked to one or more funding pools with currency, owner, available amount, committed amount, exposure cap and status. |  
| TRE-002 | Must | Exposure reservation shall reduce available capacity atomically and release exactly once on failure or cancellation. |  
| TRE-003 | Must | Originations shall stop automatically when funding capacity, programme exposure or portfolio guardrail thresholds are breached. |  
| TRE-004 | Must | Recoveries, settlement receipts and approved replenishments shall update funding availability through auditable events and journals. |  
| TRE-005 | Should | Funding cost accrual shall support fixed, variable, tiered or statement-driven arrangements and retain effective terms. |  
| TRE-006 | Must | The platform shall prevent one programme or telco from consuming another programme's restricted funding pool. |  
| TRE-007 | Must | Treasury dashboards shall display committed, utilised, available, overdue and recovered amounts by source and programme. |  
| TRE-008 | Must | Manual funding adjustments shall require maker-checker approval and balanced financial treatment. |  
| TRE-009 | Must | Funding-pool status shall be a real-time overlay in decisioning. |

| TRE-010 | Should | The system shall forecast expected recoveries and liquidity utilisation using versioned assumptions without changing ledger truth. |  
| BUR-001 | Must | Release 1 shall include a configurable bureau-export capability even where live submission is initially disabled. |  
| BUR-002 | Must | Bureau mappings shall support multiple bureaux through adapters and a canonical credit-reporting record. |  
| BUR-003 | Must | Every submitted bureau record shall retain source ledger cutoff, mapping version, submission batch, acknowledgement and correction history. |  
| BUR-004 | Must | Rejected bureau records shall enter an exception workflow and shall not be silently omitted. |  
| BUR-005 | Must | Corrections and disputes shall create versioned replacement/correction records linked to the original submission. |  
| BUR-006 | Must | Bureau export shall apply programme/legal-entity enablement, consent/lawful-basis and minimum-reporting rules. |  
| REG-001 | Must | The platform shall retain disclosure content, rendered values, language, channel, customer action, timestamp and cryptographic content hash per advance. |  
| REG-002 | Must | Complaint cases shall support intake, categorisation, SLA clocks, ownership, correspondence, redress, root cause and regulatory export. |  
| REG-003 | Must | Customer data-subject requests shall be tracked with identity verification, scope, deadlines, decisions and evidence. |  
| REG-004 | Must | Regulatory exports shall be generated from versioned, reproducible queries and retained with control totals and approval. |  
| REG-005 | Must | The platform shall support legal holds that suspend deletion for affected records without changing ordinary retention policy. |  
| REG-006 | Must | Regulatory configuration shall be scoped by jurisdiction and programme, but mandatory Nigeria deployment constraints cannot be left unset. |  
| UI-001 | Must | The front end shall use tenant-aware backend-for-frontend or APIs that enforce server-side authorisation; UI hiding alone is insufficient. |  
| UI-002 | Must | All high-risk actions shall show the affected telco, programme, environment and effective time before confirmation. |  
| UI-003 | Must | Maker-checker workflows shall prevent the same identity from creating and approving a controlled change. |  
| UI-004 | Must | Search results shall mask MSISDN and sensitive fields by default, with audited step-up reveal where authorised. |  
| UI-005 | Must | Tables shall support server-side pagnination, export controls and bounded filters for very large datasets. |  
| UI-006 | Must | Exports shall be asynchronous, access-controlled, watermarked/labelled, time-limited and audited. |  
| UI-007 | Must | Every material entity shall expose a chronological audit timeline with source references and state changes. |  
| UI-008 | Must | Decision explanations shall show inputs, reason codes, constraints and versions without exposing proprietary or unsafe detail to unauthorised roles. |  
| UI-009 | Must | Manual repair actions shall display predicted financial and state impact before submission. |  
| UI-010 | Must | Dashboards shall indicate data freshness and cutoff times. |  
| UI-011 | Must | The interface shall meet applicable accessibility requirements for keyboard access, contrast, labels and screen-reader structure. |  
| UI-012 | Must | Session timeout, reauthentication and step-up authentication shall depend on role and action sensitivity. |

| UI-013 | Must | The portal shall not cache sensitive responses in shared browsers or intermediary proxies. |  
| UI-014 | Must | Configuration forms shall use typed controls, range validation, dependency warnings and simulation links rather than unrestricted JSON for routine users. |  
| UI-015 | Must | Bulk actions shall show selection scope, item count, impact and partial-failure results. |  
| SEC-001 | Must | All external and privileged interfaces shall use strong authenticated identities; anonymous access is limited to explicitly approved public content. |  
| SEC-002 | Must | Service-to-service authentication shall use short-lived workload identities or mutually authenticated certificates rather than shared static credentials. |  
| SEC-003 | Must | Human access shall use central identity, MFA and role plus attribute-based telco/programme scope. |  
| SEC-004 | Must | Privileged access shall be time-bound, approved and recorded; standing production administrator access shall be minimised. |  
| SEC-005 | Must | Secrets shall be stored in an approved secret manager, rotated and never logged or embedded in source/config files. |  
| SEC-006 | Must | Data shall be encrypted in transit and at rest using organisation-approved cryptography and key management. |  
| SEC-007 | Must | Key use shall be separated by environment and support telco/legal-entity separation where required. |  
| SEC-008 | Must | Application and database logs shall exclude raw NIN, authentication secrets, full MSISDN and unnecessary customer content. |  
| SEC-009 | Must | PII reveal, export and manual adjustment actions shall generate immutable audit events. |  
| SEC-010 | Must | All input shall be validated and output encoded; APIs and portals shall implement protection against common web/API attack classes. |  
| SEC-011 | Must | Rate limiting and abuse controls shall apply by client, telco, subscriber token, session and operation as appropriate. |  
| SEC-012 | Must | Financial commands shall include replay protection and bounded timestamp/nonces where signatures are used. |  
| SEC-013 | Must | Software dependencies and container images shall be scanned, signed and promoted through controlled registries. |  
| SEC-014 | Must | Production data shall not be copied to non-production environments without approved masking or synthetic replacement. |  
| SEC-015 | Must | Security events shall feed central detection with telco context, severity and response playbooks. |  
| SEC-016 | Must | Tenant isolation shall be tested through automated negative tests in CI and periodic independent assessment. |  
| SEC-017 | Must | Administrative APIs shall support step-up authentication for high-impact actions such as rearming guardrails, manual journals and tenant-wide changes. |  
| SEC-018 | Must | The platform shall support secure deletion of keys and derived data while retaining legally required financial records. |  
| SEC-019 | Must | Threat modelling shall be performed for each major release and material telco integration. |  
| SEC-020 | Must | A vulnerability shall not be accepted solely because an internal network boundary exists. |  
| INF-001 | Must | Production services shall run across at least two failure zones with automated rescheduling and health-based traffic removal. |  
| INF-002 | Must | Stateful components shall use supported high-availability configurations and documented failover procedures. |  
| INF-003 | Must | Infrastructure shall be defined as code, peer-reviewed and promoted through controlled environments. |

| INF-004 | Must | Environment configuration and secrets shall be externalised from application images. |  
| INF-005 | Must | Autoscaling shall use service-appropriate metrics and bounded limits to prevent runaway cost or dependency overload. |  
| INF-006 | Must | Resource quotas and priority classes shall protect financial recovery and ledger workloads from non-critical workloads. |  
| INF-007 | Must | Database connections shall be pooled and capped per service and tenant-sensitive workload. |  
| INF-008 | Must | Maintenance and deployment shall preserve at least the minimum healthy capacity required by SLOs. |  
| INF-009 | Must | Recovery-region data replication shall meet the defined RPO by data class; ledger data requires near-zero loss objective. |  
| INF-010 | Must | Production backups shall be isolated from ordinary application credentials and protected against destructive compromise. |  
| INF-011 | Must | Network egress shall be restricted to approved destinations and observable by service. |  
| INF-012 | Must | Telco private connectivity, VPN or dedicated links shall terminate in controlled integration zones with redundant paths where contracted. |  
| INF-013 | Should | Capacity reservations or pre-scaled headroom shall exist for predictable recharge and campaign peaks. |  
| INF-014 | Must | Platform components shall expose version and build provenance to operations without revealing sensitive internals publicly. |  
| INF-015 | Must | Infrastructure cost shall be attributable by telco/programme using tags, metrics or allocation models. |  
| SCL-001 | Must | The data platform shall support at least 100 million active subscriber profiles and configurable historical feature windows. |  
| SCL-002 | Must | Offer enquiry p95 service time, excluding telco channel transit, shall target 300 ms and p99 750 ms under agreed load. |  
| SCL-003 | Must | Advance validation and exposure reservation p95 shall target 500 ms excluding telco fulfilment latency. |  
| SCL-004 | Must | The platform shall support horizontal scaling of channel, decision, adapter, recovery and event-consumer workloads independently. |  
| SCL-005 | Must | Daily or scheduled feature computation shall complete within the agreed scoring window with restartable partitions and no all-or-nothing rerun. |  
| SCL-006 | Must | Hot subscriber or telco partitions shall not create unbounded skew; partition strategy shall be load-tested. |  
| SCL-007 | Must | Caches shall use bounded TTL, quotas and stampede protection; cache loss shall degrade performance but not correctness. |  
| SCL-008 | Must | Large exports, reports and reconciliation jobs shall not execute on synchronous portal request threads. |  
| SCL-009 | Must | Rate limits shall protect downstream telco APIs and shall support fair allocation among programmes. |  
| SCL-010 | Must | Capacity models shall include USSD peaks, bulk recharge events, scoring windows, statement cycles and incident replay. |  
| SCL-011 | Must | Performance tests shall include duplicate events, retries and degraded dependencies, not only clean steady-state traffic. |  
| SCL-012 | Must | Cost-per-offer, cost-per-advance, cost-per-active-subscriber and cost-per-recovery shall be measured by programme. |  
| RES-001 | Must | Every dependency shall have explicit timeout, retry, circuit-breaker and fallback policy. |  
| RES-002 | Must | The system shall fail closed for new credit when identity, exposure, funding or decision integrity cannot be established. |

| RES-003 | Must | Recovery ingestion and ledger posting shall remain prioritised when offer generation is disabled or degraded. |  
| RES-004 | Must | A telco fulfilment outage shall open only the affected telco/programme circuit and shall not stop other telcos. |  
| RES-005 | Must | Unknown fulfilments shall be durable across restarts and automatically re-enquired or reconciled. |  
| RES-006 | Must | Worker processing shall use leases or visibility timeouts that permit safe reassignment after failure. |  
| RES-007 | Must | Jobs shall checkpoint and resume without duplicating financial effects. |  
| RES-008 | Must | DR failover shall preserve idempotency stores, event offsets and configuration versions needed to prevent duplicate processing. |  
| RES-009 | Must | Clock skew shall be monitored; business expiry shall use trusted server time and preserve source timestamps. |  
| RES-010 | Must | Runbooks shall define safe-mode behaviour for database, event bus, cache, telco link, bureau, notification and observability failures. |  
| RES-011 | Must | Load shedding shall reject low-priority work explicitly rather than allow uncontrolled timeouts and resource exhaustion. |  
| RES-012 | Must | The platform shall support controlled pausing and draining of individual consumers before deployment or incident repair. |  
| OBS-001 | Must | Every request shall produce structured logs, metrics and traces linked by correlation ID without exposing prohibited PII. |  
| OBS-002 | Must | Technical telemetry shall be complemented by business metrics for offers, approvals, fulfilments, unknowns, recoveries, exposure and breaks. |  
| OBS-003 | Must | Metrics shall be segmented by telco, programme, product, channel and environment where cardinality remains controlled. |  
| OBS-004 | Must | Alerts shall be symptom- and business-impact-oriented, deduplicated and tied to owned runbooks. |  
| OBS-005 | Must | Audit events shall record actor, action, target, before/after or version references, reason, source IP/device context and time. |  
| OBS-006 | Must | Audit storage shall be tamper-evident and access-separated from ordinary application administration. |  
| OBS-007 | Must | Financial reconciliation and ledger control failures shall have higher severity than analytics freshness failures. |  
| OBS-008 | Must | SLOs shall be calculated from defined service-level indicators and error budgets. |  
| OBS-009 | Must | The platform shall monitor FULFILMENT\_UNKNOWN age and value, not only count. |  
| OBS-010 | Must | Batch pipelines shall expose input count, rejected count, output count, control totals, partition progress and estimated completion. |  
| OBS-011 | Must | Cost telemetry shall attribute compute, messaging, storage, SMS and USSD cost where data is available. |  
| OBS-012 | Must | Operational data retention shall balance investigation needs, privacy and cost; long-term evidence belongs in governed archives. |  
| SIM-001 | Must | A standing simulator shall implement the canonical telco APIs, events and batch interfaces before live telco connectivity is available. |  
| SIM-002 | Must | The simulator shall support configurable success, decline, pending, timeout, malformed response, throttling and unavailability behaviours. |  
| SIM-003 | Must | It shall reproduce timeout-after-success and delayed callback scenarios. |  
| SIM-004 | Must | It shall emit duplicate, missing, out-of-order and reversal-before-original events. |  
| SIM-005 | Must | It shall generate batch files with valid manifests and inject checksum, count, schema and truncation faults. |

| SIM-006 | Must | It shall support deterministic seeded scenarios so failures are reproducible. |  
| SIM-007 | Must | The harness shall validate requests and responses against canonical and telco-specific schemas. |  
| SIM-008 | Must | Certification results shall produce a signed evidence pack with test IDs, payloads, outcomes and timestamps. |  
| SIM-009 | Must | The simulator shall support realistic latency distributions and load generation. |  
| SIM-010 | Must | Telco adapter certification shall include all applicable Volume 1 edge cases before production approval. |  
| SIM-011 | Must | Simulator environments shall contain only synthetic subscriber data. |  
| SIM-012 | Must | The same contract tests shall run against simulator and telco sandbox endpoints where possible. |  
| TST-001 | Must | Every numbered requirement shall map to one or more automated tests, controlled manual tests or review evidence. |  
| TST-002 | Must | Domain state machines and ledger rules shall have exhaustive transition and invariant tests. |  
| TST-003 | Must | Property-based tests shall cover ledger balance, idempotency, recovery caps and allocation invariants. |  
| TST-004 | Must | Contract tests shall validate provider and consumer compatibility for APIs and events. |  
| TST-005 | Must | Tenant-isolation tests shall attempt cross-tenant reads, writes, cache access, event consumption and export. |  
| TST-006 | Must | Concurrency tests shall cover duplicate confirmations, simultaneous advances, recovery during fulfilment and configuration activation. |  
| TST-007 | Must | Chaos tests shall cover dependency timeout, partial outage, worker death, message duplication and database failover. |  
| TST-008 | Must | Performance tests shall use realistic data volumes, partition skew and telco latency. |  
| TST-009 | Must | Security testing shall include SAST, dependency scanning, secrets scanning, DAST, API testing and penetration testing. |  
| TST-010 | Must | Data pipeline tests shall reconcile input/output counts and known expected features over golden datasets. |  
| TST-011 | Must | Model tests shall cover drift, fairness/segment outcomes, explainability, missingness, leakage and rollback. |  
| TST-012 | Must | Reconciliation tests shall include late, duplicate, missing, contradictory and corrected source records. |  
| TST-013 | Must | USSD tests shall cover session expiry at every step, repeated input and post-confirmation session loss. |  
| TST-014 | Must | DR tests shall demonstrate recovery of configuration, idempotency, event positions and ledger integrity. |  
| TST-015 | Must | Accessibility tests shall cover the administrative and customer-facing web/app interfaces. |  
| TST-016 | Must | Production deployment shall be blocked when release-gating tests or evidence are missing. |  
| DEV-001 | Must | Source code, infrastructure, schemas and configuration definitions shall be version controlled with protected branches and review. |  
| DEV-002 | Must | Builds shall be reproducible and produce signed artefacts with source revision, dependency and provenance metadata. |  
| DEV-003 | Must | CI shall run linting, unit, contract, architecture, security and schema compatibility checks. |  
| DEV-004 | Must | Deployment shall use automated pipelines with environment approvals and no manual modification of production nodes. |  
| DEV-005 | Must | Database changes shall use expand-migrate-contract patterns or equivalent rolling-compatible techniques. |

| DEV-006 | Must | Feature flags shall separate deployment from release and include owner, expiry/review date and rollback plan. |  
| DEV-007 | Must | Production configuration shall be promoted from reviewed definitions or created through governed portal workflows, not edited directly in databases. |  
| DEV-008 | Must | Non-production environments shall use synthetic/masked data and independent credentials. |  
| DEV-009 | Should | Canary or progressive delivery shall be available by telco, programme, cohort or traffic percentage for material changes. |  
| DEV-010 | Must | Rollback shall consider code, schema, configuration, event compatibility and in-flight sagas. |  
| DEV-011 | Must | Release notes shall list requirements delivered, migrations, configuration dependencies, known risks and operational actions. |  
| DEV-012 | Must | Emergency changes shall follow an expedited but auditable approval and retrospective process. |  
| DEV-013 | Must | End-of-life dependencies and runtimes shall be identified and remediated through a supported-technology lifecycle. |  
| MIG-001 | Must | Incumbent migration shall define authoritative source fields, mapping, transformation, reconciliation and acceptance for subscriber eligibility, limits, active advances, recoveries and financial balances. |  
| MIG-002 | Must | Historical data shall be classified into required operational history, regulatory evidence, analytical history and archive-only data. |  
| MIG-003 | Must | Every migration load shall have manifests, checksums, record counts, value totals, rejects and rerun identity. |  
| MIG-004 | Must | Active advances shall be migrated with exact outstanding components, dates, telco references and reconciliation status. |  
| MIG-005 | Must | The platform shall support shadow scoring against incumbent outputs before decision cutover. |  
| MIG-006 | Must | Dual run shall define which system is authoritative for offers, fulfilment, recovery and ledger at each stage to prevent double lending or collection. |  
| MIG-007 | Must | A subscriber shall not be active for origination in both incumbent and new platform unless a controlled split-cohort design proves mutual exclusion. |  
| MIG-008 | Must | Cutover shall include a freeze/cutoff, final delta, control totals, smoke tests and rollback criteria. |  
| MIG-009 | Must | In-flight unknown fulfilments and unmatched recoveries shall have explicit ownership and migration treatment. |  
| MIG-010 | Must | Post-cutover hypercare shall compare telco, platform, ledger and settlement totals at increased frequency. |  
| MIG-011 | Must | Rollback shall not lose advances or recoveries created after cutover and shall be financially reconciled. |  
| MIG-012 | Must | Migration data and tooling shall be access-controlled, time-limited and retired after approved retention. |  
| NFR-001 | Must | All SLOs shall define measurement point, exclusions, window and data source. |  
| NFR-002 | Must | A higher availability commitment shall not be approved without costed architecture and tested failover consistent with the target. |  
| NFR-003 | Must | Data loss tolerance shall be defined per store; ledger and acknowledged recovery events have the strictest class. |  
| NFR-004 | Must | The design shall prefer correctness over availability for new lending when exposure or financial integrity is uncertain. |  
| NFR-005 | Must | The design shall prefer continued durable ingestion over real-time presentation for recovery events during partial outages. |  
| NFR-006 | Must | All critical services shall have capacity, resilience, security and support ownership before production. |

## Volume 2 Conclusion

This Volume 2 establishes the technical build baseline for an institutional-grade, multi-telco digital credit platform. It deliberately separates stable domain contracts and non-configurable financial/security invariants from replaceable implementation technologies. Engineering may refine component packaging and technology choices through approved ADRs, but must preserve the system-of-record boundaries, tenant isolation, idempotency, fulfilment ambiguity controls, decision replay, ledger integrity and evidence requirements specified here.

Next volume — Volume 3 should convert this build baseline into the production operating model: service operations, support, treasury processes, reconciliations, incident management, controls, deployment waves, migration runbooks, readiness gates and acceptance evidence.