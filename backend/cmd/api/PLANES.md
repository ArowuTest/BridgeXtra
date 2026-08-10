# cmd/api — control-plane / data-plane split (BX-HIGH-012 Part B)

`cmd/api` serves one of three surfaces, selected by **`TCP_API_MODE`**:

| Mode | Serves | DB roles opened | Exposure |
|------|--------|-----------------|----------|
| `data` | channel API, recharge webhook, tenant `GET /v1/programmes` | `tcp_app` **only** | **Public** (internet-facing) |
| `control` | operator portal, config governance, programme→telco resolver | `tcp_app` + `tcp_operator` + `tcp_config` | **Internal only** (not on the public internet) |
| `all` (default) | both surfaces in one process | all of the above | single-service / local dev |

The split is the second stage of removing bypass-level DB privilege from the
internet-facing tier. **Stage 1** replaced the API's BYPASSRLS `tcp_worker` pool with
the least-privilege `tcp_config` role (migration `0076`). **Stage 2** (this file)
ensures a pure data-plane process never even *opens* `tcp_config`/`tcp_operator` and
never *mounts* the portal/config routes — verified by `buildmux_test.go` and the
`dataplane_fence_test.go` structural fence.

## Recommended production topology (best practice)

Run **three** things against the same database:

### 1. Migration job — `cmd/migrate` (owner DSN)
Runs once before the services start (pre-deploy / init). Applies migrations and
rotates role passwords from the environment. This is the ONLY process that opens the
owner/admin DSN.
- `TCP_ADMIN_DSN` = owner
- `TCP_APP_PASSWORD`, `TCP_OPERATOR_PASSWORD`, `TCP_CONFIG_PASSWORD`, `TCP_WORKER_PASSWORD` (from secrets)

### 2. Data-plane service — `cmd/api`, **public**
The internet-facing tier telcos call. Holds no credential that can bypass tenant RLS.
- `TCP_API_MODE=data`
- `TCP_API_SELF_MIGRATE=false`  (migration job already ran)
- `TCP_APP_DSN` (tcp_app)  — the only DB pool it opens
- `TCP_EGRESS_BLOCK_PRIVATE=true`
- Public networking: **yes**
- Do **not** set `TCP_CONFIG_DSN` / `TCP_OPERATOR_DSN` — a data-plane process never opens them.

### 3. Control-plane service — `cmd/api`, **internal only**
The operator portal + config governance. Operators (your staff) reach it over a
private network / VPN, **not** the public internet.
- `TCP_API_MODE=control`
- `TCP_API_SELF_MIGRATE=false`
- `TCP_APP_DSN` (tcp_app), `TCP_OPERATOR_DSN` (tcp_operator), `TCP_CONFIG_DSN` (tcp_config)
- `TCP_EGRESS_BLOCK_PRIVATE=true`
- Public networking: **no** — private service / internal ingress only. This is the
  single most important control: it shrinks the publicly reachable surface to the
  data plane, and keeps config-write + the cross-tenant resolver off the internet.

### The background worker — `cmd/worker`
Unchanged. It runs `tcp_worker` (BYPASSRLS / owner on managed PG) for cross-tenant
outbox dispatch and reconciliation. It is a batch process, **not** internet-facing.

## Why this shape

- **Blast radius:** a compromise of the public data-plane process yields only a
  tenant-RLS-scoped `tcp_app` connection — no config-write, no cross-tenant resolver,
  no BYPASSRLS. It cannot read or forge another tenant's money/PII.
- **Least privilege for the exposed tier:** config governance and the resolver — the
  higher-value operations — live only in the internal control plane behind operator
  RBAC and private networking.
- **Non-breaking:** `all` remains the default, so existing single-service and local
  setups keep working unchanged until you split the Render services.
