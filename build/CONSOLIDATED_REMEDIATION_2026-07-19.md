# Consolidated Remediation Package — External Audit + Reviewer G4

**Commit:** `14e0802` · **Date:** 19 Jul 2026 · **Compiled by:** lead reviewer (Fable 5)
**Sources merged:** (1) external Deep Engineering Audit 2026-07-19 (AUD-P0/P1/P2); (2) reviewer G4 gate (G4-F1/F2); (3) prior standing items.

## Reviewer position (read first — honest)

I independently verified the external auditor's money-path findings **at source**. **I concur with all of them, and several are defects I did not surface in ~40 review cycles.** Root cause of my miss: my M4 reviews were portal-surface-focused, and I accepted the M1b-5 reconciliation as "walking-skeleton, revisit later" without re-auditing it under an adversarial lens. The auditor also caught that **my own VR-32 SSRF remediation was under-counted** — I enumerated three outbound doors and missed reconciliation as the fourth (my egress package header literally says "three doors").

This is the **third independent audit** (external HIGH at VR-25; builder self-audit; now this) to find distinct real issues at source. That is conclusive: a single continuous reviewer is **necessary but not sufficient**. The independent professional pen-test is no longer a recommendation — it is a hard prerequisite, and this package is evidence of why.

None of the confirmed findings is a hole in the append-only ledger, the double-entry balance invariant, the tenant-isolation/authz model, or the money representation — those remain sound (auditor agrees, §4). The defects are on the **idempotency-equivalence, reconciliation, consent-evidence, and production-IAM** edges. Real, pre-pilot, but not a rewrite. Auditor's verdict "continue on this codebase, no rewrite" — I agree.

---

## P0 — PRE-PILOT BLOCKERS (money/command integrity + security)

Every P0 below **verified at source by me** unless marked. Fix before any customer-like pilot.

### R-P0-1 (=AUD-P0-001) — Advance-confirm idempotency does not enforce request equivalence — **CONFIRMED**
`replayByIdemKey` (origination.go) blind-loads the advance by `(telco_id, idempotency_key)` with **zero** comparison of offer/programme/subscriber/body. The generic `idempotency_records` (request-hash) abstraction exists but is not wired into confirm. Same key + different request → silently returns the original advance marked `Replayed`. Violates **API-002/003** ("original result for *valid* retries" — a different body is not valid) and **ADV-002/006**.
**Fix:** canonical confirm-request representation → hash → atomically claim `(telco, operation, idem_key)` in the first durable tx. Duplicate: same hash → replay exact original response (status+route incl.); different hash → typed conflict + security-audit + alert + `DIVERGENT_DUPLICATE`-class. **Tests (mandatory):** same-key/same-body pre+post completion; same-key different offer / programme / token; concurrent same-key/same-body and same-key/different-body; crash-after-commit-before-response.

### R-P0-2 (=AUD-P0-002) — Recovery dedup accepts divergent payloads — **CONFIRMED**
`recovery.go` dedups on `(telco_id, source_event_id)` only; on conflict it loads the existing row and "touches nothing" — no compare of amount/currency/token/occurred_at/payload-hash. Also the replay response returns only `{ID, State, Replayed}`, **not** the promised original `Applied/Excess/AdvanceClosed`. A telco can reuse an event ID with a changed amount and it is silently a replay.
**Fix:** persist a canonical source-event hash + original attributes; on duplicate compare; divergent → quarantine + alert + `DIVERGENT_DUPLICATE` break; return the exact original outcome (store it, or deterministically reconstruct allocations/suspense/closure). Tests mirror R-P0-1.

### R-P0-3 (=AUD-P0-003) — Reconciliation ignores currency — **CONFIRMED**
`recon.go`: `platformRecord` has **no currency field**; match compares only `abs64(p.FaceValueMinor - tr.FaceValueMinor)`. NGN 1,000 vs GHS/USD 1,000 → `MATCHED`.
**Fix:** select+store platform currency in the recon working record; compare currency **before** amount; `BREAK_CURRENCY_MISMATCH` with both values; ISO alpha-3 validation + programme/currency compatibility.

### R-P0-4 (=AUD-P0-004) — Reconciliation difference arithmetic can overflow — **CONFIRMED**
`abs64(p.FaceValueMinor - tr.FaceValueMinor)` on **external telco JSON**: the subtraction can overflow before `abs64`, and `abs64(math.MinInt64)` overflows again. (Note: the M2 ingest plausibility ceiling does **not** cover this recon path.)
**Fix:** validate positive/credible ranges before compare; checked subtraction or bound-compare without subtracting; reject/quarantine beyond configured ceilings; tests at MinInt64/MaxInt64/negative/extreme-delta.

### R-P0-5 (=AUD-P0-005) — Reconciliation bypasses the shared SSRF egress client — **CONFIRMED (extends reviewer VR-32; my miss)**
`recon.New` builds a plain `http.Client{Timeout:10s}` to a config-driven endpoint — **not** `egress.SafeClient`. This is the **fourth** outbound door; VR-32 closed three (adapter/featureingest/notify) and missed this one.
**Fix:** route recon through `egress.SafeClient`; update the egress package header to four doors; add per-telco/programme production endpoint allowlist; disable/revalidate redirects; TLS policy; tests (link-local, DNS-rebinding, redirect, private/non-approved target). **Fold the allowlist into the shared egress prod-hardening (task #44).**

### R-P0-6 (=AUD-P0-006) — Reconciliation is not production-grade / not run-idempotent — **CONCUR (architectural; consistent with M1b-5 walking-skeleton status)**
Current recon: simulator-specific `/sim/transactions`; rescans all history each run; no period/manifest/source-hash/control-totals; writes new rows each rerun; no supersession; no duplicate-source rejection; loads `auto_resolve` but never uses it; fulfilment-only. Maps to **REC-001..013, FIN-004/005**.
**Fix:** immutable recon-run header; manifest + source hash + record count + monetary control total; period/watermark; deterministic input dedup; one canonical item per run/match-key; rerun/supersession; currency-aware; extend to recovery/settlement/bureau layers; maker-checker break resolution; signed evidence pack. **Large — own workstream; the currency/overflow/SSRF fixes (P0-3/4/5) land first as point-fixes, then the framework.**

### R-P0-7 (=AUD-P0-007) — Consent is inferred, not proven by channel evidence — **CONFIRMED**
Confirm request carries only programme/offer/token; backend hardcodes `Channel:"USSD"` (origination.go:460) and builds terms from the offer. It proves *which offer existed*, not that *those exact terms were rendered and accepted*. No channel/session ID, disclosure template+version, rendered-text hash, locale, acceptance event ID, or telco evidence signature. Regulatory conduct gap (**REG-002, PRD-002/003, EDG-028**).
**Fix:** short-lived signed disclosure-snapshot token minted at offer/menu generation; require it + channel/session evidence on confirm; retain exact disclosure text/template/locale/total-cost-representation/acceptance timestamp; set responsibility+format in the telco interface contract; dropped-session + duplicate-confirm evidence tests. **Coordinate with the telco data contract (DD-06) — some evidence originates telco-side.**

### R-P0-8 — Rate limiting absent platform-wide — **CONFIRMED (reviewer G4-F1; = part of AUD-P0-008 + broader)**
No throttle/lockout/circuit-breaker on any inbound surface: portal `/login` (AUD-P0-008 login-hardening; mitigated by 256-bit key entropy) **and** the telco-facing channel API (offers/advances/recovery — higher stakes: hammering/looping telco, cred-stuffing, no backstop). **SEC-011, TEN-004, SCL-009 = R1-MUST.** Adapter also lacks an outbound circuit breaker (TEN-004; the INV-009 no-retry is correct, but a down telco isn't circuit-broken) — related, lower.
**Fix:** rate-limit middleware by client/telco/subscriber-token/session/operation (edge gateway or app middleware); login lockout/backoff; adapter circuit breaker. **Note: I rank the SSO/MFA portion of AUD-P0-008 as P1 (see R-P1-8), keeping rate-limiting as the P0 slice.**

---

## P1 — PRODUCTION-READINESS (before certification / broad pilot)

Severity reconciled; `[verified]` = I checked at source, `[concur]` = architectural absence, clearly true.

- **R-P1-1 (=AUD-P1-001, my VR-22 note b, EXT-2) — role/scope is login snapshot** `[verified]`. Status is live-checked (revocation closed) but role/scope stay from login → downgrade doesn't take effect until logout/expiry. **Fix:** bind session to credential ID + `authz_version`; re-resolve or revoke-all on assignment change.
- **R-P1-2 (=AUD-P1-002) — operator reads rely on BYPASSRLS worker pool** `[verified — my own M4c pattern]`. `OperatorScope` is a compile-time app boundary but the DB is not the final arbiter; a future dynamic-SQL/injection bug bypasses it. **Fix:** dedicated read-only operator role with DB-enforced scope (RLS via session claims / security-barrier views / audited security-definer fns). *Reviewer note: I accepted this as "structural app boundary" through M4c-F1; the auditor is right that defense-in-depth wants a DB backstop too. Upgrade from accepted-risk to P1.*
- **R-P1-3 (=AUD-P1-003) — public API holds migration-owner + worker creds** `[verified]`. Internet-facing API self-migrates with admin DSN, rotates creds, keeps worker pool. **Fix:** separate migration job; API gets app role only; dedicated config + operator-read roles; no owner creds in runtime API env. *Ties to R-P1-2.*
- **R-P1-4 (=AUD-P1-004) — telco adapter lacks partner auth** `[concur]`. No mTLS/OAuth/signing/cert-rotation/allowlist. **Fix:** adapter credential profiles + secret-manager + mTLS/signing + cert rollover + partner allowlist + rotation audit. **Telco-cert gate (Gate C).**
- **R-P1-5 (=AUD-P1-005) — telco credential lifecycle incomplete** `[concur]`. Hashes exist; need expiry/not-before, overlapping rotation, endpoint/op scopes, last-used/source-net telemetry, quotas, cert/IP binding, revocation audit.
- **R-P1-6 (=AUD-P1-006) — NIN/KYC verification flags absent from feed contract** `[concur]`. Cannot enforce a `NIN_VERIFIED` gate. **Fix:** versioned `nin_linked`/`nin_verified`/`sim_registration_status` + source ts/quality (flags only, never raw NIN per SOR-003). **DD-06.**
- **R-P1-7 (=AUD-P1-007) — feed authenticity/completeness not established** `[concur]`. Hashing proves storage, not source authenticity. **Fix:** signed/encrypted manifests, sequence/batch ID, expected count + control totals, producer key identity, ack, missing/duplicate-file alerts, authenticated transport. **DD-06 + Gate C.**
- **R-P1-8 (=AUD-P0-008 SSO/MFA slice, downgraded to P1) — portal IAM not production-grade** `[verified]`. API-key-in-body → 8h session; no SSO/OIDC/SAML, MFA/step-up, central offboarding, device/session mgmt, idle timeout, priv-action reauth, cred expiry/rotation. **Fix:** external IdP; roles/scopes as claims; MFA+step-up for finance/config/risk. *Reviewer severity: P1, not P0 — a controlled pilot can run on seeded creds + rate-limiting (R-P0-8); SSO/MFA is a production-IAM item, must land before broad/regulated launch. If the pilot is customer-facing regulated from day 1, escalate to P0.*
- **R-P1-9 (=AUD-P1-008) — unmatched recovery may be unbooked money** `[verified]`. `UNMATCHED` posts no ledger/suspense; if the telco event means real garnishment, cash exists with no entry. **Fix:** define contract semantics; where receipt is economically real, post to a telco-level unidentified-recovery clearing/suspense immediately, reclassify after investigation. **COL-004 / FIN-008.**
- **R-P1-10 (=AUD-P1-009) — portal CI absent** `[verified]`. Merge-blocking CI is backend-only. **Fix:** required portal job: `npm ci`, non-interactive ESLint, typecheck, Next build, unit/component, Playwright login/RBAC/scope/maker-checker, a11y, dep/SBOM.
- **R-P1-11 (=AUD-P1-010) — portal lint not configured** `[verified]`. `next lint` opens interactive prompt. **Fix:** direct ESLint + Next/security plugins, fail-on-warn.
- **R-P1-12 (=AUD-P1-012, =EXT-5) — config hash on raw bytes but stored as JSONB** `[concur]`. Whitespace/key-order variants → hash may not reproduce from stored JSONB. **Fix:** canonicalize JSON before hash+store; verify on read/replay; or retain canonical raw bytes + JSONB projection. *(EXT-5, open since VR-25.)*
- **R-P1-13 (=AUD-P1-013) — draft-create lacks durable audit** `[concur]`. Submit/approve/activate audited; `CreateDraft` not. **Fix:** audit create + sensitive-config read/export + rejected-approval + failed-authz + rollback/scheduled.
- **R-P1-14 (=AUD-P1-016) — money crosses browser API as JS `number`** `[verified]`. `amount_minor: number` loses precision above 2^53 (aggregated settlement/exposure). **Fix:** int64 money as JSON decimal strings; BigInt/decimal parse; range tests.
- **R-P1-15 (=AUD-P1-017) — server lifecycle/timeout hardening incomplete** `[verified]`. Only `ReadHeaderTimeout`. **Fix:** read/write/idle timeouts, max header, graceful drain, panic recovery, shared body-size policy.
- **R-P1-16 (=AUD-P1-018 = reviewer G4-F2 = EXT-7) — liveness/readiness conflated** `[verified]`. `/healthz` DB-pings + labeled liveness → DB blip risks restart loop. Escaped the VR-28 EXT fold. **Fix:** `/livez` (no deps) + `/readyz` (DB) + `/version` (commit/schema).
- **R-P1-17 (=AUD-P1-019) — HTTP decoding not uniformly strict** `[verified]`. Config validators are strict (EXT-4); other handlers use ordinary decoders (bounded by MaxBytesReader, but accept unknown/trailing). **Fix:** shared strict request decoder (content-type, size, DisallowUnknownFields, single-doc, safe errors) across all handlers.
- **R-P1-18 (=AUD-P1-020) — outbox consumers incomplete** `[concur]`. Only fulfilment-confirmed has a real consumer; failed/unknown/recovery-applied log-only. **Fix:** explicit consumers + contracts + replay/DLQ for notifications/analytics/bureau/finance per R1.
- **R-P1-19 (=AUD-P1-021) — no durable scheduler** `[concur]`. Recon/delinquency/invariants/breaks are command flags. **Fix:** scheduler with immutable job runs, checkpoints, ownership, SLA, overlap-prevention, per-telco calendars, completeness alerts. (Ties R-P0-6.)
- **R-P1-20 (=AUD-P1-023) — browser security headers absent** `[concur]`. No CSP/HSTS/frame/referrer/permissions/nosniff. Especially important since CSRF token is in sessionStorage. **Fix:** full header set in `next.config.mjs` / BFF.
- **R-P1-21 (=AUD-P1-024) — production observability not implemented** `[concur]`. Structured logs only; no traces/metrics/SLO/queue-lag/latency/break/invariant alerts. **M5 must prove, not document** (OBS-001..012).
- **R-P1-22 (=AUD-P1-011) — config lifecycle partial** `[verified — TS omits SCHEDULED]`. Schema/OpenAPI model scheduled/rejected/rolledback; service+portal expose draft→submit→approve→activate. **Fix:** either implement scheduled/reject/rollback/canary/diff, or **remove unsupported states from the R1 contract and explicitly defer** (my lean: defer with a written note; don't ship dead enum states).
- **R-P1-23 (=AUD-P1-014/015) — scope model coarse + global readable by all roles** `[concur]`. Single scope string (`*`/one telco/one programme); `PermitsRead` lets any role read `global`. **Fix:** many-to-many role grants across telco/programme/domain/data-class + domain/classification-aware read perms (some global domains hold endpoints/commercial/security detail). *Note: this revisits my M4a-F3 scope design — accepted for M4, but the auditor is right it's too coarse for real teams. Roadmap, not pilot-blocking for a single telco.*

---

## P2 — COMPLETENESS / CONDUCT / MAINTAINABILITY

Concur in principle (mostly absence-of-feature, low individual risk). Grouped for the builder; **not pilot-blocking individually** but several feed regulatory posture.

- **R-P2-1 (=AUD-P2-002) — support token in URL query** `[verified: r.URL.Query().Get("token")]`. Leaks to logs/history. **Fix:** POST body; prohibit token logging; keyed lookup index. *(Small, do it early — it's a live PII-in-logs edge.)*
- **R-P2-2 (=AUD-P2-004) — complaint actor recorded as `channel:*` not operator** `[verified]`. Non-repudiation gap. **Fix:** pass+record `sess.Actor`; keep channel separate. *(Small.)*
- **R-P2-3 (=AUD-P2-005) — complaint error returns `err.Error()`** `[concur]`. Internals leak. **Fix:** typed validation errors + safe public messages; details to logs w/ correlation ID.
- **R-P2-4 (=AUD-P2-017) — minor-unit grouping helper overflows at MinInt64** `[concur]`. Money utils should be boundary-correct + tested.
- **R-P2-5 (=AUD-P2-010) — recon doesn't classify duplicate telco success records** `[concur]`. Folds into R-P0-6 framework.
- **R-P2-6 (=AUD-P2-018) — audit needs tamper-evident export/retention** `[concur]`. Append-only grants help but DB owner can alter. **Fix:** WORM/hash-chain/signed batches + retention/legal-hold + independent verify. **SEC-005.**
- **R-P2-7 (=AUD-P2-019) — source IP not proxy-safe** `[concur]`. `RemoteAddr` incl. port, no trusted-proxy policy. **Fix:** derive client IP from known proxies; preserve chain.
- **R-P2-8 (=AUD-P2-016/022/023) — contract drift + toolchain pins + doc drift** `[verified: golangci/gosec/govulncheck were `latest` (I fixed the local lint to v2; CI pins still needed); docs reference stale migration counts]`. **Fix:** generate TS client from OpenAPI + conformance tests; pin tool versions/checksums + SBOM; regenerate docs from release commit.
- **R-P2-9 (=AUD-P2-020) — simulator/demo deployment isolation** `[concur]`. Demo is config+role-gated (my VR-39 verified allowlist), but prod should build/mount sim + fault scenarios only in non-prod, or a separate service.
- **R-P2-10 (=AUD-P2-003) — stable subscriber token is still PII** `[concur]`. **Fix:** keyed HMAC lookup token, rotation/versioning, reveal/search audit, retention + DSR policy.
- **R-P2-11 — deferred-completeness (=AUD-P2-001/006/007/008/009/011/012/013/014/015/021)**: portal workspaces below their M4 plan depth (risk oversight, finance generation/close, ops unmatched queue, support recoveries+consent in timeline); complaint register thin for regulatory; **customer self-service conduct (self-exclusion/opt-out/DSR/appeal/DND)** = **overlaps #45 self-exclusion, R1-MUST**; airtime hardcoded (`ProductType:"AIRTIME_ADVANCE"` — fine for R1); no USSD session orchestrator (telco-owned? — set in contract); settlement/treasury operational lifecycle; portfolio guardrails narrow; **cross-telco serial default = DD-12 decision, not omission**; model governance not institutional; prod infra/DR evidence (=M5). **These are the honest M4-depth + M5 + business-rail gaps — the auditor is right that "M4 formally complete" is a narrative, not a state. Track as roadmap; none blocks the P0 pilot-integrity fixes.**

---

## Merged priority for the builder

**Sprint 1 — P0 money/command integrity (pre-anything-customer-like):**
1. R-P0-1 confirm request-hash idempotency + adversarial tests.
2. R-P0-2 recovery event-hash dedup + exact-original-response + DIVERGENT_DUPLICATE.
3. R-P0-3 + R-P0-4 recon currency-before-amount + safe arithmetic + ceilings.
4. R-P0-5 route recon through egress.SafeClient (+ allowlist, task #44).
5. R-P0-8 rate-limiting (login + channel) + adapter circuit breaker.
6. R-P0-7 consent/channel disclosure-evidence token (coordinate DD-06).
7. Re-run full invariant + `-race` pack after duplicate/timeout/crash tests. **Reviewer re-verifies each at source + own runs (this is Gate A).**
Then **R-P0-6** recon framework as its own workstream.

**Sprint 2 — P1 production-readiness (Gate B/C/E foundations):** R-P1-1..3 (session authz-version + DB role split + operator read role), R-P1-16 health split, R-P1-15/17 server+decoder hardening, R-P1-10/11 portal CI+lint, R-P1-20 security headers, R-P1-14 money-as-string, R-P1-12 config-hash canonicalize, R-P1-4/5/6/7 telco auth + feed authenticity (Gate C), R-P1-9 unmatched-recovery clearing, R-P1-8 SSO/MFA design, R-P1-19 scheduler.

**Sprint 3 — M5/certification:** R-P0-6 recon framework, R-P1-21 observability/SLO/load/chaos, DR/backup/IaC, **independent pen-test**, regulatory/customer-rights (#45), evidence pack, formal G4/G5 sign-off.

**Quick wins to slot anytime:** R-P2-1 (token in URL), R-P2-2 (complaint actor), R-P2-3 (error text), R-P2-8 (tool pins) — all small, all real.

## What is NOT in dispute (auditor + reviewer agree)
Append-only ledger, double-entry balance, money representation, saga/FULFILMENT_UNKNOWN handling, scoring anti-gaming, tenant-credential resolution, grant-immutability posture (post-#42), and review governance are **sound**. Continue on this codebase; no rewrite. The G4 "CONDITIONAL PASS" stands **downgraded pending Gate A** — the money-command-integrity P0s must close before I sign G4 as fully passed.
