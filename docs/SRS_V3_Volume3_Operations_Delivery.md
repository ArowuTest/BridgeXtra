# Telco Digital Credit Platform

**Enterprise Blueprint & Software Requirements Specification v3.0**

## VOLUME 3

## OPERATIONS, ASSURANCE & DELIVERY

Production operating model, service management, financial and risk operations, security, continuity, testing, telco onboarding, migration, rollout and go-live assurance.

Status: Production operations and delivery baseline

Date: 16 July 2026

Classification: Confidential - Controlled Document

## Document Control

| Document | Telco Digital Credit Platform V3.0 - Volume 3: Operations, Assurance & Delivery |
| --- | --- |
| Purpose | Define the operating model, procedures, controls, readiness evidence and delivery governance required to launch and run the platform. |
| Audience | Executives, programme leadership, product, risk, finance, treasury, operations, support, compliance, privacy, security, data, engineering, SRE, QA, telco and funding partners. |
| Status | Baseline for Release 1 operational design and acceptance. |
| Precedence | Read with Volume 1 and Volume 2. Non-configurable enterprise and technical invariants remain binding. |
| Owner | Platform Service Owner / Programme Director |
| Review cycle | At least annually and after material product, telco, regulation, architecture or incident change. |

## Version History

| Version | Date | Status | Summary |
| --- | --- | --- | --- |
| 3.0 | 16 July 2026 | Initial baseline | Full operations, assurance, migration, rollout and go-live companion to Volumes 1 and 2. |

## How to Use This Document

-   Executives and programme governance: use Sections 1-5, 28, 31-32 and 38-40 to approve the operating model, delivery sequence and production risk.
    
-   Service, operations and SRE teams: use Sections 6-13, 17, 27-28 and the runbook appendices to design support and command processes.
    
-   Risk, fraud and collections: use Sections 14-18 and the KPI/control appendices.
    
-   Finance and treasury: use Sections 20-23, the control calendar and reconciliation/settlement requirements.
    
-   QA, security and assurance: use Sections 25, 29, 36 and 39 to build the production-readiness evidence pack.
    
-   Telco implementation teams: use Sections 13, 30-32 and the cutover checklist.
    
-   Every requirement is written to be traceable. The ID must appear in procedures, tests, backlog items and evidence where relevant.
    

Requirement class: MUST requirements are release-gating unless a formally approved waiver exists. SHOULD requirements are expected for the target release but may be phased through an approved decision. Release tags indicate the earliest intended release, not an exemption from architecture compatibility.

## Table of Contents

1.  Operations Executive Summary
    
2.  Purpose, Scope and Relationship to Volumes 1 and 2
    
3.  Production Operating Model and Governance
    
4.  Organisation, Roles and Segregation of Duties
    
5.  Service Catalogue, SLOs, SLAs and OLAs
    
6.  Command Centre, Monitoring and Shift Operations
    
7.  Incident Management
    
8.  Major Incident and Crisis Communications
    
9.  Problem Management and Continual Improvement
    
10.  Change, Release and Configuration Management
     
11.  Production Access and Privileged Operations
     
12.  Business Operations and Daily Control Cycle
     
13.  Telco Integration Operations
     
14.  Credit Decisioning and Scoring Operations
     
15.  Portfolio Risk and Automatic Guardrails
     
16.  Fraud Operations
     
17.  Advance and Fulfilment Operations
     
18.  Recovery, Delinquency and Collections Operations
     
19.  Customer Support, Complaints and Disputes
     
20.  Financial Operations and Ledger Controls
     
21.  Reconciliation and Settlement Operations
     
22.  Treasury, Funding and Liquidity Operations
     
23.  Credit Bureau and Regulatory Reporting Operations
     
24.  Privacy and Data Subject Rights Operations
     
25.  Security Operations and Cyber Incident Response
     
26.  Data Operations and Model Operations
     
27.  SRE, Platform and Capacity Operations
     
28.  Business Continuity, Disaster Recovery and Crisis Management
     
29.  Environments, QA and Operational Acceptance
     
30.  Telco Sandbox, Certification and Onboarding
     
31.  Migration, Dual Run and Cutover
     
32.  Pilot, Rollout, Hypercare and BAU Transition
     
33.  Training, Knowledge and Documentation
     
34.  Vendor and Third-Party Service Management
     
35.  Management Information, KPIs and Governance Cadence
     
36.  Audit, Control Testing and Evidence Retention
     
37.  Staffing, Support Coverage and Operating Calendar
     
38.  Delivery Workstreams, Backlog and Release Roadmap
     
39.  Go-Live Gate and Production Readiness
     
40.  Volume 3 Acceptance, Traceability and Maintenance
     

Appendix A - Target RACI

Appendix B - Severity, Priority and Escalation Matrix

Appendix C - Minimum Runbook Catalogue

Appendix D - Operating Control Calendar

Appendix E - Operational Readiness Checklist

Appendix F - Cutover and Rollback Checklist

Appendix G - KPI Dictionary

Appendix H - Major Incident Scenario Catalogue

Appendix I - Evidence and Traceability Matrix

Appendix J - Glossary

## 1\. Operations Executive Summary

Volume 3 defines the production operating system required to run the platform safely, prove that it remains financially correct, and move it from implementation into controlled business-as-usual service. The operating model assumes that the platform owns the credit relationship and financial truth while each telco owns subscriber rails, fulfilment and recovery execution.

-   Operate by explicit control calendars rather than informal heroics.
    
-   Preserve ledger and tenant integrity ahead of availability or sales volume.
    
-   Treat ambiguous telco outcomes as investigation states, never as retry opportunities.
    
-   Use risk, funding and conduct guardrails to suspend originations automatically while keeping recoveries and evidence ingestion available.
    
-   Make every production action attributable, authorised, reversible where possible and evidenced.
    

### Binding operational requirements

| ID | Class | Requirement | Acceptance evidence |
| --- | --- | --- | --- |
| OPE-001 | MUST; R1 | The production operating model shall cover business operations, telco operations, risk, fraud, collections, finance, treasury, customer support, compliance, security, data and SRE as one coordinated service. | Approved operating model and RACI. |
| OPE-002 | MUST; R1 | Every production day shall have opening, intraday, closing and next-day-readiness controls with named owners and evidence retention. | Daily control pack. |
| OPE-003 | MUST; R1 | The platform shall maintain service continuity priorities that place ledger integrity, recovery-event capture and tenant isolation above new-originations availability. | Approved continuity priority matrix. |
| OPE-004 | MUST; R1 | Every operational process shall reference the corresponding Volume 1 enterprise requirement and Volume 2 technical control where applicable. | Traceability matrix. |
| OPE-005 | MUST; R1 | Production operation shall be prohibited until the go-live gate in Section 39 is passed or a formally accepted risk waiver is approved. | Signed go-live decision. |
| OPE-006 | MUST; R1 | Operational controls shall be configuration-aware and shall record the telco, programme, product, environment and configuration version to which each control applies. | Control evidence sample. |
| OPE-007 | MUST; R1 | Originations shall be capable of suspension independently by telco, programme, product, channel, risk segment or funding pool without disabling recoveries or reconciliation. | Kill-switch exercise. |
| OPE-008 | MUST; R1 | The operating model shall support a single-telco Release 1 while preserving processes and role boundaries required for later multi-telco scale. | R1 operating readiness assessment. |

## 2\. Purpose, Scope and Relationship to Volumes 1 and 2

This volume is the run, assurance and delivery companion to the enterprise baseline and technical build specification. It is not a substitute for technical design; it defines the people, controls, procedures, acceptance evidence and governance required to operate that design.

-   Volume 1 determines business ownership, policy and non-configurable enterprise principles.
    
-   Volume 2 determines technical interfaces, data, services and engineering invariants.
    
-   Volume 3 determines operating procedures, governance, readiness and evidence.
    

### Binding operational requirements

| ID | Class | Requirement | Acceptance evidence |
| --- | --- | --- | --- |
| SCP-001 | MUST; R1 | A controlled document register shall identify the effective versions of all three volumes and all approved ADRs, policies, runbooks and commercial schedules. | Controlled-document register. |
| SCP-002 | MUST; R1 | A conflict between volumes shall be escalated to architecture and product governance; operations shall not invent a local interpretation for a financial or regulatory requirement. | Conflict-management procedure. |
| SCP-003 | MUST; R1 | Every runbook shall identify prerequisite system states, permissions, decision authority, rollback path, evidence and post-action validation. | Runbook template audit. |
| SCP-004 | MUST; R1 | Operational procedures shall distinguish mandatory controls, recommended practices and programme-specific configuration. | Procedure classification review. |
| SCP-005 | MUST; R1 | Operational acceptance criteria shall be converted into testable backlog items and linked to release evidence. | Release traceability report. |
| SCP-006 | MUST; R1 | Jurisdiction-specific operating procedures shall be maintained as annexes without weakening the common control baseline. | Jurisdiction annex review. |
| SCP-007 | MUST; R1 | The scope shall include outsourced and telco-executed activities where the platform retains accountability or requires evidence. | Third-party control map. |
| SCP-008 | MUST; R1 | The service shall maintain a documented list of decisions still open at each release gate and shall prevent unresolved critical decisions from being silently defaulted. | Decision register. |

## 3\. Production Operating Model and Governance

The target model combines functional ownership with an integrated production command structure. Domain teams retain accountability for risk, finance, compliance and technology, while the command centre coordinates real-time service health and incidents.

-   Executive Steering Committee: strategy, risk appetite and investment.
    
-   Product and Credit Committee: product, pricing, limits and portfolio decisions.
    
-   Service Review Board: SLA, incidents, capacity, telco and vendor performance.
    
-   Change Advisory Board: production changes and release risk.
    
-   Financial Control Committee: ledger, reconciliation, settlement and treasury.
    
-   Data and Model Governance: feature, score, model and data-quality decisions.
    

**Target Production Operating Model**

![image](https://static-us-img.skywork.ai/prod/nexus/1784239339/cropped_image_11_1784239339577508497.jpg)

_Figure 1 - Target production operating model._

### Binding operational requirements

| ID | Class | Requirement | Acceptance evidence |
| --- | --- | --- | --- |
| GOV-001 | MUST; R1 | A named accountable executive shall own the end-to-end service, including outsourced and telco-dependent outcomes. | Approved accountability letter. |
| GOV-002 | MUST; R1 | Each telco programme shall have an accountable programme owner, service owner, risk owner, finance owner, compliance owner and technical owner. | Programme RACI. |
| GOV-003 | MUST; R1 | Governance forums shall have documented charters, quorum, decision rights, meeting cadence, inputs, outputs and escalation paths. | Forum charters and minutes. |
| GOV-004 | MUST; R1 | Material decisions affecting pricing, limits, risk, funding, accounting or conduct shall require maker-checker approval and effective dating. | Approval evidence. |
| GOV-005 | MUST; R1 | Governance shall review cross-telco aggregation only where contract, privacy and legal basis permit it. | Data-governance decision. |
| GOV-006 | MUST; R1 | The Service Review Board shall review incidents, SLOs, backlog, capacity, complaints, reconciliation, settlement and vendor performance at least monthly. | Monthly service report. |
| GOV-007 | MUST; R1 | The Product and Credit Committee shall review approval rates, utilisation, roll rates, losses, trust-tier movement, anti-gaming outcomes and guardrail actions. | Credit committee pack. |
| GOV-008 | MUST; R1 | Governance decisions shall be recorded in a searchable decision register linked to affected configuration and release versions. | Decision-register sample. |
| GOV-009 | MUST; R1 | Emergency authority shall be explicitly limited and followed by retrospective approval within one business day. | Emergency-action audit. |
| GOV-010 | MUST; R1 | No individual shall have unilateral authority to configure, approve, deploy and financially reconcile the same material change. | Segregation-of-duties review. |

## 4\. Organisation, Roles and Segregation of Duties

Role design shall prevent concentration of power while enabling rapid incident response. Small-team Release 1 may combine functions, but incompatible permissions and approvals must remain separated.

-   Core functions: service management, telco operations, risk, fraud, collections, finance, treasury, support, compliance/privacy, security, data/model and SRE.
    
-   Independent assurance: internal control, audit and risk oversight.
    
-   Telco counterparts: channel, fulfilment, billing/recharge, network operations, finance and customer care.
    

### Binding operational requirements

| ID | Class | Requirement | Acceptance evidence |
| --- | --- | --- | --- |
| ORG-001 | MUST; R1 | A role catalogue shall define accountability, responsibilities, competencies, coverage and prohibited combinations for every production role. | Approved role catalogue. |
| ORG-002 | MUST; R1 | Production access shall be role-based, time-bound where elevated, and limited to the least privilege required. | Quarterly access review. |
| ORG-003 | MUST; R1 | Risk-policy makers shall not directly deploy their own approved policy into production without an independent release action. | Change evidence. |
| ORG-004 | MUST; R1 | Finance users who prepare settlements shall not be the sole approvers of the same settlement. | Settlement sample. |
| ORG-005 | MUST; R1 | Customer-support users shall not be able to alter ledger entries, limits, score histories or telco settlement records. | Entitlement test. |
| ORG-006 | MUST; R1 | SRE personnel may execute technical recovery but shall not perform undocumented financial adjustments. | Runbook and audit-log review. |
| ORG-007 | MUST; R1 | Emergency privileged access shall require a declared incident, justification, session recording and post-use review. | Break-glass exercise. |
| ORG-008 | MUST; R1 | Joiner, mover and leaver controls shall remove or update access within defined SLAs, with immediate removal for high-risk termination. | JML sample. |
| ORG-009 | MUST; R1 | A quarterly toxic-access and orphan-account review shall be performed across the platform, cloud, data and external partner systems. | Quarterly SoD report. |
| ORG-010 | MUST; R1 | Delegation during absence shall be documented and shall not bypass dual-control requirements. | Delegation record. |

## 5\. Service Catalogue, SLOs, SLAs and OLAs

The service catalogue converts architecture into measurable operational commitments. Customer-facing availability is only one dimension; financial truth, event completeness and reconciliation timeliness require separate objectives.

-   Originations and offer retrieval.
    
-   Fulfilment status and ambiguity resolution.
    
-   Recovery-event ingestion and allocation.
    
-   Ledger posting and financial close.
    
-   Telco and funder reconciliation and settlement.
    
-   Scoring and feature publication.
    
-   USSD/SMS/notification delivery.
    
-   Support, complaints, bureau and regulatory reporting.
    

### Binding operational requirements

| ID | Class | Requirement | Acceptance evidence |
| --- | --- | --- | --- |
| SVC-001 | MUST; R1 | Each service shall have a named owner, service description, consumers, dependencies, hours, SLOs, recovery priority and support route. | Published service catalogue. |
| SVC-002 | MUST; R1 | SLOs shall distinguish latency, availability, correctness, completeness, freshness and recovery objectives. | SLO register. |
| SVC-003 | MUST; R1 | Financial correctness and recovery-event completeness shall not be represented solely by generic API uptime. | Service report sample. |
| SVC-004 | MUST; R1 | Telco contracts shall define reciprocal SLAs for fulfilment, status enquiry, recharge/recovery events, files, reconciliation and incident response. | Executed SLA schedule. |
| SVC-005 | MUST; R1 | Internal OLAs shall define hand-offs between command centre, risk, finance, telco operations, security, data and engineering. | OLA matrix. |
| SVC-006 | MUST; R1 | SLO breaches shall generate service credits or remediation actions only according to approved commercial and governance rules. | Breach workflow. |
| SVC-007 | MUST; R1 | Service availability calculations shall exclude only formally approved maintenance and shall disclose degraded-mode periods. | Availability calculation audit. |
| SVC-008 | MUST; R1 | Release 1 shall target 99.9 percent core service availability and RTO no greater than 30 minutes unless an approved architecture decision sets a stronger target. | SLO approval and test. |
| SVC-009 | MUST; R1 | A separate recovery-ingestion durability objective shall require no acknowledged event loss. | Durability test evidence. |
| SVC-010 | MUST; R1 | Service levels shall be reported per telco and programme so one tenant cannot conceal another tenant's underperformance. | Tenant-level service dashboard. |

## 6\. Command Centre, Monitoring and Shift Operations

The command centre is the operational nervous system. It coordinates alerts, business controls and cross-domain escalation, but does not replace accountable domain ownership.

-   Initial Release 1 coverage may be 16x7 plus on-call if supported by risk and telco operating hours; recovery ingestion and critical alerts remain 24x7.
    
-   Every shift opens with health, funding, change, incident and backlog review and closes with a formal handover.
    

### Binding operational requirements

| ID | Class | Requirement | Acceptance evidence |
| --- | --- | --- | --- |
| CMD-001 | MUST; R1 | A central command function shall monitor technical health, transaction flows, financial controls, risk guardrails and telco dependencies. | Command dashboard. |
| CMD-002 | MUST; R1 | Shift handover shall include incidents, ambiguous fulfilments, backlog, changes, funding status, reconciliation breaks, security alerts and expected telco activity. | Handover sample. |
| CMD-003 | MUST; R1 | Every actionable alert shall have an owner, severity, threshold rationale, runbook and escalation timer. | Alert catalogue audit. |
| CMD-004 | MUST; R1 | Alert noise shall be measured and reduced; repeated unactionable alerts shall enter problem management. | Alert-quality report. |
| CMD-005 | MUST; R1 | Monitoring shall distinguish tenant-specific faults from shared-platform faults. | Fault-isolation exercise. |
| CMD-006 | MUST; R1 | The command centre shall maintain a real-time operational event log during major incidents. | Incident chronology. |
| CMD-007 | MUST; R1 | Shift staffing shall be based on observed demand, peak windows, telco campaign calendar and incident history. | Workforce plan. |
| CMD-008 | MUST; R1 | No shift may close while a Severity 1 incident lacks an active commander and next update time. | Shift closure control. |
| CMD-009 | MUST; R1 | The command centre shall verify that scheduled scoring, feed, reconciliation and settlement jobs completed or were formally deferred. | Daily completion report. |
| CMD-010 | MUST; R1 | Control dashboards shall display data freshness and last-success timestamps to prevent stale green indicators. | Dashboard evidence. |

## 7\. Incident Management

Incident management restores safe service while preserving evidence and preventing double credit, ledger corruption, privacy breach or cross-tenant impact. Financial ambiguity is itself an incident class.

-   Severity is based on customer, financial, regulatory, data, security, telco and reputational impact.
    
-   Containment may include selective origination suspension, adapter isolation, queue throttling or configuration rollback.
    

## Incident, Major Incident and Problem Lifecycle

![image](https://static-us-img.skywork.ai/prod/nexus/1784239336/cropped_image_6_1784239336886025531.jpg)

Severity determines command structure, update cadence, authority and evidence.

Financial ambiguity, cross-tenant exposure, privacy breach or mass over-approval automatically invokes major-incident governance.

_Figure 2 - Incident, major-incident and problem lifecycle._

### Binding operational requirements

| ID | Class | Requirement | Acceptance evidence |
| --- | --- | --- | --- |
| INC-001 | MUST; R1 | Every incident shall receive a unique identifier, severity, owner, start time, affected tenant/programme, impact statement and next update time. | Incident record sample. |
| INC-002 | MUST; R1 | Incident severity shall be reassessed as new information emerges and may not be downgraded solely to improve metrics. | Severity audit. |
| INC-003 | MUST; R1 | A timeout after a fulfilment instruction shall enter ambiguity-resolution procedures and shall never trigger blind retry. | Scenario test. |
| INC-004 | MUST; R1 | Incidents involving suspected financial imbalance, mass over-approval, privacy breach or cross-tenant access shall be treated as at least Severity 1 pending assessment. | Severity matrix. |
| INC-005 | MUST; R1 | Containment actions shall prioritise stopping additional harm while preserving recoveries, evidence and status enquiry where safe. | Runbook test. |
| INC-006 | MUST; R1 | Customer-impacting incidents shall have documented internal and external communication plans. | Communication evidence. |
| INC-007 | MUST; R1 | Incident tickets shall link alerts, logs, traces, affected transactions, changes, configuration versions and recovery actions. | Incident trace sample. |
| INC-008 | MUST; R1 | Closure shall require service validation, backlog assessment, financial reconciliation and customer/regulatory follow-up where applicable. | Closure checklist. |
| INC-009 | MUST; R1 | Incident metrics shall include detection, acknowledgement, containment, restoration and full-recovery times. | Incident KPI report. |
| INC-010 | MUST; R1 | Repeated incident categories shall automatically create or update a problem record. | Problem linkage sample. |
| INC-011 | MUST; R1 | Operational staff shall conduct scheduled incident simulations, including timeout-after-success, duplicate recovery, ledger imbalance and telco outage. | Simulation record. |
| INC-012 | MUST; R1 | An incident shall not be closed merely because the API recovered if financial or customer remediation remains outstanding. | Closure audit. |

## 8\. Major Incident and Crisis Communications

Major incidents require explicit command, stakeholder communication and decision authority. The major-incident process is designed for high-impact operational, financial, cyber, privacy and regulatory events.

-   Roles: Incident Commander, Technical Lead, Business/Risk Lead, Finance Lead, Communications Lead, Scribe and Executive Sponsor.
    
-   War rooms must have one authoritative timeline and decision log.
    

### Binding operational requirements

| ID | Class | Requirement | Acceptance evidence |
| --- | --- | --- | --- |
| MIC-001 | MUST; R1 | Severity 1 declaration shall appoint an Incident Commander and activate the major-incident bridge within the target SLA. | Major-incident exercise. |
| MIC-002 | MUST; R1 | The Incident Commander shall be independent of detailed technical execution and shall maintain priorities, decisions and communications. | Role-observation record. |
| MIC-003 | MUST; R1 | A single source of truth shall record facts, hypotheses, decisions, owners, deadlines and customer impact. | War-room log. |
| MIC-004 | MUST; R1 | Stakeholder updates shall state known facts, unknowns, impact, containment, next actions and next update time. | Update template. |
| MIC-005 | MUST; R1 | Telco, funder, bureau, regulator and customer communications shall follow pre-agreed responsibility and approval matrices. | Communication matrix. |
| MIC-006 | MUST; R1 | Public statements shall not expose personal data, security-sensitive detail or unverified root causes. | Communication review. |
| MIC-007 | MUST; R1 | Material financial incidents shall trigger immediate exposure, funding and settlement assessment. | Finance activation evidence. |
| MIC-008 | MUST; R1 | Cyber or privacy crises shall invoke legal, privacy and security breach-response procedures in parallel with service recovery. | Joint-response exercise. |
| MIC-009 | MUST; R1 | Executive escalation shall occur when impact or duration breaches defined thresholds even if technical recovery is progressing. | Escalation log. |
| MIC-010 | MUST; R1 | A major-incident review shall be completed within five business days unless a regulator or contract requires sooner. | Review pack. |
| MIC-011 | MUST; R1 | The review shall identify control, process, design, training and contractual actions, not merely an individual error. | RCA quality audit. |
| MIC-012 | MUST; R1 | Crisis simulations shall include participation by telco and key third parties at least annually. | Annual exercise evidence. |

## 9\. Problem Management and Continual Improvement

Problem management prevents recurrence and reduces operational toil. Root-cause work must cover technical, data, process, configuration, contractual and organisational factors.

-   Reactive problems arise from incidents; proactive problems arise from trends, near misses, audit findings and control failures.
    
-   Known errors require documented workarounds and expiry dates.
    

### Binding operational requirements

| ID | Class | Requirement | Acceptance evidence |
| --- | --- | --- | --- |
| PRB-001 | MUST; R1 | Problem records shall be created for recurring incidents, significant near misses, control failures and systemic weaknesses. | Problem register. |
| PRB-002 | MUST; R1 | Root-cause analysis shall use an approved method and distinguish root cause, contributing factors and detection gaps. | RCA sample. |
| PRB-003 | MUST; R1 | Problem actions shall have owners, due dates, risk ratings and verification criteria. | Action tracker. |
| PRB-004 | MUST; R1 | Temporary workarounds shall be documented, approved, monitored and removed after permanent remediation. | Known-error database. |
| PRB-005 | MUST; R1 | High-risk overdue problem actions shall be reported to the Service Review Board and risk governance. | Governance report. |
| PRB-006 | MUST; R1 | Post-implementation validation shall confirm that remediation reduced recurrence or control risk. | Effectiveness review. |
| PRB-007 | MUST; R1 | Operational toil, manual adjustment volume, repeated reconciliation breaks and alert noise shall feed the improvement backlog. | Improvement backlog. |
| PRB-008 | MUST; R1 | Problems caused by telco or vendor services shall be tracked through contractual service management, not closed as external dependencies. | Supplier problem record. |
| PRB-009 | MUST; R1 | RCA records shall preserve evidence needed for audit, regulatory or contractual review. | Evidence archive. |
| PRB-010 | MUST; R1 | Lessons learned shall update runbooks, tests, training and simulator fault scenarios. | Change linkage evidence. |

## 10\. Change, Release and Configuration Management

Production change is a major risk vector because a small configuration error can generate uncontrolled exposure at telco scale. The process therefore combines technical release controls with business, risk and financial approval.

-   Change classes: standard, normal, emergency and regulatory/time-critical.
    
-   Configuration changes are production changes even when no code is deployed.
    

### Binding operational requirements

| ID | Class | Requirement | Acceptance evidence |
| --- | --- | --- | --- |
| CHG-001 | MUST; R1 | Every production change shall identify scope, tenant/programme, risk, testing, rollback, monitoring, approvals and implementation window. | Change record. |
| CHG-002 | MUST; R1 | Changes affecting scoring, limits, fees, disclosures, funding, posting templates or recovery allocation shall require domain approval in addition to technical approval. | Approval audit. |
| CHG-003 | MUST; R1 | Configuration shall pass schema validation, simulation, historical replay and maker-checker approval before activation. | Configuration evidence. |
| CHG-004 | MUST; R1 | Material risk changes shall support canary scope and automatic rollback thresholds. | Canary test. |
| CHG-005 | MUST; R1 | Emergency changes shall be limited to restoring safe service or meeting unavoidable obligations and shall receive retrospective CAB review. | Emergency-change sample. |
| CHG-006 | MUST; R1 | No production change shall be implemented during a declared freeze without authorised exception. | Freeze calendar and exception. |
| CHG-007 | MUST; R1 | Release packages shall be immutable, signed, traceable to source and reproducibly deployable. | Release provenance. |
| CHG-008 | MUST; R1 | Database and event-schema changes shall be backward compatible or have an approved migration and rollback plan. | Compatibility test. |
| CHG-009 | MUST; R1 | Posting-template changes shall prove debit equals credit per currency at validation and posting time. | Ledger-template test. |
| CHG-010 | MUST; R1 | Change success shall include business and financial validation, not only technical deployment completion. | Post-change validation. |
| CHG-011 | MUST; R1 | Failed changes and near misses shall enter problem management and release-retrospective review. | Release review. |
| CHG-012 | MUST; R1 | Telco-dependent changes shall be coordinated through a joint change calendar and certification path. | Joint change evidence. |

## 11\. Production Access and Privileged Operations

Production access is exceptional, observable and controlled. Routine business operations shall be performed through governed portals and APIs rather than direct database or infrastructure access.

-   Human access uses named identities and MFA.
    
-   Service identities are non-interactive, rotated and scoped.
    
-   Direct ledger mutation is prohibited.
    

### Binding operational requirements

| ID | Class | Requirement | Acceptance evidence |
| --- | --- | --- | --- |
| PAM-001 | MUST; R1 | All production human access shall use named identities, MFA and central identity governance. | Access configuration. |
| PAM-002 | MUST; R1 | Privileged access shall be just-in-time or time-limited and require approved purpose. | PAM report. |
| PAM-003 | MUST; R1 | Privileged sessions shall be logged and, for high-risk systems, recorded. | Session evidence. |
| PAM-004 | MUST; R1 | Direct production database writes shall be prohibited except through approved emergency tooling that creates auditable domain events. | Technical control test. |
| PAM-005 | MUST; R1 | Ledger corrections shall occur only through authorised compensating or reversal entries. | Ledger audit. |
| PAM-006 | MUST; R1 | Production data extraction shall require purpose, scope, approval, minimisation and secure delivery controls. | Extraction record. |
| PAM-007 | MUST; R1 | Secrets shall not be disclosed in tickets, chat, logs, runbooks or source code. | Secret-scanning report. |
| PAM-008 | MUST; R1 | Access reviews shall occur at least quarterly and after material organisational change. | Access-review evidence. |
| PAM-009 | MUST; R1 | Break-glass access shall alert security and service management immediately. | Break-glass exercise. |
| PAM-010 | MUST; R1 | Administrative actions shall capture before/after state, actor, reason, approval and correlation identifier. | Audit-log sample. |

## 12\. Business Operations and Daily Control Cycle

Business operations maintain the daily health of products, offers, advances, recoveries and customer obligations. The control cycle aligns scoring windows, telco feed availability, live originations, reconciliation and close.

-   Opening controls validate data freshness, configuration, funding, limits, integrations and prior-day exceptions.
    
-   Intraday controls monitor volume, approval, fulfilment, recovery, exposure, complaints and fraud.
    
-   Closing controls validate ledger, reconciliation, settlement and unresolved ambiguity.
    

## Illustrative 24-Hour Operational Control Cycle

![image](https://static-us-img.skywork.ai/prod/nexus/1784239339/cropped_image_8_1784239339364258028.jpg)

## Non-negotiable daily controls:

Opening balance and funding availability | stale score/feature checks | fulfilment ambiguity queue | ledger balance | recovery completeness | reconciliation breaks | settlement exposure | portfolio guardrails | incident/change calendar

_Figure 3 - Illustrative 24-hour operational control cycle._

### Binding operational requirements

| ID | Class | Requirement | Acceptance evidence |
| --- | --- | --- | --- |
| BOP-001 | MUST; R1 | An opening checklist shall confirm all required feeds, scoring outputs, configuration versions, funding pools, integrations and control dashboards are healthy. | Opening checklist sample. |
| BOP-002 | MUST; R1 | Stale or incomplete features shall prevent affected subscribers from receiving newly calculated offers unless a configured safe fallback is approved. | Stale-data test. |
| BOP-003 | MUST; R1 | Intraday operations shall monitor originations, approval rate, average limit, fulfilment success, ambiguity, recovery, exposure and guardrails per programme. | Intraday dashboard. |
| BOP-004 | MUST; R1 | Unexpected deviation from baseline shall trigger investigation or automatic origination suspension according to thresholds. | Guardrail event. |
| BOP-005 | MUST; R1 | Business operations shall maintain queues for fulfilment ambiguity, failed notifications, recovery exceptions, complaints and manual review. | Queue dashboard. |
| BOP-006 | MUST; R1 | A day-end control shall prove ledger balance, event completeness, reconciliation status and funding/exposure positions. | Day-end control pack. |
| BOP-007 | MUST; R1 | Unresolved critical exceptions shall have named owners, aged status, next action and escalation before day closure. | Exception register. |
| BOP-008 | MUST; R1 | Daily controls shall be independently reviewed or maker-checked for high-risk programmes. | Control approval. |
| BOP-009 | MUST; R1 | Programme launch, promotion and campaign calendars shall be integrated into capacity and risk planning. | Campaign readiness record. |
| BOP-010 | MUST; R1 | Operational reports shall differentiate event time, processing time and business date. | Report sample. |
| BOP-011 | MUST; R1 | Manual overrides shall be exceptional, reason-coded, time-limited and included in daily review. | Override report. |
| BOP-012 | MUST; R1 | Control evidence shall be retained according to the approved evidence schedule and be readily retrievable. | Evidence retrieval test. |

## 13\. Telco Integration Operations

Telco operations manage the live relationship with each operator’s channels, subscriber feeds, fulfilment, status enquiry, recharge/recovery, messaging, reconciliation and change teams.

-   Each telco has its own adapter health, queues, credentials, rate limits, circuit breakers and escalation contacts.
    
-   One telco incident must not degrade other tenants.
    

### Binding operational requirements

| ID | Class | Requirement | Acceptance evidence |
| --- | --- | --- | --- |
| TOP-001 | MUST; R1 | A telco operational profile shall define endpoints, certificates, contacts, support hours, SLAs, files, cut-offs, rate limits and escalation paths. | Telco profile. |
| TOP-002 | MUST; R1 | Adapter health shall be monitored independently by telco, interface and environment. | Adapter dashboard. |
| TOP-003 | MUST; R1 | Authentication or certificate expiry shall generate advance warning and named renewal action. | Certificate report. |
| TOP-004 | MUST; R1 | Telco timeouts after submission shall enter status-enquiry and reconciliation workflows rather than retry. | Timeout scenario test. |
| TOP-005 | MUST; R1 | Inbound files and events shall be checked for completeness, schema, sequence, duplicates, timeliness and tenant authenticity. | Feed control evidence. |
| TOP-006 | MUST; R1 | Replay or backfill shall use controlled windows, idempotency and reconciliation validation. | Replay exercise. |
| TOP-007 | MUST; R1 | Telco planned maintenance shall be represented in the shared change calendar and customer/channel plan. | Maintenance record. |
| TOP-008 | MUST; R1 | A telco outage shall open its own incident and circuit-breaker scope without suppressing other telcos. | Isolation exercise. |
| TOP-009 | MUST; R1 | Telco operations shall reconcile message counts and acknowledgements at interface level before financial reconciliation. | Interface reconciliation. |
| TOP-010 | MUST; R1 | Material telco SLA breaches shall trigger service review, remediation and commercial processes. | SLA breach record. |
| TOP-011 | MUST; R1 | Operational contact details shall be tested at least quarterly. | Contact-tree test. |
| TOP-012 | MUST; R1 | Telco-specific workarounds shall be documented in the adapter/runbook layer and shall not contaminate the canonical core model. | Architecture review. |

## 14\. Credit Decisioning and Scoring Operations

Credit operations govern scheduled feature computation, score publication, real-time decision overlays and model/configuration performance. The focus is controlled use, explainability and drift management.

-   Batch scoring is preferred for population-scale feature computation.
    
-   Real-time overlays apply exposure, fraud, funding, status and conduct gates.
    
-   Every decision retains exact versions and reason codes.
    

### Binding operational requirements

| ID | Class | Requirement | Acceptance evidence |
| --- | --- | --- | --- |
| CRO-001 | MUST; R1 | Each scoring run shall record input snapshot, feature version, model/rule version, population, exclusions, failures and publication status. | Scoring-run record. |
| CRO-002 | MUST; R1 | Score publication shall be atomic or versioned so channels do not mix partially published limits. | Publication test. |
| CRO-003 | MUST; R1 | Feature and score freshness thresholds shall be monitored by telco and programme. | Freshness dashboard. |
| CRO-004 | MUST; R1 | A one-tier maximum upward movement per scoring cycle shall be the default configurable control. | Policy configuration. |
| CRO-005 | MUST; R1 | Recharge spikes shall be discounted or capped using approved robust statistics and historical baselines. | Model validation report. |
| CRO-006 | MUST; R1 | Model and rule changes shall undergo validation, replay, bias/fairness assessment where applicable and approval. | Model-change pack. |
| CRO-007 | MUST; R1 | Decision reason codes shall be human-interpretable for support, complaint and regulatory use. | Decision explanation sample. |
| CRO-008 | MUST; R1 | Score distribution, approval, utilisation, performance and drift shall be monitored against expected ranges. | Model monitoring report. |
| CRO-009 | MUST; R1 | Unexpected population shifts shall trigger investigation and, where thresholds are breached, publication hold or origination guardrail. | Drift alert exercise. |
| CRO-010 | MUST; R1 | Champion/challenger tests shall use controlled cohorts and shall not bypass exposure or conduct limits. | Experiment approval. |
| CRO-011 | MUST; R1 | Historical replay shall reproduce the decision using retained data and configuration or explain any non-deterministic dependency. | Replay evidence. |
| CRO-012 | MUST; R1 | Model rollback shall preserve previous approved versions and decision traceability. | Rollback exercise. |

## 15\. Portfolio Risk and Automatic Guardrails

Portfolio operations protect capital and customers from rapid adverse change. Guardrails must operate at telco, programme, product, segment, tier, channel and funding-pool levels.

-   Core metrics: approval, utilisation, exposure, repayment, delinquency, roll rates, vintage loss, concentration, fraud and complaints.
    
-   Guardrails can warn, restrict tiers, reduce limits or suspend originations.
    

### Binding operational requirements

| ID | Class | Requirement | Acceptance evidence |
| --- | --- | --- | --- |
| RSK-001 | MUST; R1 | Risk appetite shall define exposure, loss, delinquency, concentration and conduct limits per programme. | Approved risk appetite. |
| RSK-002 | MUST; R1 | Automatic guardrails shall monitor approval rate, average limit, exposure velocity, early delinquency, fraud and configuration deviation. | Guardrail configuration. |
| RSK-003 | MUST; R1 | Guardrail actions shall be independently scoped by telco, programme, product and segment. | Scope test. |
| RSK-004 | MUST; R1 | A mass over-approval indicator shall suspend originations within the defined response time. | Simulation evidence. |
| RSK-005 | MUST; R1 | Risk staff shall be able to reduce limits or suspend products without code deployment while preserving maker-checker controls. | Portal test. |
| RSK-006 | MUST; R1 | Re-arming a tripped guardrail shall require root-cause assessment, evidence and approved authority. | Re-arm record. |
| RSK-007 | MUST; R1 | Portfolio reports shall distinguish booked exposure, utilised exposure, recoverable balance, written-off balance and recoveries after write-off. | Portfolio report. |
| RSK-008 | MUST; R1 | Vintage and cohort analysis shall be available by score band, tier, telco, product, channel and configuration version. | Analytics evidence. |
| RSK-009 | MUST; R1 | Concentration limits shall cover telco, funding source, subscriber segment, geography where lawful, and product. | Concentration report. |
| RSK-010 | MUST; R1 | Risk actions shall be included in the operational event log and daily control pack. | Control evidence. |
| RSK-011 | MUST; R1 | Cross-telco negative visibility shall be used only according to the approved legal and contractual design decision. | Privacy/legal control. |
| RSK-012 | MUST; R1 | Portfolio overrides shall expire automatically unless reapproved. | Override-expiry test. |

## 16\. Fraud Operations

Fraud operations detect and manage subscriber, account, device, recharge, channel, insider and integration abuse without weakening tenant isolation or lawful data use.

-   Signals include SIM swap, device/identity changes, abnormal recharge, request velocity, repeated ambiguity, collusion and support abuse.
    
-   Actions include step-up, decline, temporary hold, watchlist and investigation.
    

### Binding operational requirements

| ID | Class | Requirement | Acceptance evidence |
| --- | --- | --- | --- |
| FRO-001 | MUST; R1 | Fraud rules and models shall be versioned, explainable and configurable per telco programme. | Fraud-policy evidence. |
| FRO-002 | MUST; R1 | High-risk SIM-swap or subscriber-status changes shall apply immediate real-time overlays regardless of last batch score. | Scenario test. |
| FRO-003 | MUST; R1 | Velocity limits shall cover requests, failed confirmations, advances, devices, channels and support-assisted actions. | Velocity test. |
| FRO-004 | MUST; R1 | Fraud alerts shall include related transactions, subscriber history, device/network signals where lawful and recommended action. | Case sample. |
| FRO-005 | MUST; R1 | Fraud cases shall maintain evidence, actions, disposition, loss and linked incident/problem records. | Case audit. |
| FRO-006 | MUST; R1 | Watchlists and blocklists shall have source, reason, expiry/review date and authority. | Watchlist review. |
| FRO-007 | MUST; R1 | Insider and privileged-user activity shall be monitored for anomalous access or adjustments. | Insider-monitoring report. |
| FRO-008 | MUST; R1 | Fraud controls shall be tested against false-positive and false-negative outcomes and customer impact. | Control-effectiveness report. |
| FRO-009 | MUST; R1 | Confirmed fraud shall feed model/rule improvement and telco escalation where applicable. | Feedback-loop evidence. |
| FRO-010 | MUST; R1 | Fraud data sharing across telcos shall occur only under approved legal basis and contract. | Data-sharing audit. |
| FRO-011 | MUST; R1 | Fraud-related customer communications shall avoid disclosing detection logic. | Template review. |
| FRO-012 | MUST; R1 | Material fraud events shall trigger portfolio and funding impact assessment. | Incident record. |

## 17\. Advance and Fulfilment Operations

Advance operations control request, approval, exposure reservation, telco fulfilment and activation. The operational focus is idempotency, ambiguity, ageing and customer confirmation.

-   Advance begins at REQUESTED; offer is a separate entity.
    
-   FULFILMENT\_UNKNOWN is a controlled state requiring enquiry/reconciliation.
    

### Binding operational requirements

| ID | Class | Requirement | Acceptance evidence |
| --- | --- | --- | --- |
| AFO-001 | MUST; R1 | Advance-state queues shall be monitored for ageing, stuck states and invalid transitions. | State dashboard. |
| AFO-002 | MUST; R1 | REQUESTED or APPROVED records exceeding thresholds shall be investigated before exposure or customer impact accumulates. | Ageing report. |
| AFO-003 | MUST; R1 | Exposure reservation and release shall be reconciled to advance state and funding pools. | Exposure reconciliation. |
| AFO-004 | MUST; R1 | FULFILMENT\_UNKNOWN cases shall be resolved by telco status enquiry, callback or reconciliation before repeat instruction. | Ambiguity case sample. |
| AFO-005 | MUST; R1 | A configurable maximum concurrent-advance policy shall default to one active advance per subscriber account. | Policy test. |
| AFO-006 | MUST; R1 | Partial or mismatched fulfilment shall be quarantined for investigation and shall not be silently normalised. | Scenario test. |
| AFO-007 | MUST; R1 | Customer confirmation shall be sent through the approved channel even if the originating USSD session has ended. | Notification evidence. |
| AFO-008 | MUST; R1 | Reversal and cancellation shall follow state-specific rules and balanced ledger treatment. | Reversal test. |
| AFO-009 | MUST; R1 | Stuck fulfilment backlog shall trigger telco escalation and service-impact assessment. | Escalation record. |
| AFO-010 | MUST; R1 | Operations shall be able to search the full journey using telco, MSISDN token, offer, advance, fulfilment and correlation identifiers. | Portal test. |
| AFO-011 | MUST; R1 | Manual state correction shall be prohibited; authorised remediation shall create explicit events and evidence. | Audit test. |
| AFO-012 | MUST; R1 | Advance operations shall monitor duplicate request and idempotency-key collision rates. | Idempotency dashboard. |

## 18\. Recovery, Delinquency and Collections Operations

Collections for airtime/data advance are primarily telco-executed recharge garnishment, supported by proportionate notifications and conduct controls. The platform owns allocation, ageing, evidence and complaint handling.

-   Default policy: no external field collections or aggressive recovery for micro airtime advances unless specifically approved.
    
-   Delinquency buckets and write-off rules are configurable and versioned.
    

### Binding operational requirements

| ID | Class | Requirement | Acceptance evidence |
| --- | --- | --- | --- |
| COL-001 | MUST; R1 | Every recovery event shall be idempotently allocated according to the approved waterfall. | Recovery allocation test. |
| COL-002 | MUST; R1 | Partial recovery shall reduce outstanding exposure and preserve the remaining balance accurately. | Ledger evidence. |
| COL-003 | MUST; R1 | Recovery exceeding outstanding balance shall be quarantined and handled by approved over-recovery rules. | Scenario test. |
| COL-004 | MUST; R1 | Reversal-before-original and out-of-order recovery events shall be retained and resolved without corrupting balances. | Fault-injection test. |
| COL-005 | MUST; R1 | Delinquency ageing shall use configured business-date rules and be reproducible. | Ageing reconciliation. |
| COL-006 | MUST; R1 | Dunning cadence, quiet hours, language and content shall be configured and conduct-reviewed. | Notification policy. |
| COL-007 | MUST; R1 | Customer self-exclusion and marketing opt-out shall not prevent lawful repayment notices but shall suppress promotional offers. | Preference test. |
| COL-008 | MUST; R1 | Write-off shall create approved accounting events without erasing the legal or operational history. | Write-off sample. |
| COL-009 | MUST; R1 | Recovery after write-off shall be recorded separately and reported accurately. | Post-write-off recovery test. |
| COL-010 | MUST; R1 | Collections cases and notifications shall be available to support and complaints teams. | Case-view test. |
| COL-011 | MUST; R1 | Harassment, public shaming, contact-list access and unauthorised third-party contact shall be prohibited. | Conduct audit. |
| COL-012 | MUST; R1 | Collections performance shall be reported by vintage, tier, telco, product, score band and configuration version. | Collections dashboard. |

## 19\. Customer Support, Complaints and Disputes

Customer operations provide explainable, evidence-based support across eligibility, offer, fulfilment, repayment, notifications, bureau data, privacy and complaints. Support must not alter financial truth directly.

-   Channels may include telco care, platform service desk, web/self-service and regulatory escalation.
    
-   Responsibility and hand-off must be contractually clear.
    

### Binding operational requirements

| ID | Class | Requirement | Acceptance evidence |
| --- | --- | --- | --- |
| SUP-001 | MUST; R1 | A customer-support responsibility matrix shall define whether the telco, platform or both receive each complaint type. | Support RACI. |
| SUP-002 | MUST; R1 | Support users shall see a masked, chronological customer journey including decisions, disclosures, fulfilment, recoveries and notifications. | Support portal test. |
| SUP-003 | MUST; R1 | Support shall provide reason-coded explanations without exposing proprietary model detail or sensitive fraud logic. | Response template. |
| SUP-004 | MUST; R1 | Complaints shall have category, severity, owner, SLA, regulatory clock, evidence and resolution. | Complaint sample. |
| SUP-005 | MUST; R1 | Financial disputes shall be investigated against ledger and telco evidence before any adjustment. | Dispute case. |
| SUP-006 | MUST; R1 | Goodwill or remediation credits shall require policy authority and balanced accounting. | Remediation approval. |
| SUP-007 | MUST; R1 | Complaint trends shall feed product, risk, collections, telco and problem management. | Trend report. |
| SUP-008 | MUST; R1 | Customers shall receive acknowledgement and resolution communications in the approved language/channel. | Communication sample. |
| SUP-009 | MUST; R1 | Escalated complaints shall preserve the complete disclosure and consent version used for the advance. | Evidence retrieval. |
| SUP-010 | MUST; R1 | Support shall be unable to disclose one telco programme's data to another telco's staff. | Tenant-isolation test. |
| SUP-011 | MUST; R1 | Complaint registers shall support FCCPC or other regulatory export formats configured for the jurisdiction. | Export test. |
| SUP-012 | MUST; R1 | Unresolved complaints shall be aged and escalated before SLA breach. | Aged-complaint report. |

## 20\. Financial Operations and Ledger Controls

Finance operations prove that the append-only ledger remains balanced and that operational states reconcile to financial positions. The platform ledger is the operational financial source of truth; the corporate GL receives controlled summaries or entries.

-   Daily proof includes debit=credit, currency balance, orphan detection, state-to-ledger comparison and control account reconciliation.
    
-   Corrections are compensating entries, never edits.
    

### Binding operational requirements

| ID | Class | Requirement | Acceptance evidence |
| --- | --- | --- | --- |
| FOP-001 | MUST; R1 | The ledger shall be balanced by transaction and currency at posting time and as part of daily close. | Ledger-balance report. |
| FOP-002 | MUST; R1 | Finance shall reconcile advance, exposure, fee, recovery, reversal, write-off, settlement and funding events to ledger postings. | Daily finance control. |
| FOP-003 | MUST; R1 | No user shall edit or delete posted ledger entries. | Technical and access test. |
| FOP-004 | MUST; R1 | Financial adjustments shall require reason, source evidence, maker-checker approval and linked compensating entries. | Adjustment sample. |
| FOP-005 | MUST; R1 | Control accounts shall be defined for telco receivable/payable, funding, fees, taxes, suspense and settlement. | Chart-of-accounts mapping. |
| FOP-006 | MUST; R1 | Suspense balances shall have owners, ageing and clearance targets. | Suspense report. |
| FOP-007 | MUST; R1 | Daily close shall identify late events and support controlled reopening or next-period treatment. | Close procedure. |
| FOP-008 | MUST; R1 | Financial reports shall state business date, event cut-off, currency, telco, programme and configuration context. | Report sample. |
| FOP-009 | MUST; R1 | Posting templates shall be versioned, approved and tested against representative and edge-case events. | Posting-template test pack. |
| FOP-010 | MUST; R1 | Finance shall review unusual manual actions, reversals, write-offs and recoveries after write-off. | Exception report. |
| FOP-011 | MUST; R1 | Corporate GL integration shall be reconciled to platform totals and acknowledge accepted/rejected records. | GL interface reconciliation. |
| FOP-012 | MUST; R1 | Period-end close shall preserve immutable evidence and sign-off. | Month-end close pack. |

## 21\. Reconciliation and Settlement Operations

Reconciliation compares platform records with telco, funder, bank and GL records. Settlement converts agreed financial positions into controlled payment instructions and confirmations.

-   Layers: interface, transaction, ledger, cash, settlement and GL reconciliation.
    
-   Breaks are classified, aged, assigned and evidenced.
    

Operational Reconciliation and Settlement Control Model

![image](https://static-us-img.skywork.ai/prod/nexus/1784239339/cropped_image_6_1784239339469009009.jpg)

_Figure 4 - Reconciliation and settlement control model._

### Binding operational requirements

| ID | Class | Requirement | Acceptance evidence |
| --- | --- | --- | --- |
| REC-001 | MUST; R1 | Reconciliation shall be performed separately per telco, programme, product, currency and business date. | Reconciliation output. |
| REC-002 | MUST; R1 | Matching shall use stable identifiers plus amount, status and time-window rules; fuzzy matching shall be controlled and reviewable. | Matching-rule review. |
| REC-003 | MUST; R1 | Break categories shall include missing platform, missing telco, amount mismatch, status mismatch, duplicate, reversal, timing and unknown. | Break taxonomy. |
| REC-004 | MUST; R1 | Reconciliation rules shall be versioned and maker-checker approved. | Configuration evidence. |
| REC-005 | MUST; R1 | Automatic matching shall retain the evidence and rule version that produced the result. | Match audit sample. |
| REC-006 | MUST; R1 | Unmatched financial items shall enter ageing, escalation and suspense processes. | Aged-break report. |
| REC-007 | MUST; R1 | Settlement statements shall show principal, fees, revenue share, tax, recoveries, reversals, adjustments and net payable/receivable. | Settlement statement. |
| REC-008 | MUST; R1 | Settlement preparation and approval shall be segregated. | Approval audit. |
| REC-009 | MUST; R1 | Cash settlement shall be reconciled to bank confirmation and platform settlement status. | Bank reconciliation. |
| REC-010 | MUST; R1 | Disputed settlement items shall be separately identified and shall not obscure agreed amounts. | Dispute statement. |
| REC-011 | MUST; R1 | Late telco files shall not cause silent close; provisional and final statuses shall be explicit. | Late-file scenario. |
| REC-012 | MUST; R1 | Settlement completion shall be reported to treasury, finance and programme governance. | Settlement sign-off. |

## 22\. Treasury, Funding and Liquidity Operations

Treasury operations ensure that originations remain within committed funding, liquidity and counterparty limits. Funding constraints must act before exposure exceeds available capital.

-   Funding models may be own balance sheet, telco inventory, third-party funder or hybrid.
    
-   Pools are configurable by programme, product, currency and funder.
    

### Binding operational requirements

| ID | Class | Requirement | Acceptance evidence |
| --- | --- | --- | --- |
| TRY-001 | MUST; R1 | Each funding pool shall have committed amount, available balance, utilisation, limits, effective dates and owner. | Funding-pool register. |
| TRY-002 | MUST; R1 | Originations shall reserve funding atomically before fulfilment submission. | Reservation test. |
| TRY-003 | MUST; R1 | Funding exhaustion or limit breach shall suspend affected originations automatically. | Funding-guardrail test. |
| TRY-004 | MUST; R1 | Recoveries, settlements, drawdowns, replenishments and funding costs shall update the pool through auditable events. | Funding ledger. |
| TRY-005 | MUST; R1 | Treasury shall reconcile platform funding positions to bank/funder statements. | Treasury reconciliation. |
| TRY-006 | MUST; R1 | Liquidity forecasts shall incorporate expected originations, recovery curves, settlements, campaigns and stress scenarios. | Liquidity forecast. |
| TRY-007 | MUST; R1 | Concentration and counterparty limits shall be monitored by funder, bank, telco and currency. | Limit dashboard. |
| TRY-008 | MUST; R1 | Funding cost accrual shall be supported where applicable and reconciled to contracts. | Accrual sample. |
| TRY-009 | MUST; R1 | Funding overrides shall be time-limited and require treasury and risk approval. | Override record. |
| TRY-010 | MUST; R1 | A funding incident shall not disable recovery ingestion or ledger posting. | Degraded-mode test. |
| TRY-011 | MUST; R1 | Treasury shall maintain contingency funding and settlement-failure procedures. | Contingency plan. |
| TRY-012 | MUST; R1 | Funder statements shall include agreed exposure, utilisation, recoveries, losses, fees and exceptions. | Funder statement. |

## 23\. Credit Bureau and Regulatory Reporting Operations

Bureau and regulatory operations turn the retained credit, disclosure, complaint, conduct and financial evidence into controlled submissions. Capability is Release 1 even where activation is initially dormant.

-   Targets may include CRC, FirstCentral and CreditRegistry through configurable mappings.
    
-   Submission depends on legal, licence and programme decisions.
    

### Binding operational requirements

| ID | Class | Requirement | Acceptance evidence |
| --- | --- | --- | --- |
| REG-001 | MUST; R1 | A bureau reporting calendar shall define data cut-off, mapping, validation, approval, submission and acknowledgement. | Reporting calendar. |
| REG-002 | MUST; R1 | Bureau extracts shall be reconciled to the platform ledger and subscriber/advance records. | Extract reconciliation. |
| REG-003 | MUST; R1 | Rejected bureau records shall enter a controlled correction and resubmission queue. | Rejection workflow. |
| REG-004 | MUST; R1 | Bureau corrections and disputes shall preserve original submission, reason and resolution evidence. | Correction sample. |
| REG-005 | MUST; R1 | Regulatory submissions shall use approved templates, legal entity identity, licence references and reporting period. | Submission sample. |
| REG-006 | MUST; R1 | Disclosure and consent evidence shall be retrievable per advance and linked to the effective terms/version. | Evidence retrieval. |
| REG-007 | MUST; R1 | Complaint, conduct, delinquency and portfolio reports shall support jurisdiction-specific export formats. | Export test. |
| REG-008 | MUST; R1 | Submission access and approval shall be segregated and logged. | Access audit. |
| REG-009 | MUST; R1 | Missed or rejected regulatory submissions shall trigger incident and compliance escalation. | Escalation record. |
| REG-010 | MUST; R1 | Regulatory watch items shall be tracked with owner, impact assessment and implementation action. | Regulatory watch register. |
| REG-011 | MUST; R1 | Nigeria data residency and approved cross-border controls shall be validated as part of operational readiness. | Residency evidence. |
| REG-012 | MUST; R1 | Tax reporting inputs shall identify VAT, withholding or other configured tax lines without assuming unapproved legal treatment. | Tax extract sample. |

## 24\. Privacy and Data Subject Rights Operations

Privacy operations control lawful data use, retention, access, correction, deletion/limitation where permitted, cross-border transfers and automated-decision inquiries.

-   The platform minimises identity data and prefers verification/status flags over raw NIN values.
    
-   Rights are balanced with financial, legal and evidential retention obligations.
    

### Binding operational requirements

| ID | Class | Requirement | Acceptance evidence |
| --- | --- | --- | --- |
| PRI-001 | MUST; R1 | A data inventory shall identify purpose, lawful basis, source, owner, classification, residency, retention and recipients for each data domain. | Data inventory. |
| PRI-002 | MUST; R1 | NIN and other high-risk identity values shall not be ingested where a verified-status flag satisfies the requirement. | Data minimisation audit. |
| PRI-003 | MUST; R1 | Data-subject requests shall be identity-verified, tracked, SLA-managed and evidenced. | DSR case sample. |
| PRI-004 | MUST; R1 | Correction requests shall preserve audit history and shall not rewrite financial ledger truth. | Correction procedure. |
| PRI-005 | MUST; R1 | Deletion or restriction shall respect statutory, contractual and fraud/financial retention obligations. | Retention decision sample. |
| PRI-006 | MUST; R1 | Cross-border transfer shall require approved legal mechanism, destination and security controls. | Transfer register. |
| PRI-007 | MUST; R1 | Privacy incidents shall invoke breach assessment, containment, notification and evidence processes. | Privacy-incident exercise. |
| PRI-008 | MUST; R1 | Automated-decision inquiries shall provide meaningful reason information and escalation route. | Decision-rights response. |
| PRI-009 | MUST; R1 | Marketing opt-out, self-exclusion and channel preferences shall be applied consistently across platform and telco interfaces. | Preference propagation test. |
| PRI-010 | MUST; R1 | Data extracts for support, audit or analytics shall be masked or tokenised according to role and purpose. | Masking test. |
| PRI-011 | MUST; R1 | Retention jobs shall be monitored and exceptions reviewed. | Retention execution report. |
| PRI-012 | MUST; R1 | ADPIA and privacy operating controls shall be reviewed before new high-risk data or cross-telco analytics are introduced. | DPIA approval. |

## 25\. Security Operations and Cyber Incident Response

Security operations monitor identities, infrastructure, applications, data, endpoints and partner integrations. Cyber response must coordinate with service, privacy, legal, telco and financial teams.

-   Core capabilities: SIEM, vulnerability management, threat detection, secrets/certificate management, security testing and incident response.

### Binding operational requirements

| ID | Class | Requirement | Acceptance evidence |
| --- | --- | --- | --- |
| SEC-001 | MUST; R1 | Security monitoring shall cover authentication, privilege, configuration, data access, API abuse, malware, exfiltration and tenant-boundary events. | SIEM use-case catalogue. |
| SEC-002 | MUST; R1 | High-confidence cross-tenant or ledger-tampering alerts shall be Severity 1. | Alert classification. |
| SEC-003 | MUST; R1 | Vulnerabilities shall be prioritised by exploitability, exposure, asset criticality and business impact. | Vulnerability report. |
| SEC-004 | MUST; R1 | Critical internet-facing vulnerabilities shall meet defined remediation SLAs or receive approved compensating control. | Remediation evidence. |
| SEC-005 | MUST; R1 | Secrets and certificates shall have inventory, owner, rotation and expiry monitoring. | Secrets inventory. |
| SEC-006 | MUST; R1 | Cyber incidents shall preserve forensic evidence and chain of custody. | Forensic procedure. |
| SEC-007 | MUST; R1 | Security response shall have authority to isolate components while coordinating financial-safe-state requirements. | Joint runbook. |
| SEC-008 | MUST; R1 | Third-party and telco security incidents shall be governed by notification and cooperation clauses. | Contract/control evidence. |
| SEC-009 | MUST; R1 | Security testing shall include SAST, SCA, DAST, infrastructure scanning, penetration testing and tenant-isolation tests. | Security test pack. |
| SEC-010 | MUST; R1 | Security exceptions shall be time-bound, risk accepted and tracked to closure. | Exception register. |
| SEC-011 | MUST; R1 | Threat models shall be refreshed for major architecture, product and data changes. | Threat-model review. |
| SEC-012 | MUST; R1 | Annual cyber exercises shall include credential compromise, API abuse, data exfiltration and ransomware/region loss. | Exercise report. |

## 26\. Data Operations and Model Operations

Data operations maintain feed quality, lineage, feature computation, retention, analytics and reporting. Model operations govern model deployment, monitoring, rollback and evidence.

-   Source data comes from telcos and platform events; quality controls must be programme-specific.
    
-   Analytical convenience cannot override system-of-record boundaries.
    

### Binding operational requirements

| ID | Class | Requirement | Acceptance evidence |
| --- | --- | --- | --- |
| DAT-001 | MUST; R1 | Every inbound dataset shall have owner, schema, quality rules, expected cadence, retention and reconciliation control. | Data-contract register. |
| DAT-002 | MUST; R1 | Data-quality dimensions shall include completeness, validity, uniqueness, consistency, timeliness and tenant correctness. | Quality dashboard. |
| DAT-003 | MUST; R1 | Critical quality failures shall block score publication or affected decisions according to approved safe-state rules. | Quality-gate test. |
| DAT-004 | MUST; R1 | Data lineage shall trace source fields to features, scores, decisions, reports and regulatory extracts. | Lineage sample. |
| DAT-005 | MUST; R1 | Backfills shall be controlled, versioned and reconciled to avoid rewriting historical decisions. | Backfill evidence. |
| DAT-006 | MUST; R1 | Analytical datasets shall be separated from transactional systems and refreshed through governed pipelines. | Architecture/control review. |
| DAT-007 | MUST; R1 | Model artifacts shall have version, owner, training data lineage, validation, approval and deployment history. | Model registry. |
| DAT-008 | MUST; R1 | Model monitoring shall include drift, stability, performance, calibration and operational failure. | Monitoring report. |
| DAT-009 | MUST; R1 | Model rollback shall be tested and shall preserve decision traceability. | Rollback exercise. |
| DAT-010 | MUST; R1 | Data access shall be purpose-based and reviewed regularly. | Access review. |
| DAT-011 | MUST; R1 | Late-arriving data shall be handled through explicit effective-time and processing-time rules. | Late-data scenario. |
| DAT-012 | MUST; R1 | Data jobs shall publish completion, row counts, error counts, freshness and quality metrics. | Pipeline evidence. |

## 27\. SRE, Platform and Capacity Operations

SRE operates the shared technical platform, balancing reliability, safe change, performance and cost. The service is designed to scale from one telco to tens of millions of subscriber profiles and multiple operators.

-   Operational focus: error budgets, queue lag, capacity, dependency health, database performance, cost and automation.

### Binding operational requirements

| ID | Class | Requirement | Acceptance evidence |
| --- | --- | --- | --- |
| SRE-001 | MUST; R1 | Each critical service shall have SLI/SLO definitions, dashboards, alerts and runbooks. | SRE service pack. |
| SRE-002 | MUST; R1 | Error budgets shall inform release pace and reliability work. | Error-budget report. |
| SRE-003 | MUST; R1 | Capacity plans shall cover peak originations, scoring, feed ingestion, recovery events, reconciliation and reporting. | Capacity plan. |
| SRE-004 | MUST; R1 | Load tests shall model telco campaigns, retry storms, duplicate events and degraded dependencies. | Performance test report. |
| SRE-005 | MUST; R1 | Autoscaling shall be bounded by database, queue, partner-rate-limit and cost constraints. | Scaling test. |
| SRE-006 | MUST; R1 | Queue lag and dead-letter growth shall be monitored per tenant and event type. | Messaging dashboard. |
| SRE-007 | MUST; R1 | Database operations shall include backup, restore, replication, vacuum/maintenance, partition and index health. | Database runbook evidence. |
| SRE-008 | MUST; R1 | Cost attribution shall be available per telco programme for compute, messaging, storage, SMS/USSD and external services. | Cost dashboard. |
| SRE-009 | MUST; R1 | Noisy-tenant controls shall prevent one operator from exhausting shared capacity. | Isolation load test. |
| SRE-010 | MUST; R1 | Platform maintenance shall preserve event durability and financial consistency. | Maintenance exercise. |
| SRE-011 | MUST; R1 | Technical debt and toil shall be measured and prioritised through service governance. | SRE backlog. |
| SRE-012 | MUST; R1 | Runbooks shall prefer automation but require safe guards, dry-run where possible and post-action validation. | Automation audit. |

## 28\. Business Continuity, Disaster Recovery and Crisis Management

Continuity planning prioritises life/safety where relevant, financial truth, recovery-event capture, tenant isolation, evidence and controlled restoration. New originations may remain unavailable while essential financial processing continues.

-   Release 1 target: RTO \<=30 minutes for core services, ledger RPO approximately zero through approved replication design, other stores by classified RPO.

## Business Continuity and Disaster Recovery Decision Flow

![image](https://static-us-img.skywork.ai/prod/nexus/1784239337/cropped_image_7_1784239337667185486.jpg)

_Figure 5 - Business continuity and disaster-recovery decision flow._

### Binding operational requirements

| ID | Class | Requirement | Acceptance evidence |
| --- | --- | --- | --- |
| DRP-001 | MUST; R1 | A business impact analysis shall classify services, dependencies, RTO, RPO, minimum staffing and recovery sequence. | Approved BIA. |
| DRP-002 | MUST; R1 | DR plans shall identify primary/secondary locations, data protection, failover, failback and decision authority. | DR plan. |
| DRP-003 | MUST; R1 | Ledger and acknowledged recovery events shall receive the strongest data-loss protection. | Replication evidence. |
| DRP-004 | MUST; R1 | Originations shall remain suspended until financial and tenant-integrity validation passes after recovery. | DR exercise. |
| DRP-005 | MUST; R1 | DR activation shall have explicit declaration, communication and command roles. | Activation runbook. |
| DRP-006 | MUST; R1 | Restoration shall follow service priority: evidence/event ingestion, ledger, recovery, status/reconciliation, support, then originations. | Recovery sequence test. |
| DRP-007 | MUST; R1 | Backups shall be immutable or protected from routine administrative compromise and regularly restored. | Restore test. |
| DRP-008 | MUST; R1 | DR exercises shall include region loss, database corruption, queue loss risk, cyber compromise and telco outage. | Exercise calendar. |
| DRP-009 | MUST; R1 | Business continuity shall cover loss of staff, office, connectivity, key vendor and telco contact. | BCP exercise. |
| DRP-010 | MUST; R1 | Failback shall be controlled and reconciled, not assumed complete when traffic returns. | Failback evidence. |
| DRP-011 | MUST; R1 | Exercise findings shall have owners and deadlines and be reported to governance. | Action tracker. |
| DRP-012 | MUST; R1 | Critical third parties shall provide continuity evidence aligned to contracted objectives. | Supplier assurance evidence. |

## 29\. Environments, QA and Operational Acceptance

Quality assurance proves functional, financial, security, resilience and operational behaviour before production. Environments and test data must support realistic telco and failure scenarios without exposing production personal data.

-   Environment path: local/dev, integration, system test, performance, security, UAT, telco certification, pre-production and production as appropriate.

### Binding operational requirements

| ID | Class | Requirement | Acceptance evidence |
| --- | --- | --- | --- |
| QAO-001 | MUST; R1 | Environment purposes, data classes, access, refresh, integrations and change controls shall be documented. | Environment register. |
| QAO-002 | MUST; R1 | Production personal data shall not be copied to lower environments without approved protection and necessity. | Test-data audit. |
| QAO-003 | MUST; R1 | Test coverage shall include functional, integration, contract, state-machine, ledger, reconciliation, security, performance, resilience and operational scenarios. | Master test plan. |
| QAO-004 | MUST; R1 | Every numbered requirement shall map to acceptance evidence or an approved waiver. | Traceability report. |
| QAO-005 | MUST; R1 | Financial tests shall prove debit=credit and state-to-ledger invariants under normal and edge cases. | Financial test pack. |
| QAO-006 | MUST; R1 | Operational acceptance shall verify monitoring, alerts, runbooks, support, access, backup, DR, capacity and evidence. | OAT sign-off. |
| QAO-007 | MUST; R1 | Defect severity shall consider customer, financial, regulatory and operational impact. | Defect matrix. |
| QAO-008 | MUST; R1 | No Severity 1 or unresolved release-blocking defect shall remain at go-live. | Defect report. |
| QAO-009 | MUST; R1 | Non-production telco integration shall use simulator and sandbox where real access is unavailable. | Simulator evidence. |
| QAO-010 | MUST; R1 | Performance tests shall use representative distributions and peak campaign scenarios. | Performance evidence. |
| QAO-011 | MUST; R1 | UAT shall include telco, risk, finance, operations, support and compliance roles. | UAT sign-off. |
| QAO-012 | MUST; R1 | Test evidence shall be immutable, versioned and linked to release artifacts. | Evidence repository. |

## 30\. Telco Sandbox, Certification and Onboarding

Onboarding is a controlled programme that aligns commercial responsibilities, data, interfaces, controls, operations and financial reconciliation before production. The simulator allows build and demonstration before live telco access.

-   Stages: discovery, contract/control mapping, canonical data mapping, adapter build, sandbox, certification, dual run, pilot, scale.

## Telco Onboarding, Certification and Production Cutover

![image](https://static-us-img.skywork.ai/prod/nexus/1784239337/cropped_image_5_1784239337085386523.jpg)

_Figure 6 - Telco onboarding, certification and cutover lifecycle._

### Binding operational requirements

| ID | Class | Requirement | Acceptance evidence |
| --- | --- | --- | --- |
| ONB-001 | MUST; R1 | Each telco onboarding shall have a plan, RACI, decision log, dependency register and acceptance criteria. | Onboarding plan. |
| ONB-002 | MUST; R1 | Responsibility for disclosure, consent, complaints, bureau reporting, notifications, fulfilment, recovery and reconciliation shall be contractually mapped. | Responsibility matrix. |
| ONB-003 | MUST; R1 | Canonical data mapping shall identify source, meaning, format, cadence, quality, ownership and fallback for every field. | Mapping specification. |
| ONB-004 | MUST; R1 | The simulator shall support success, decline, timeout-after-success, duplicates, delay, partial fulfilment, reversal and out-of-order events. | Fault-injection catalogue. |
| ONB-005 | MUST; R1 | Adapter certification shall include authentication, rate limits, idempotency, status enquiry, replay, monitoring and security. | Certification report. |
| ONB-006 | MUST; R1 | Telco operational contacts, support windows, maintenance, incident and escalation paths shall be tested. | Contact test. |
| ONB-007 | MUST; R1 | Financial certification shall reconcile representative advances, recoveries, fees, reversals, taxes and settlements. | Financial certification pack. |
| ONB-008 | MUST; R1 | Production credentials shall be issued only after security and readiness approval. | Credential approval. |
| ONB-009 | MUST; R1 | A telco shall be isolated in production configuration until formal activation. | Activation control test. |
| ONB-010 | MUST; R1 | Onboarding artifacts shall be reusable as a template without hardcoding operator-specific behaviour into the core. | Architecture review. |
| ONB-011 | MUST; R1 | Pilot volumes and exposure shall be capped and progressively increased through approved gates. | Pilot configuration. |
| ONB-012 | MUST; R1 | Exit criteria shall include operating, financial, technical, security, legal and commercial acceptance. | Onboarding sign-off. |

## 31\. Migration, Dual Run and Cutover

Migration from an incumbent is a financial and customer-risk programme, not merely data transfer. Dual running and controlled cutover must prove parity while preventing double offers, double credit and inconsistent recovery ownership.

-   Migration domains: subscribers, scores/limits, active advances, balances, repayment history, configurations, disclosures, complaints, reconciliation and evidence.

### Binding operational requirements

| ID | Class | Requirement | Acceptance evidence |
| --- | --- | --- | --- |
| MIG-001 | MUST; R1 | A migration inventory shall identify every source dataset, owner, quality, volume, retention and target treatment. | Migration inventory. |
| MIG-002 | MUST; R1 | Data mapping shall define transformation, validation, defaulting, rejection and reconciliation rules. | Mapping document. |
| MIG-003 | MUST; R1 | Active advance and outstanding balance migration shall be financially reconciled before cutover. | Balance reconciliation. |
| MIG-004 | MUST; R1 | Dual-run ownership shall ensure only one platform can originate or recover each subscriber/advance at a time. | Dual-run control test. |
| MIG-005 | MUST; R1 | Parallel scoring shall compare eligibility, limits, tier movement and reason outcomes within approved tolerances. | Score parity report. |
| MIG-006 | MUST; R1 | Migration rehearsals shall include production-scale volume and cutover timing. | Rehearsal report. |
| MIG-007 | MUST; R1 | Cutover shall have go/no-go criteria, command structure, communications, rollback and reconciliation checkpoints. | Cutover plan. |
| MIG-008 | MUST; R1 | Rollback shall define how new advances and recoveries created after cutover are handled without loss or duplication. | Rollback scenario. |
| MIG-009 | MUST; R1 | Incumbent historical data shall be retained or accessible according to legal, complaint and bureau obligations. | Retention evidence. |
| MIG-010 | MUST; R1 | Customer and telco service teams shall receive cutover scripts and known-issue guidance. | Support readiness. |
| MIG-011 | MUST; R1 | Post-cutover reconciliation shall compare transaction counts, amounts, states, balances and settlements. | Cutover reconciliation. |
| MIG-012 | MUST; R1 | Migration defects shall be triaged by financial/customer impact and may block scale-up. | Defect/governance record. |

## 32\. Pilot, Rollout, Hypercare and BAU Transition

Launch uses progressive exposure and operational learning. A pilot is not successful merely because transactions complete; it must demonstrate customer, risk, financial and operational control.

-   Phases: internal/synthetic, employee or controlled cohort where lawful, limited market pilot, progressive scale, hypercare, BAU.

### Binding operational requirements

| ID | Class | Requirement | Acceptance evidence |
| --- | --- | --- | --- |
| ROL-001 | MUST; R1 | Pilot scope shall define subscribers, geography/segment where lawful, channels, products, exposure, duration and exit criteria. | Pilot plan. |
| ROL-002 | MUST; R1 | Pilot caps shall limit daily originations, per-subscriber limits, total exposure and funding utilisation. | Pilot configuration. |
| ROL-003 | MUST; R1 | Daily pilot governance shall review incidents, approval, fulfilment, recovery, complaints, fraud, reconciliation and funding. | Pilot daily pack. |
| ROL-004 | MUST; R1 | Scale-up shall require formal gate approval and may occur by cohort rather than all-at-once. | Scale gate. |
| ROL-005 | MUST; R1 | Any critical control failure shall pause scale-up and may suspend originations. | Pause decision record. |
| ROL-006 | MUST; R1 | Hypercare shall provide enhanced staffing, monitoring, vendor/telco presence and update cadence. | Hypercare plan. |
| ROL-007 | MUST; R1 | Known issues shall have workarounds, customer impact, owners and exit dates. | Known-issue register. |
| ROL-008 | MUST; R1 | BAU transition shall require service acceptance, support ownership, capacity, documentation and residual-risk sign-off. | BAU acceptance. |
| ROL-009 | MUST; R1 | Hypercare exit shall use objective criteria rather than a fixed date alone. | Exit assessment. |
| ROL-010 | MUST; R1 | Performance and cost shall be measured against the commercial business case during scale-up. | Unit-economics report. |
| ROL-011 | MUST; R1 | Post-launch review shall update assumptions, risk appetite, procedures and roadmap. | Post-launch review. |
| ROL-012 | MUST; R1 | Rollout communications shall be coordinated with telco customer-care and marketing teams. | Communication plan. |

## 33\. Training, Knowledge and Documentation

A platform of this complexity cannot rely on a few builders. Knowledge must be converted into controlled documentation, role-based training and exercises before production.

-   Documentation hierarchy: policy, standard, procedure, runbook, quick reference, architecture decision and evidence template.

### Binding operational requirements

| ID | Class | Requirement | Acceptance evidence |
| --- | --- | --- | --- |
| TRN-001 | MUST; R1 | Every production role shall have a competency and training matrix. | Training matrix. |
| TRN-002 | MUST; R1 | Training shall cover system-of-record boundaries, tenant isolation, financial invariants and no-blind-retry rules. | Training content. |
| TRN-003 | MUST; R1 | Role-based training shall include realistic scenarios and portal practice. | Training record. |
| TRN-004 | MUST; R1 | Runbooks shall have owner, version, review date, prerequisites, steps, validation, rollback and escalation. | Runbook audit. |
| TRN-005 | MUST; R1 | Documentation shall be searchable, access-controlled and linked from alerts and service records. | Knowledge-base test. |
| TRN-006 | MUST; R1 | Material process or system change shall trigger documentation and training impact assessment. | Change checklist. |
| TRN-007 | MUST; R1 | Critical runbooks shall be exercised at least annually and after material change. | Exercise evidence. |
| TRN-008 | MUST; R1 | New joiners shall not receive unsupervised production duties until competency is verified. | Access/training control. |
| TRN-009 | MUST; R1 | Knowledge concentration risk shall be measured and mitigated through cross-training. | Bus-factor review. |
| TRN-010 | MUST; R1 | Lessons from incidents, complaints, audit and migration shall update training content. | Update evidence. |
| TRN-011 | MUST; R1 | Telco and vendor teams shall receive interface-specific operational guidance where responsibilities intersect. | Partner training pack. |
| TRN-012 | MUST; R1 | Obsolete documentation shall be archived and removed from active use. | Document-control audit. |

## 34\. Vendor and Third-Party Service Management

The service depends on telcos, cloud, messaging, bureau, banking, identity, monitoring and other providers. Outsourcing execution does not outsource accountability.

-   Third parties are tiered by customer, financial, data, security and continuity criticality.

### Binding operational requirements

| ID | Class | Requirement | Acceptance evidence |
| --- | --- | --- | --- |
| TPR-001 | MUST; R1 | A third-party inventory shall identify service, owner, data access, criticality, contract, SLA, concentration and exit plan. | Supplier inventory. |
| TPR-002 | MUST; R1 | Due diligence shall cover financial, operational, security, privacy, continuity, regulatory and subcontractor risk. | Due-diligence pack. |
| TPR-003 | MUST; R1 | Contracts shall include service, incident notification, audit, data, security, continuity, exit and cooperation obligations. | Contract review. |
| TPR-004 | MUST; R1 | Critical suppliers shall provide periodic control and continuity assurance. | Supplier assurance report. |
| TPR-005 | MUST; R1 | Supplier performance shall be reviewed against SLA, incidents, changes, capacity and remediation. | Supplier scorecard. |
| TPR-006 | MUST; R1 | Concentration and single-point-of-failure risk shall be documented and mitigated. | Concentration review. |
| TPR-007 | MUST; R1 | Third-party incidents shall be linked to internal incident and problem records. | Incident linkage. |
| TPR-008 | MUST; R1 | Subcontractor changes affecting risk shall require notification and assessment. | Subprocessor register. |
| TPR-009 | MUST; R1 | Exit plans shall cover data return/deletion, replacement, transition and continuity. | Exit plan. |
| TPR-010 | MUST; R1 | No supplier shall receive broader tenant or personal data than necessary. | Data-access audit. |
| TPR-011 | MUST; R1 | Commercial disputes shall not prevent safe access to operational and financial evidence. | Contract/control evidence. |
| TPR-012 | MUST; R1 | Critical vendor contacts shall participate in relevant exercises. | Exercise record. |

## 35\. Management Information, KPIs and Governance Cadence

Management information must join customer, service, risk, fraud, financial, treasury, compliance and cost views without obscuring tenant-specific truth.

-   Daily packs focus on control and exceptions; weekly packs on trends and actions; monthly packs on performance, risk appetite and strategic decisions.

### Binding operational requirements

| ID | Class | Requirement | Acceptance evidence |
| --- | --- | --- | --- |
| KPI-001 | MUST; R1 | A KPI dictionary shall define owner, formula, source, grain, cut-off, target, threshold and interpretation. | KPI dictionary. |
| KPI-002 | MUST; R1 | Reports shall distinguish booked volume, successful fulfilment, active exposure, recoveries, fees and cash settlement. | Metric reconciliation. |
| KPI-003 | MUST; R1 | Technical uptime shall not substitute for customer outcome or financial correctness metrics. | Dashboard review. |
| KPI-004 | MUST; R1 | KPIs shall be available per telco and programme with controlled consolidated views. | Tenant dashboard. |
| KPI-005 | MUST; R1 | Daily MI shall highlight incidents, guardrails, funding, ambiguity, reconciliation breaks, complaints and security alerts. | Daily pack. |
| KPI-006 | MUST; R1 | Risk MI shall include approval, utilisation, delinquency, roll rates, vintage loss, tier movement and override usage. | Risk pack. |
| KPI-007 | MUST; R1 | Finance MI shall include ledger balance, suspense, reconciliation, settlement, revenue share, tax and funding cost. | Finance pack. |
| KPI-008 | MUST; R1 | Operational MI shall include queue ageing, alert quality, change success, incident times and runbook automation. | Operations pack. |
| KPI-009 | MUST; R1 | Cost MI shall attribute platform and channel costs per programme and transaction. | Cost report. |
| KPI-010 | MUST; R1 | Metric changes shall be versioned and approved to prevent silent definition drift. | Metric-change record. |
| KPI-011 | MUST; R1 | Data freshness and completeness shall be visible on management reports. | Report control. |
| KPI-012 | MUST; R1 | Governance actions shall be tracked to closure and linked to the originating MI. | Action tracker. |

## 36\. Audit, Control Testing and Evidence Retention

Control assurance proves not only that controls are designed, but that they operated effectively. Evidence must be retrievable, attributable and protected from alteration.

-   Three layers: first-line control execution, second-line oversight/compliance, independent audit.

### Binding operational requirements

| ID | Class | Requirement | Acceptance evidence |
| --- | --- | --- | --- |
| AUD-001 | MUST; R1 | A control library shall map risks, controls, frequency, owner, evidence, systems and testing method. | Control library. |
| AUD-002 | MUST; R1 | Key controls shall be tested on a risk-based schedule and after material change. | Test plan. |
| AUD-003 | MUST; R1 | Control failures shall be risk-rated, assigned and tracked to remediation and effectiveness validation. | Issue register. |
| AUD-004 | MUST; R1 | Evidence shall be time-stamped, attributable, complete and retained according to schedule. | Evidence sample. |
| AUD-005 | MUST; R1 | Automated controls shall have logic, configuration, monitoring and change evidence. | Automated-control audit. |
| AUD-006 | MUST; R1 | Manual controls shall use standard templates and maker-checker where material. | Manual-control sample. |
| AUD-007 | MUST; R1 | Audit access shall preserve tenant confidentiality and least privilege. | Audit-access record. |
| AUD-008 | MUST; R1 | Regulatory and telco assurance requests shall be coordinated through a controlled response process. | Assurance-request log. |
| AUD-009 | MUST; R1 | Evidence repositories shall prevent routine alteration or deletion. | Repository control test. |
| AUD-010 | MUST; R1 | Repeat findings shall be escalated to executive governance. | Repeat-finding report. |
| AUD-011 | MUST; R1 | Control design shall be reviewed when products, jurisdictions, funding or telcos change. | Control-impact assessment. |
| AUD-012 | MUST; R1 | The requirement-to-evidence matrix shall remain current for each production release. | Traceability audit. |

## 37\. Staffing, Support Coverage and Operating Calendar

Staffing aligns coverage to customer journeys, telco windows, scoring cycles, settlement calendars and incident risk. Release 1 can start lean but must avoid single-person dependency and incompatible role combinations.

-   Coverage model includes core hours, extended hours, 24x7 on-call and mandatory 24x7 event/alert monitoring for critical flows.

### Binding operational requirements

| ID | Class | Requirement | Acceptance evidence |
| --- | --- | --- | --- |
| STF-001 | MUST; R1 | A workforce model shall estimate demand by channel, transaction volume, queue, incident and control workload. | Workforce model. |
| STF-002 | MUST; R1 | Critical functions shall have primary and secondary coverage and documented handover. | Coverage roster. |
| STF-003 | MUST; R1 | On-call rotations shall define response SLA, escalation and fatigue controls. | On-call policy. |
| STF-004 | MUST; R1 | No critical daily control shall depend on one unavailable individual. | Continuity review. |
| STF-005 | MUST; R1 | Holiday and telco campaign calendars shall inform staffing and change freezes. | Operating calendar. |
| STF-006 | MUST; R1 | Finance and settlement coverage shall align to bank, telco and funder cut-offs. | Settlement roster. |
| STF-007 | MUST; R1 | Support language capability shall align to enabled customer languages or telco hand-off. | Language coverage. |
| STF-008 | MUST; R1 | Staffing changes shall trigger access and segregation-of-duties review. | Staff-change audit. |
| STF-009 | MUST; R1 | Overtime and repeated call-outs shall be monitored as operational-risk indicators. | Fatigue report. |
| STF-010 | MUST; R1 | External support escalation shall have tested contacts and contractual coverage. | Contact test. |
| STF-011 | MUST; R1 | Capacity shall be reassessed at each pilot scale gate. | Scale staffing review. |
| STF-012 | MUST; R1 | Training and competency shall be prerequisites for rota participation. | Rota competency audit. |

## 38\. Delivery Workstreams, Backlog and Release Roadmap

Delivery governance translates the three-volume baseline into executable workstreams while preserving end-to-end outcomes. Work must not be split so narrowly that ledger, telco or operational controls emerge late.

-   Suggested workstreams: product/channel, telco integration, credit/risk, ledger/finance, recovery/collections, data/model, security/platform, portals/operations, assurance/migration.

### Binding operational requirements

| ID | Class | Requirement | Acceptance evidence |
| --- | --- | --- | --- |
| DLV-001 | MUST; R1 | Every epic shall trace to numbered requirements and acceptance evidence. | Backlog traceability. |
| DLV-002 | MUST; R1 | Cross-workstream dependencies shall be visible, owned and reviewed weekly. | Dependency log. |
| DLV-003 | MUST; R1 | A walking-skeleton release shall prove offer-to-fulfilment-to-recovery-to-ledger-to-reconciliation using the simulator. | Demo evidence. |
| DLV-004 | MUST; R1 | Financial and operational controls shall be built alongside transaction features, not deferred to hardening. | Release plan review. |
| DLV-005 | MUST; R1 | Release 1 shall prioritise one telco, airtime advance, full ledger/reconciliation, admin configuration, USSD/simulator and operational readiness. | R1 scope baseline. |
| DLV-006 | MUST; R1 | Scope changes shall assess architecture, control, data, regulatory and operational impact. | Change-control sample. |
| DLV-007 | MUST; R1 | Definition of done shall include tests, monitoring, runbooks, evidence and support readiness. | DoD audit. |
| DLV-008 | MUST; R1 | Technical debt shall be explicit, risk-rated and included in release governance. | Debt register. |
| DLV-009 | MUST; R1 | Delivery metrics shall not incentivise velocity at the expense of financial or customer correctness. | Metric review. |
| DLV-010 | MUST; R1 | Architecture decisions shall be documented before implementation where they affect invariants or long-term portability. | ADR register. |
| DLV-011 | MUST; R1 | Release planning shall reserve capacity for defects, controls, resilience and operational automation. | Capacity allocation. |
| DLV-012 | MUST; R1 | Programme governance shall maintain a single integrated release and readiness plan. | Integrated plan. |

## 39\. Go-Live Gate and Production Readiness

Go-live is a formal risk decision supported by evidence from product, telco, risk, finance, operations, security, data, compliance and technology. Passing functional UAT alone is insufficient.

-   Gate categories: business/legal, product/risk, financial, telco, technical, security/privacy, operations/support, migration and executive acceptance.

### Binding operational requirements

| ID | Class | Requirement | Acceptance evidence |
| --- | --- | --- | --- |
| GLV-001 | MUST; R1 | The go-live checklist shall have named approvers and evidence for every gate. | Signed checklist. |
| GLV-002 | MUST; R1 | Legal entity, licence/registration, contract and responsibility matrices shall be approved for the launch programme. | Legal readiness pack. |
| GLV-003 | MUST; R1 | Product, pricing, disclosure, consent, concurrent-advance, write-off and collections policies shall be approved. | Product readiness. |
| GLV-004 | MUST; R1 | Funding pools, exposure caps, posting templates, reconciliation and settlement shall be certified. | Financial readiness. |
| GLV-005 | MUST; R1 | Telco adapter, USSD/SMS channels, status enquiry, recovery events and support contacts shall be certified. | Telco certification. |
| GLV-006 | MUST; R1 | Security, privacy, access, vulnerability, backup and DR evidence shall meet acceptance criteria. | Security readiness. |
| GLV-007 | MUST; R1 | Monitoring, alerts, runbooks, staffing, training, incident and command processes shall be operational. | Operations readiness. |
| GLV-008 | MUST; R1 | Performance and capacity shall support pilot peak with agreed headroom. | Capacity evidence. |
| GLV-009 | MUST; R1 | No unresolved Severity 1 or release-blocking defect, financial imbalance or critical control failure shall remain. | Defect/control report. |
| GLV-010 | MUST; R1 | Cutover and rollback rehearsals shall have passed. | Rehearsal evidence. |
| GLV-011 | MUST; R1 | Residual risks and waivers shall have named owners, expiry and executive acceptance. | Risk acceptance. |
| GLV-012 | MUST; R1 | The final go/no-go decision shall be recorded with conditions and next review. | Decision record. |

## 40\. Volume 3 Acceptance, Traceability and Maintenance

This volume is accepted when operating procedures, roles, controls and evidence can support the target release and remain maintainable as new telcos, products and jurisdictions are added.

-   Acceptance is evidence-based and release-specific.
    
-   The document remains a controlled baseline, not a one-time project artifact.
    

### Binding operational requirements

| ID | Class | Requirement | Acceptance evidence |
| --- | --- | --- | --- |
| V3A-001 | MUST; R1 | All Volume 3 requirements shall have owner, release status and evidence mapping. | Requirements matrix. |
| V3A-002 | MUST; R1 | Operational processes shall be exercised through tabletop, simulator or production-like tests before go-live. | Exercise report. |
| V3A-003 | MUST; R1 | The RACI, runbook catalogue, control calendar, KPI dictionary and readiness checklists shall be completed for Release 1. | Appendix completion review. |
| V3A-004 | MUST; R1 | Outstanding design decisions shall be recorded and prevented from becoming undocumented defaults. | Decision register. |
| V3A-005 | MUST; R1 | Changes to Volume 1 or 2 shall trigger Volume 3 impact assessment. | Impact-assessment sample. |
| V3A-006 | MUST; R1 | The document shall be reviewed at least annually and after material incident, regulation, product or operating-model change. | Review schedule. |
| V3A-007 | MUST; R1 | Obsolete procedures shall be withdrawn from active use when superseded. | Document-control audit. |
| V3A-008 | MUST; R1 | Programme-specific annexes may add controls but shall not weaken non-configurable invariants. | Annex review. |
| V3A-009 | MUST; R1 | Evidence retention shall support telco, funder, audit, bureau and regulatory examination. | Evidence retrieval test. |
| V3A-010 | MUST; R1 | The final production service shall demonstrate end-to-end traceability from customer request to decision, fulfilment, ledger, recovery, reconciliation and settlement. | End-to-end evidence pack. |

## Appendix A - Target RACI

A=Accountable, R=Responsible, C=Consulted, I=Informed. Programme-specific RACIs may add roles but shall preserve segregation of duties.

| Activity | Executive | Service | Telco | Risk/Fraud | Finance/Treasury | Support/Compliance | Security/Data | SRE/Engineering |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Product/risk policy | A | C | I | R | C | C | I | I |
| Production service health | I | A | R | C | C | C | C | R |
| Major incident command | I | A | R | C | C | C | C | R |
| Ledger and financial close | I | C | C | C | A/R | I | I | C |
| Reconciliation and settlement | I | C | R | C | A/R | I | I | C |
| Fraud and portfolio guardrails | I | C | C | A/R | C | C | C | I |
| Complaints and conduct | I | C | C | C | C | A/R | I | I |
| Security/privacy incident | I | C | C | I | I | C | A/R | R |
| Telco onboarding/certification | I | A | R | C | C | C | C | R |
| Go-live approval | A | R | C | C | C | C | C | C |

## Appendix B - Severity, Priority and Escalation Matrix

| Severity | Indicative criteria | Response and governance |
| --- | --- | --- |
| SEV1 - Critical | Cross-tenant exposure; ledger imbalance; mass over-approval; material privacy/cyber event; widespread double credit; complete critical-service outage. | Immediate bridge and commander; executive, telco, finance, risk and security activation; updates every 30 minutes or agreed cadence. |
| SEV2 - High | Material single-telco degradation; significant fulfilment/recovery backlog; settlement risk; high customer impact with workaround. | Bridge within 30 minutes; domain leads; hourly updates; same-day containment target. |
| SEV3 - Medium | Limited impact, non-critical feature failure, manageable reconciliation break or support backlog. | Assigned team; business-day updates; standard change/problem process. |
| SEV4 - Low | Minor defect, cosmetic issue, information request or low-risk operational improvement. | Backlog and normal prioritisation. |

## Appendix C - Minimum Runbook Catalogue

-   RB-001 Start/end-of-day control cycle
    
-   RB-002 Telco adapter outage
    
-   RB-003 Timeout after fulfilment submission
    
-   RB-004 FULFILMENT\_UNKNOWN resolution
    
-   RB-005 Duplicate request/event handling
    
-   RB-006 Recovery feed delay or replay
    
-   RB-007 Ledger imbalance or posting failure
    
-   RB-008 Reconciliation break investigation
    
-   RB-009 Settlement failure or dispute
    
-   RB-010 Funding-pool exhaustion
    
-   RB-011 Mass over-approval guardrail
    
-   RB-012 Score publication failure or stale features
    
-   RB-013 USSD outage/session drop
    
-   RB-014 Notification delivery failure
    
-   RB-015 Cross-tenant access alert
    
-   RB-016 Privacy/security breach
    
-   RB-017 Database failover and restore
    
-   RB-018 Message-bus backlog/DLQ
    
-   RB-019 Certificate/secret expiry
    
-   RB-020 Bureau submission rejection
    
-   RB-021 Complaints escalation
    
-   RB-022 Write-off and recovery after write-off
    
-   RB-023 DR activation/failback
    
-   RB-024 Cutover rollback
    
-   RB-025 Break-glass access review
    

## Appendix D - Operating Control Calendar

| Cadence | Minimum controls | Primary owner |
| --- | --- | --- |
| Per shift | Health, alerts, incidents, backlog, funding, changes, handover | Command Centre |
| Daily opening | Feed/scoring freshness, configuration, funding, adapters, prior exceptions | Business Ops / SRE / Risk |
| Intraday | Volume, approval, fulfilment, ambiguity, recovery, exposure, complaints, fraud | Business Ops / Risk / Telco Ops |
| Daily close | Ledger balance, event completeness, reconciliation, settlement readiness, evidence | Finance / Operations |
| Weekly | Risk/collections trends, problem backlog, capacity, vendor and telco performance | Service Review / Credit Committee |
| Monthly | Financial close, settlement, KPI/SLA, control testing, access exceptions, risk appetite | Governance Forums |
| Quarterly | Access/SoD, DR or restore rotation, supplier review, model/data review | Security / Risk / Audit |
| Annual | Full DR, cyber and crisis exercise; policy and document review | Executive Governance |

## Appendix E - Operational Readiness Checklist

-    ORR-001 Service catalogue and SLOs approved
    
-    ORR-002 RACI and escalation contacts approved/tested
    
-    ORR-003 Production access and SoD certified
    
-    ORR-004 Monitoring/alerts/runbooks operational
    
-    ORR-005 Daily control calendar rehearsed
    
-    ORR-006 Ledger and reconciliation controls certified
    
-    ORR-007 Funding and portfolio guardrails configured
    
-    ORR-008 Support/complaints processes trained
    
-    ORR-009 Security/privacy controls passed
    
-    ORR-010 Backup/restore and DR exercised
    
-    ORR-011 Telco sandbox and certification passed
    
-    ORR-012 Capacity/performance headroom approved
    
-    ORR-013 Incident and major-incident simulation passed
    
-    ORR-014 Evidence repository and traceability complete
    
-    ORR-015 Residual risks formally accepted
    

## Appendix F - Cutover and Rollback Checklist

-    CUT-001 Confirm go/no-go authority and bridge
    
-    CUT-002 Freeze source/configuration changes
    
-    CUT-003 Validate final migration files and checksums
    
-    CUT-004 Reconcile active advances and balances
    
-    CUT-005 Confirm single origination/recovery owner
    
-    CUT-006 Activate routing and feature flags by cohort
    
-    CUT-007 Validate first transactions end to end
    
-    CUT-008 Compare counts, amounts, states and ledger
    
-    CUT-009 Monitor support, incidents and guardrails
    
-    CUT-010 Escalate breaks before scale increase
    
-    CUT-011 Rollback only through approved decision
    
-    CUT-012 Reconcile all post-cutover events if rollback
    
-    CUT-013 Communicate status to telco and stakeholders
    
-    CUT-014 Exit cutover when financial parity and service stability are proven
    

## Appendix G - KPI Dictionary

| KPI | Definition | Owner | Cadence |
| --- | --- | --- | --- |
| Offer availability | Eligible offer responses / valid offer requests | Channel/Decisioning | Daily |
| Approval rate | Approved advances / valid advance requests | Decisioning | Intraday/Daily |
| Fulfilment success | Confirmed successful fulfilments / submitted fulfilments | Telco Ops | Intraday/Daily |
| Ambiguity rate | FULFILMENT\_UNKNOWN / submitted fulfilments | Telco Ops | Intraday |
| Recovery rate | Recovered principal / due or originated principal by vintage | Collections | Daily/Weekly |
| Early delinquency | Balances entering configured early bucket / booked balance | Risk | Daily/Weekly |
| Ledger balance exceptions | Unbalanced transactions or currency control breaks | Finance | Real-time/Daily |
| Reconciliation break rate | Unmatched items / compared items | Finance/Telco Ops | Daily |
| Settlement variance | Unexplained settlement difference | Finance | Per cycle |
| Funding utilisation | Reserved + utilised / committed pool | Treasury | Real-time |
| Complaint rate | Complaints / successful advances | Support/Compliance | Weekly/Monthly |
| Cost per advance | Attributed platform + channel cost / successful advances | Finance/SRE | Monthly |

## Appendix H - Major Incident Scenario Catalogue

-   SCN-001 Telco timeout after successful credit
    
-   SCN-002 Duplicate recovery events
    
-   SCN-003 Recovery reversal arrives before original
    
-   SCN-004 Mass configuration error increases limits
    
-   SCN-005 Cross-tenant credential/payload mismatch
    
-   SCN-006 Ledger posting template imbalance
    
-   SCN-007 Funding pool reaches hard limit
    
-   SCN-008 Score publication is partial or stale
    
-   SCN-009 USSD dies after customer confirmation
    
-   SCN-010 Notification provider outage
    
-   SCN-011 Telco recharge feed delayed for hours
    
-   SCN-012 Database primary loss
    
-   SCN-013 Message-bus partition or DLQ surge
    
-   SCN-014 Cyber credential compromise
    
-   SCN-015 Privacy data extraction or wrong-recipient export
    
-   SCN-016 Bureau submission rejected at scale
    
-   SCN-017 Bank settlement does not arrive
    
-   SCN-018 Incumbent cutover creates double recovery ownership
    
-   SCN-019 Region loss during peak campaign
    
-   SCN-020 Customer complaint reveals systemic disclosure defect
    

## Appendix I - Evidence and Traceability Matrix

| Domain | Minimum evidence classes |
| --- | --- |
| Governance | Charters, minutes, decision register, RACI |
| Service | Catalogue, SLO dashboards, SLA/OLA reports |
| Operations | Shift handovers, daily control packs, queue reports |
| Incident | Tickets, timelines, communications, RCA and actions |
| Risk/Fraud | Policy versions, guardrail events, cases, committee packs |
| Finance | Ledger controls, adjustments, reconciliation, settlement, close |
| Treasury | Funding pool, forecasts, limit and counterparty reports |
| Compliance | Disclosure/consent, complaints, bureau/regulatory submissions |
| Security/Privacy | Access, alerts, vulnerabilities, incidents, DSR and retention |
| SRE/Data | Capacity, backup/restore, pipeline quality, model monitoring |
| Testing/Release | Traceability, test results, OAT, security and performance evidence |
| Migration/Rollout | Rehearsals, cutover, parity, pilot and hypercare reports |

## Appendix J - Glossary

| Term | Definition |
| --- | --- |
| Adapter | Telco-specific integration component translating operator contracts into canonical platform interfaces. |
| Ambiguous fulfilment | A submitted telco instruction whose final outcome is not yet known. |
| Business date | Controlled reporting/accounting date, which may differ from event or processing timestamp. |
| Control calendar | Scheduled set of opening, intraday, closing, weekly, monthly and periodic controls. |
| Guardrail | Automated threshold-based action that warns, restricts or suspends originations. |
| OAT | Operational Acceptance Testing. |
| RPO | Maximum tolerable data loss measured in time. |
| RTO | Target time to restore a service after disruption. |
| SLO | Internal measurable service objective. |
| SLA | Contractual service commitment. |
| Telco tenant | Isolated operator context used for routing, data, configuration, reporting and access. |
| Walking skeleton | Minimal end-to-end implementation proving the complete business and financial flow. |