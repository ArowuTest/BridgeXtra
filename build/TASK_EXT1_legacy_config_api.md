# Builder Task Brief — EXT-1 (HIGH): close the legacy config-API authz bypass

**Priority:** TOP — before any M4d work. **Source:** external review + reviewer VR-25/VR-26 (verified at source).
**Severity:** HIGH (privilege escalation / broken access control). Structural defect; exploitable the instant a non-ADMIN operator credential exists (M4b enables that).

## The defect (verified)
`backend/cmd/api/main.go:96` mounts `cfgAdmin.Mount(mux, adminAuth)`. `AdminAuth.Wrap` (`configadmin.go:33`) authenticates via role-UNAWARE `ResolveCredential` and applies NO role or scope check, then routes to the SAME `configsvc` the portal uses. The five legacy routes:
```
POST /v1/admin/config/drafts        (mutation)
POST /v1/admin/config/{id}/submit   (mutation)
POST /v1/admin/config/{id}/approve  (mutation)
POST /v1/admin/config/{id}/activate (mutation)
GET  /v1/admin/config/active        (read — ALSO unscoped: cross-scope config read leak)
```
Since M4a/0017 added roles and M4b provisions RISK/FINANCE/OPS/SUPPORT into the same `admin_credentials` table, any active non-admin credential can mutate+activate money-config and read any scope's config through this door — bypassing BOTH the portal ADMIN-only gate AND the M4C-F1 scope model.

## Required fix
1. **Remove the legacy mutation routes entirely** (drafts/submit/approve/activate) — the portal supersedes them. Remove them from `configadmin.go` Mount, `main.go` wiring, and `api/openapi.yaml` (same-commit spec discipline).
2. **The legacy read** (`GET /v1/admin/config/active`): either remove it, or if a machine-readable config read is genuinely needed, route it through the portal's scoped read path (`OperatorScope`) — never unscoped.
3. **If any config automation is needed later** (not now): a separately-classified **service principal** with an explicit ADMIN role + scope, authenticated distinctly from human portal credentials, going through the SAME central RBAC chain — never a role-unaware header path.
4. **Delete now-dead code**: `AdminAuth`, `ConfigAdmin` handler, `ResolveCredential` (role-unaware) if nothing else uses them (grep first — confirm the channel/tenant path uses its own `TenantAuth.ResolveCredential`, a DIFFERENT function; do not break that).

## Recurrence-proof test (GATE-CHK-3 — this is the permanent fix)
Add a test asserting **no config-mutation path reaches `configsvc.Approve/Activate/Submit/CreateDraft` except through the RBAC-gated portal chain.** Concretely: enumerate all mounted routes on the API mux; assert every route whose handler can mutate config is registered via `mountRBAC` (i.e. present in `routeRoles`). A config-mutation route mounted outside the RBAC registry must fail the test. This makes the parallel-door class structurally impossible to reintroduce.

## Acceptance
- Legacy `/v1/admin/config/*` routes gone (or read-path scoped); spec updated same-commit; drift test green.
- New structural test proves no config mutation exists outside `mountRBAC`.
- A negative test: a non-ADMIN portal credential presented as `X-Admin-Key` to any former legacy path gets 404 (route gone) — and cannot mutate config by any route.
- Full `-race` suite + lint + gosec green; fresh-DB migrations green.

## Bundle with (config-integrity cluster, same push if clean):
- **EXT-3 / DAP-1:** column-scope the `config_versions` (and advances/funding_pools/programmes) UPDATE grants for tcp_app to match the tcp_worker discipline; add immutability trigger forbidding mutation of content/content_hash/domain/scope/version_no/created_by after submission; DB negative test.
- **EXT-4:** one shared strict JSON decoder (`DisallowUnknownFields` + trailing-doc + depth/size caps) used by every config validator; test the `fee_bp` vs `fee_bps` silent-typo case is now rejected.
