# Interim Security Review — M4a Portal Auth + RBAC (ba8f66c, gosec fix c519497)

**Reviewer:** Claude (Fable 5), lead reviewer · **Date:** 18 Jul 2026
**Type:** interim review (NOT the G4 gate — G4 is the M4-complete adversarial gate). M4a is the highest-risk surface in the platform (session auth, CSRF, deny-by-default RBAC on money-config), so it gets a source review now rather than waiting.
**Method:** full read of `portal.go`, `repo/portalsessions.go`, migration 0017, `portal_test.go`; independent `-race` run of the portal auth/RBAC/CSRF/logout/maker-checker tests (all PASS in reviewer's hands).

## Verdict: architecture is strong; **4 findings (3 MEDIUM, 1 LOW)** to fold — M4A-F1 before any further portal work, the rest across M4.

### Verified correct at source
- **Session tokens**: 256-bit `crypto/rand`, stored **sha256-hashed** — a DB leak never yields a live session. Lookup by hash + `revoked_at IS NULL` + `expires_at > now()`, so expired/revoked/absent all collapse to `ErrNotFound` → identical 401, **no oracle**.
- **CSRF**: per-session secret, sha256-stored, `subtle.ConstantTimeCompare`, enforced on every non-GET/HEAD. A live session without the header still gets 403 (test-proven).
- **Cookie**: `HttpOnly`, `Secure` **unconditional** (the G124 fix — and it is the *right* call: a security attribute must not be env-disarmable, same BC-5 arm-or-refuse principle we enforced on treasury), `SameSite=Strict`, `Path=/v1/portal`, `MaxAge`.
- **Deny-by-default RBAC**: a route absent from `routeRoles` is refused for everyone (`ok==false` → 403) with an error log — proven by the matrix test (every route × every role: allowed→not-401/403, disallowed→403; no-session→401).
- **Maker-checker now flows through real sessions** — the config actor is the authenticated session actor, not a spoofable header. This is a genuine **security upgrade** over the header-based admin API.
- **Logout revokes server-side** (old cookie 401s afterward — proven). Role snapshot at login is documented.
- **Frontend**: same-origin proxy keeps the cookie first-party; `npm audit` 0 vulns after the Next 15.5.20 bump + postcss override.

## Findings

### M4A-F1 — MEDIUM (security) — a revoked/offboarded admin keeps a live session up to 8h
`PortalSessions.Resolve` checks only the **session row** (`revoked_at`, `expires_at`). It does **not** re-check the underlying `admin_credentials.status`. So setting a credential to non-ACTIVE (offboard, compromise, role change) stops *new logins* (`ResolveCredentialWithRole` filters `status='ACTIVE'`) but leaves that actor's **existing session working for the full 8h TTL** — with power to draft/submit/approve/activate money-affecting config. This is exactly the live-session-revocation discipline we enforced elsewhere (kill live credentials on offboard). **Fix (1-line):** `Resolve` (and `VerifyCSRF`) join `admin_credentials` and require `status='ACTIVE'` — freshest, single place; OR an explicit revoke-all-sessions-for-actor wired to offboard/role-change. The join is cleaner. **Fold before further portal work** (before M4b ships more admin power on these sessions).

### M4A-F2 — MEDIUM (structural / drift) — route→roles identity lives in three independent copies
The same route→roles fact is maintained in three places that nothing keeps in sync: (1) the `routeRoles` map, (2) the `mux.Handle(...)` + `rbac("literal", ...)` mounts (the rbac string must equal both the mux pattern and a map key), (3) the test's hand-listed `routes` slice. Deny-by-default is proven for today's 6 routes, but nothing structurally prevents route #7 being mounted with an `rbac()` string that mismatches its mux pattern (checking the wrong route's roles), or added without a test row. Same class as M3D-F1 (correct-now, unenforced). **Fix:** collapse to one source — a `mountRBAC(mux, pattern, handler)` helper that registers the mux **and** requires `routeRoles[pattern]` to exist (fail at mount if absent), and drive the test matrix by iterating `routeRoles` so it cannot drift from production.

### M4A-F3 — MEDIUM (forward-looking; design in NOW, don't retrofit) — RBAC is role-only, no telco/programme scope
The session carries `Role` but **no telco/programme scope**. Fine for M4a (global platform config, no tenant data exposed). But V2-UI-001 / TEN-006 require portal authz = functional permission **AND** permitted telco/programme scope, and M4b+ will surface finance/ops/support screens over tenant-scoped data. The scope dimension must be designed into `portal_sessions` + the authz check **before** any tenant-scoped portal data ships — cheaper as a column now than a retrofit onto live screens. This is the property G4 will attack (cross-tenant fetch through the portal).

### M4A-F4 — LOW (hardening) — no rate-limit/lockout on /v1/portal/login
`/v1/portal/login` has no throttle or lockout (V2-SEC-011: rate limiting by client/session/operation). Mitigated today by 256-bit key entropy (brute force infeasible), so LOW — track for M5 hardening / the edge gateway.

## Gate status & housekeeping
- **NOT gate-passed** — this is interim. The formal **G4** (adversarial cross-role + cross-tenant attack against the running backend, MSISDN masking, audited step-up) runs at M4 completion.
- CI: run #12 was red on gosec G124 only; **c519497 fixes it correctly** (Secure unconditional, knob removed, test asserts all 3 flags). Backend/migrations/coverage/other quality steps already passed. New run expected green.
- **Date correction:** today is **18 Jul 2026**, not the 20th (builder narration drifted 2 days). The **DEON ruling has NOT landed** — A-5 re-review remains genuinely pending, fires on the actual ruling text on the 20th.

## Recommendation
Builder: fold **M4A-F1 now** (1-line join — a stale admin session on money-config is the one that shouldn't wait); fold **M4A-F2** and design in **M4A-F3** as part of M4b; track **M4A-F4** for hardening. Then continue M4b. I re-verify M4A-F1/F2/F3 at source and run the full adversarial pass at **G4**.
