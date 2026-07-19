# M4e — Ops Workspace: Design Gate

Status: DESIGN — awaiting reviewer gate before build.
Scope source: task #41 + VR-35-F1 disposition (privileged subscriber-status
action folded in). SRS anchors: V2-OPS-00x ambiguity queues, EDG-005/007
(UNKNOWN fulfilment), M3B-F1 (PARKED reversals for operators), V2-SIM-002
(fault catalogue), V2-SUB-009/EDG-030 context (status gates; self-exclusion
itself is task #45, NOT in M4e).

Principles carried: no hardcoding (every threshold = admin-config record with
validator + seed); no stubs; RBAC via `mountRBAC`/`routeRoles` (GATE-CHK-3
covers new routes structurally); OpenAPI in the same commit; grant changes
gate on the FULL `-race ./backend/...` suite (VR-34 standing correction).

---

## 1. Fulfilment ambiguity queue (UNKNOWN + stale-SENT)

**Why**: EDG-005/007 leave attempts in UNKNOWN (credit may have happened) or
stale-SENT (no response, resolver nudged it to UNKNOWN on next enquiry tick).
The resolver worker already re-enquires (`Attempts.DueEnquiries`, SKIP
LOCKED). What ops lacks is *visibility* — which advances are ambiguous, for
how long, at what exposure — and one *safe* action.

**Read surface** `GET /v1/portal/ops/fulfilments`:
attempts in state UNKNOWN, or SENT older than the stale threshold; joined to
advance (face value, subscriber token, state) with age, enquiry_count,
next_enquiry_at. Filter by state; ordered oldest-first. Worker-pool read with
mandatory `OperatorScope` (M4c pattern); cross-scope = no-oracle 404.

**Action** `POST /v1/portal/ops/fulfilments/{id}/enquire-now`:
sets `next_enquiry_at = now()` on the claimed attempt (app-pool tenant tx,
load-scoped-then-act). The resolver does the actual enquiry on its next tick —
the portal never talks to the telco and never resolves state itself.
**Deliberately NO manual confirm/fail**: resolving an attempt moves money
(activates the advance / releases reservation) and must only follow telco
evidence through the resolver. If a manual override is ever needed it is a
separate reviewer-gated design with its own evidence requirements.

**Config** (`ops.queues` domain, new, seeded + validated):
`stale_sent_after_seconds` (queue-listing threshold; the resolver's own
scan cadence is already config), `max_page_size`.

## 2. PARKED reversals queue

**Why**: M3B-F1 parks colliding reversals with `park_reason` "recorded for
operators" — the queue is that promise delivered. Reversals auto-apply when
the original arrives; a parked one with a collision reason (e.g.
`REOPEN_HEADROOM_COLLISION`) sits until conditions change.

**Read surface** `GET /v1/portal/ops/reversals`: PARKED rows with
park_reason, amount, original/reversal source events, age. Same scope rules.

**Action** `POST /v1/portal/ops/reversals/{id}/retry`:
re-runs the SAME guarded apply used by ingest (`applyReversalGuarded` —
reuse, not reimplement): if the collision has cleared it applies atomically;
if not, park_reason is refreshed and the queue shows the current blocker.
Idempotent, evidence-preserving, no new money semantics.

## 3. Privileged subscriber-status action (VR-35-F1)

**Why**: `blocked_statuses` is consumed by three live gates but no production
writer can produce any status but 'ACTIVE'. This gives the "out-of-band
privileged operation" a real, audited existence — and its migration is the
first designed use of 0025's add-the-grant-when-the-writer-lands discipline.

**Model** — maker-checker, write-off pattern (NOT a raw update endpoint):
- New table `subscriber_status_actions` (mig 0026):
  `action_id, telco_id, subscriber_account_id, from_status, to_status,
  reason (required), requested_by, state
  (REQUESTED|APPROVED|REJECTED|APPLIED), approved_by, decided_at, applied_at`.
  Schema-enforced two-actor: `CHECK (approved_by IS NULL OR approved_by <>
  requested_by)` + approver-fixed-once trigger (guardrail re-arm pattern).
- Transitions allowed in M4e: ACTIVE→BARRED, BARRED→ACTIVE (unbar),
  ACTIVE→CLOSED, BARRED→CLOSED. CLOSED is terminal (no reopen; a returning
  subscriber gets a NEW account row/identity period — DD-06 decides periods).
  SELF_EXCLUDED is NOT settable here: it is the customer's own channel act
  (task #45); an ops override of self-exclusion would undermine EDG-030.
  The transition set lives in a validated config record
  (`ops.status_actions`, seeded with exactly the above) — no hardcoding, and
  tightening it later is config, not code.
- Apply step (on approve): single UPDATE of `subscriber_accounts.status`
  via app-pool tenant tx + audit row; the offer/confirm/engine gates then see
  it immediately (that is the point — the gates finally have a producer).
- **Grant change** (same mig 0026): `GRANT UPDATE (status) ON
  subscriber_accounts TO tcp_app;` — probe flips
  `{"subscriber_accounts","status", true}`, `msisdn_token`/`effective_from`
  stay false. Full-suite gate (standing correction).

**RBAC**: request = OPS, RISK, ADMIN; approve = same roles, distinct actor
(schema-enforced regardless of role). Reads for all oversight roles.
Routes: `GET /v1/portal/ops/status-actions`,
`POST /v1/portal/ops/status-actions` (request),
`POST /v1/portal/ops/status-actions/{id}/approve|reject`.

## 4. Non-engineer fault demo

**Why**: prove to a non-engineer (and to MTN) that the platform survives the
fault catalogue — from the portal, no terminal.

**Mechanism**: the simulator's faults are msisdn-token-shaped (`FAIL`,
`TIMEOUT`) plus `HoldEnquiries`. A demo run drives the REAL origination
usecase (channel API path) against a fault-shaped demo token, then renders
the artifact chain as it evolves: offer → advance PENDING_FULFILMENT →
attempt SENT/UNKNOWN → resolver enquiry → CONFIRMED/FAILED terminal state,
with ledger postings and (for TIMEOUT) the EDG-005 "credit happened, platform
didn't hear" recovery visible.

**Surface**:
- `POST /v1/portal/ops/demo/run` — body: scenario ∈ config-seeded catalogue
  (`happy_path`, `hard_fail`, `timeout_unknown`). Executes origination with
  the scenario's token; returns run id.
- `GET /v1/portal/ops/demo/runs/{id}` — the artifact chain (offer, advance,
  attempts, journal entries, SMS log from `/sim/sms`), polled by the UI.
**Guards**: `ops.fault_demo` config record — `enabled` (bool) +
`allowed_telcos` (seeded `["SIM_NG"]` only) + the three scenario token
suffixes; validator refuses empty allowed_telcos when enabled. Handler
refuses any telco not allowlisted → structurally cannot run against a real
telco integration. Demo runs are ordinary advances in the demo telco's
tenant — no special-cased money paths, nothing to un-stub later.
RBAC: run = OPS, ADMIN; read = oversight roles.

## 5. UI (portal, existing Next.js patterns)

`/ops` workspace, four tabs mirroring M4c/M4d idioms:
1. **Fulfilments** — ambiguity queue table (age-highlighted), enquire-now
   button, honest empty state ("no ambiguous fulfilments").
2. **Reversals** — parked queue with park_reason chips, retry button.
3. **Subscribers** — status-action list + request form (reason mandatory),
   pending-approval banner for the second actor (re-arm UI pattern).
4. **Fault demo** — scenario picker, run button, live artifact-chain view.

## 6. Slicing & gates

- **M4e-1**: queues (read + enquire-now + retry) — repo/usecase/handler/UI.
- **M4e-2**: mig 0026 + status-action maker-checker journey (request→approve→
  gate-visible) + probe flip. FULL-suite gate.
- **M4e-3**: fault demo backend + UI + demo E2E (timeout_unknown proves
  EDG-005 end-to-end through the portal surface).
Each slice: OpenAPI same-commit, RBAC matrix auto-derived from `RBACRoutes()`,
GATE-CHK-3 covers mounting, gofmt+race+lint v2+gosec, push at green.

## 7. Explicitly out of scope

Self-exclusion channel path (#45, R1-MUST pre-pilot, customer-facing);
telco delta feed / identity-period closes (#46, DD-06); manual fulfilment
resolution override (needs its own evidence design); bureau SENT lifecycle
(licensing).
