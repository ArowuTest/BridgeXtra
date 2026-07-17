# Deployment — DEMO environment (Render, Frankfurt)

**Stood up:** 17 Jul 2026, after G1 gate pass. Purpose: walking-skeleton demo
against the simulator. NOT production: see "Before production" below.

## Topology

| Component | Render resource | Plan | Notes |
|---|---|---|---|
| Postgres 16 | `bridgextra-db` (`dpg-d9d7d3m7r5hc73fhfreg-a`) | basic_256mb | owner role `bridgextra` (CREATEROLE, no superuser); IP allow-list OPEN for demo — tighten before production |
| API | `bridgextra-api` → https://bridgextra-api.onrender.com | free | boot-time self-migration + role-password rotation; health `/healthz` |
| Simulator | `bridgextra-sim` → https://bridgextra-sim.onrender.com | free | the fake telco (V2-SIM); free instances spin down on idle |
| Worker | `bridgextra-worker` | starter | dispatcher + resolver loop; no free tier for background workers (owner-authorized lowest paid) |

Repo: https://github.com/ArowuTest/BridgeXtra (private), auto-deploy from `main`.
CI (GitHub Actions) is the merge gate; Render deploys what lands on main.

## Role model on managed Postgres

BYPASSRLS is superuser-only on managed clusters, so (per 0001's
insufficient_privilege fallback, proven by the serial CI test):

- `tcp_app` — RLS-enforced application role; strong password set via
  `TCP_APP_PASSWORD` env (rotated at boot by `platform/dbroles`).
- `tcp_worker` — exists for grants; NOT used by deployed workers.
- worker/admin pools connect as the database owner (`bridgextra`), which
  ENABLE-RLS does not apply to — same dispatch capability as BYPASSRLS.

Secrets live ONLY in Render env vars (and the owner's local credentials
store) — never in this repo (V2-SEC-005).

## Operator jobs (Render shell or any host with the DSNs)

```
./worker -invariants   # BC-3 sweep: exit 1 on any violation
./worker -recon        # fulfilment reconciliation: exit 1 on any break
```

## Live verification (17 Jul 2026, first deploy)

The full money story ran against the DEPLOYED stack, not localhost:

1. Boot self-migration applied all 6 migrations from zero (managed-owner
   fallback path); role passwords rotated from env.
2. Credentials provisioned (bootstrap right, hash-only): 1 channel key for
   SIM_NG, 2 admin actors (maker + approver).
3. `telco.adapter` re-pointed at https://bridgextra-sim.onrender.com through
   the FULL governed flow — draft → submit → approve (distinct actor) →
   activate, version 2. Zero code changes: the no-hardcoding directive
   working as designed.
4. Channel flow over the public API: offer ladder (governed product config)
   → confirm ₦50 → **201 ACTIVE** (deployed API called deployed simulator)
   → idempotent replay **200 same advance** → recovery event ₦50 →
   **CLOSED, outstanding ₦0**.
5. Deployed worker's dispatcher drained the outbox (FulfilmentConfirmed +
   RecoveryApplied both published, per-aggregate FIFO).
6. Operator jobs against the deployed DB: `-invariants` → "all invariants
   hold — the ledger balances at this instant"; `-recon` → "reconciliation
   clean across all active telcos/programmes".

## Before production (tracked, non-blocking for the demo)

1. Close the DB IP allow-list to known egress CIDRs.
2. Private repo + branch protection on main (CI required check).
3. KMS-backed secrets rotation cadence; drop the owner-DSN admin path once a
   dedicated migration identity exists (M4 RBAC).
4. Real telco adapter endpoint + credentials replace the simulator via
   governed `telco.adapter` config — no code change by design.
