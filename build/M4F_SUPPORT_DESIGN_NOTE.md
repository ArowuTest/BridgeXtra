# M4f — Support Workspace: Design Note (lightweight per VR-39)

Scope: the smallest workspace. Two surfaces, zero new money semantics, all
established idioms — no gate doc needed unless the reviewer flags deviation.

## 1. Masked subscriber timeline (V2-SUB-008, UI-004)

`GET /v1/portal/support/subscriber?token=` — the case-handling view: live
identity (status, since-when), advances history (state, face, outstanding,
dates), notifications, complaints, status actions. Worker-pool reads,
TelcoLevelBound (subscriber data is telco-grained). No-oracle 404.

**Masking**: every subscriber token in RESPONSES is masked server-side
(`…` + last 4) — masked by default per UI-004, enforced where the data
leaves the platform, not as UI decoration. The operator types the full
token to search (they receive it from the channel system); the platform
never echoes it back whole.

**No step-up reveal is built, deliberately**: the platform is PII-lean by
design — it stores tokenised MSISDNs only, and detokenisation lives with
the telco. There is no raw MSISDN on file to reveal, so a reveal flow would
be armed-but-dead theatre. If a future data contract ever lands raw
MSISDNs, the reveal ships THEN, with step-up auth + audit (per VR-39's
condition), alongside that contract.

## 2. Complaints workflow (V1-CUS, M3f backend)

The M3f usecases (`OpenComplaint`, `ProgressComplaint`: OPEN → IN_REVIEW →
RESOLVED/REJECTED, resolution mandatory at close, audited) get their portal
surface: list (worker-pool TelcoLevelBound read, new), open, progress.
CAS-guarded transition already exists (`Transition ... WHERE state=$from`).

## 3. RBAC (V3-ORG-005: SUPPORT read-only on financial truth)

- Timeline + complaints reads: ADMIN, SUPPORT, OPS, RISK, FINANCE.
- Open/progress complaint: ADMIN, SUPPORT, OPS (case management, not
  financial truth).
- SUPPORT appears in NO mutation allowlist that touches ledger, limits,
  config, guardrails, status actions, or demo — structurally enforced by
  routeRoles (deny-by-default) and provable from `RBACRoutes()`: the test
  asserts SUPPORT's complete write surface is exactly {complaints}.

## 4. UI

`/support` page: Timeline search tab + Complaints tab (open form +
progress/resolve with mandatory resolution). SUPPORT gains the nav entry.

## 5. Tests

Timeline masked-by-default (response NEVER contains the full token);
SUPPORT write-surface enumeration from the production RBAC map; complaint
journey via portal as SUPPORT (open → progress → resolve, resolution
required, audited); programme-scoped emptiness (TelcoLevelBound); no-oracle
404 cross-telco.

Out of scope: bureau lifecycle (licensing), self-exclusion (#45),
manual money actions of any kind.
