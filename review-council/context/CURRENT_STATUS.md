# BridgeXtra Current Engineering Status

This file is mutable context for the review council. Runtime provenance supplied by the harness is always authoritative for SHA/migration state; this file describes audit/tranche disposition, not git identity.

## Critical / High

- Original non-partner Critical/High source-engineering findings: **CLOSED**.
- `BX-HIGH-016` (REJECTED reconciliation could renew RECOVERY freshness): **CLOSED**.
- Real MTN contract/auth/feed/sandbox UAT (`HIGH-015` and partner-specific portions of related findings): **EXTERNAL / DORMANT** until real partner material exists.

## MED-004 Mandatory-Control Scheduler

- MED-004 overall: **OPEN**.
- MED-004-A1 control/publication extraction: **CLOSED**.
- MED-004-A2 durable/re-verifying recovery publisher primitive: **CLOSED** at the bounded F1/F2 follow-up (`b5f8589` lineage).
- A2-F3 — concurrent policy activation/publication ordering caused by PostgreSQL transaction-start `now()`: **NEXT-TRANCHE / PRE-CUTOVER**.
- Real scheduler occurrence identity + monotonic ownership fence integration: **OPEN / NEXT TRANCHE**.
- Terminal success + freshness publication under the current occurrence fence: **OPEN / PRE-CUTOVER**.
- Durable missed/overdue visibility independent of scheduler-worker survival: **OPEN**.
- Rollback/re-grant artifact, mixed-version fleet drain proof, `tcp_app` freshness UPDATE revoke, and final DB-role/DSN proof: **OPEN / CUTOVER GATES**.

## Scoring Scheduler

`BX-MED-016` — existing scoring scheduler reclaim lacks monotonic ownership fencing.

Disposition: **DORMANT / ACTIVATION BLOCKER**, severity MEDIUM if activated unfixed. Do not convert it into a current live blocker unless deployed runtime evidence proves the scoring scheduler is enabled. Any future activation must either fence it directly or converge it onto the MED-004 occurrence/fence primitives.

## Other Mediums

- MED-001: CLOSED.
- MED-002: CLOSED.
- MED-005: CLOSED.
- MED-006: CLOSED.
- MED-007: CLOSED.
- MED-008: CLOSED.
- MED-009 canonical config hashing: CLOSED as an engineering invariant, but a separate historical production-row incident hardening follow-up remains bounded and must not be conflated with MED-004.
- MED-010: CLOSED.
- MED-015 supply-chain/CI engineering phase: CLOSED; dependency-review now runs on direct pushes as well as PRs.
- MED-003 bounded-memory staged/streamed feature ingestion: **OPEN**, sequenced after MED-004.
- MED-011 legal/compliance governance: OWNER/EXTERNAL.
- MED-012 enterprise SSO/MFA: OWNER/IMPLEMENTATION/RISK decision.
- MED-013 airtime-only scope: OWNER scope decision.
- MED-014 bureau integration: EXTERNAL/contract/regulatory dependency.

## Production Readiness

Do not declare live-money/production readiness while MED-004/MED-003 and external/owner gates remain open. Internal simulator demonstrations and green CI are necessary engineering evidence, not sufficient partner/production acceptance.
