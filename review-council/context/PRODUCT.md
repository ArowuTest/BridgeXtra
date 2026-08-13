# BridgeXtra Product Context

## What BridgeXtra Is

BridgeXtra is a telco digital-credit platform for airtime/data advances. A subscriber can receive telco value before paying for it; the platform records the economic exposure, coordinates fulfilment, and later applies telco recovery evidence against the outstanding advance. Because reconciliation and recovery ultimately affect real economic balances, correctness is defined by money/evidence integrity rather than by HTTP success alone.

The current engineering strategy is **retain and harden, not rewrite**. The original audit found a disciplined implementation with a strong core architecture, but identified concrete Critical/High/Medium controls that had to be closed before live-money readiness.

## Major Product/Technical Areas

- **Subscriber/account state** — tenant/telco-scoped customer identity and eligibility state.
- **Offer / advance origination** — governed credit decision and immutable economic terms.
- **Fulfilment** — delivery of airtime/data through a telco adapter, with explicit unknown/error handling and reconciliation.
- **Funding pools** — capital/liquidity capacity and economic pool mutation supporting advances/recoveries.
- **Ledger/economic evidence** — durable monetary entries used to cross-foot product state and prevent duplicate economic effects.
- **Recovery** — application of telco-side deductions/recovery evidence against advances.
- **Reconciliation** — independent comparison of telco evidence and platform evidence for fulfilment/recovery layers.
- **Feature/scoring pipeline** — governed feature ingestion and credit-scoring inputs/decisions.
- **Governed configuration** — maker/checker lifecycle, effective windows, immutable evidence hashes, scoped config domains.
- **Operations/control plane** — arming/disarming, reconciliation controls, schedulers, retries, missed/overdue state, incident response.
- **Portal/API** — operator surfaces with role/scope controls; public/data-plane API must not hold owner/BYPASSRLS authority.
- **Adapters/simulator** — canonical engineering interfaces for telco interactions. Simulator evidence is engineering/demo evidence only.

## Core Economic Flow

A typical high-level flow is:

1. A subscriber is evaluated under governed programme/scoring/config rules.
2. An advance/offer is created with immutable programme/economic evidence.
3. Confirmation must bind to that immutable offer/programme rather than trust a caller to restate the programme.
4. Fulfilment requests telco value delivery and records an unambiguous outcome or a separately reconciled unknown state.
5. Later telco recovery evidence is ingested/applied idempotently against the correct subscriber/advance/economic book.
6. Reconciliation compares the independent telco-side evidence with platform-side evidence and persists durable run headers/items/control totals.
7. Recovery ingress is allowed only when the RECOVERY layer is both armed and sufficiently fresh.

## RECOVERY Freshness Is a Money Gate

`recon_layer_arming.last_recon_at` is not ordinary telemetry. The recharge/recovery money path checks RECOVERY layer liveness before accepting money-impacting recovery events. Therefore any code capable of advancing RECOVERY freshness participates in a money-safety boundary.

A freshly armed layer is intentionally **not live** until qualifying reconciliation evidence exists. Stale evidence must age the gate closed.

A reconciliation may persist a `REJECTED` run and return a normal Summary rather than an error. HIGH-016 was the historical defect where a rejected summary could previously renew freshness. That is fixed; reviewers must preserve the invariant that rejected/unqualified evidence never opens or renews the money gate.

## Monetary Confirmation, Not Row Count

`dayConfirmed` deliberately reasons on monetary control totals, not only record counts. Equal row counts do not prove equal economics: an intra-day omission/sub-event change can preserve counts while moving money. Any replacement/recomputation of confirmation must preserve the monetary property.

## Reconciliation Evidence Model

Current recovery reconciliation persists immutable/monotonic evidence including `recon_runs` and, for the MED-004 A2 publisher path, `recon_recovery_qualifications`. A publisher must independently bind to durable evidence, validate that the run is still appropriate/ACTIVE, and re-evaluate current governed policy rather than trust a caller-supplied threshold/window/timestamp.

## Scheduler / Mandatory-Control Objective

BX-MED-004 is building production-grade mandatory-control orchestration. Required properties include:

- one durable occurrence identity per job/scope/slot;
- leases and monotonic ownership fencing;
- durable attempts and retry/exhaustion history;
- crash/reclaim semantics;
- a zombie worker cannot heartbeat, terminalize, publish freshness, or perform fence-sensitive writes after losing ownership;
- terminal success and any freshness publication tied to that success are atomic under the current ownership fence;
- missed/overdue state is independently observable even if all scheduler workers are dead;
- per-tenant isolation and bounded concurrency;
- manual controls use the same evidence protocol and cannot erase historical scheduled misses/failures.

## External Telco / MTN Boundary

There is currently no final MTN contract/feed specification available for production integration. Do **not** invent MTN payload schemas, signature rules, authentication semantics, reconciliation contracts, service levels, or sandbox/UAT evidence.

Partner-specific implementation remains external/dormant until real contractual/interface material exists. The simulator/canonical adapter may prove engineering behavior but is not evidence of MTN acceptance or production UAT.

## Readiness Interpretation

Internal simulator demonstrations can be technically meaningful while external/commercial/partner gates remain open. Do not equate green source tests with live-MTN or live-money production readiness. Final readiness also requires partner contracts/UAT, IAM/operational controls, DR/load/chaos/security evidence, and deployment-role proof.
