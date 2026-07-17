# Best-in-Class Engineering Standard — Telco Digital Credit Platform

**Owner directive (17 Jul 2026):** build correctly and to best-in-class standard — better than the competition (Optasia et al.) — from the ground up.
**Status:** binding on the builder from M0 forward, at the same authority level as ADR-0001. Reviewer enforces at every gate alongside the SRS MUSTs.

**What this document is for.** The SRS says *what* to build; this says *how well*, in the specific dimensions that (a) separate an institutional-grade ledger platform from a merely competent one, and (b) are **cheap to establish now and ruinously expensive to retrofit**. Retrofit-cost is the selection rule: a standard earns a place here only if getting it wrong at the foundation contaminates everything built on top. This is deliberately short — proportionality still applies (we flagged the 900-MUST risk ourselves). These are the load-bearing few, not a wish list.

**Competitive thesis.** Incumbents win on 12 years of scar tissue and telco relationships. We beat them where new code can be structurally better than old code: provable financial truth in real time, bit-exact decision replayability, compile-time money safety, adversarially-proven isolation, and a supply chain that is clean from commit one. Those are the fronts. Everything below serves one of them.

---

## The standards (BC-*)

Each is checkable, mapped to the gate that first enforces it. "Enforced" = there is a test or CI job that fails if it regresses — never a code-review opinion.

### BC-1 — Money is a type, not an integer. **[enforce: pre-M1, before any money flows]**
Raw `int64` money is the single most common source of financial defects: currency mixing, silent unit errors, ad-hoc rounding. Best-in-class makes these **impossible at compile time**.
- A `Money` value type (amount `int64` minor units + `Currency`) is the ONLY representation of money in `entity/` and above. No bare `int64` naira crosses a function boundary.
- Arithmetic is methods (`Add`, `Sub`, `Neg`, `AllocateByRatio`); mixing currencies **panics or returns an error**, never silently coerces. There is no `float` anywhere in the money path — a CI grep forbids `float32/float64` in `internal/ledger`, `internal/entity` money fields, and any `*money*.go`.
- Rounding is **one** documented policy (half-up or banker's — decide in an ADR) applied in exactly one place; every rounding produces an explicit residual entry, never a silent drop (V2-LED-013).
- DB boundary maps `Money` ↔ `(BIGINT, CHAR(3))` in the repo layer only.
*Why now:* once M1 passes raw `int64` through the saga, ledger, recovery and settlement, retrofitting a type across all of it is a multi-week rewrite. Introduce it as the first M1 primitive.

### BC-2 — The pipeline is the quality floor: lint, vuln, security, coverage — from M0. **[enforce: G0 amendment, immediately]**
A clean static-analysis baseline is free at 30 files and unachievable at 3,000 (the from-zero lesson applies to lint debt too). CI MUST additionally run, as merge-blocking jobs:
- `golangci-lint` with at minimum: `staticcheck`, `errcheck`, `ineffassign`, `govet`, `bodyclose`, `rowserrcheck`, `sqlclosecheck`, `nilerr`, `contextcheck`. Config committed as `.golangci.yml`. Zero warnings, not a ratchet.
- `govulncheck ./...` — Go vulnerability DB, supply-chain gate.
- `gosec ./...` — security static analysis (hardcoded creds, weak crypto, SQL-string-building — the last catches any drift from parametrized SQL).
- `gofmt -l` / `goimports -l` must be empty; `go mod verify` + `go mod tidy` diff-clean.
- Coverage measured and reported; a floor (start 70%, ratchet up, never down) on `internal/...`. The floor never *drops* to accommodate new code — that is the never-weaken-a-test rule applied to coverage.
*Why now:* every one of these finds cheaper bugs the earlier it runs, and a codebase that was never lint-clean is never made lint-clean.

### BC-3 — Financial truth is continuously proven, not periodically hoped. **[enforce: G1]**
The differentiator incumbents lack. A **standing invariant checker** runs the ledger/exposure invariants as (a) a property-based test pack in CI and (b) a runnable operational job, from the first milestone that posts a journal:
- INV-004 every journal balances per currency; INV-006 no recovery drives outstanding < 0; INV-002 no exposure exceeds the most-restrictive cap; INV-001 no ACTIVE advance without confirmed fulfilment; no orphan advance without a journal; derived balances == ledger reconstruction (V2-LED-008).
- These are **property-based** (`gopter` or equivalent) — generate thousands of randomized histories and assert the invariants hold — not a handful of example rows. Example tests prove presence; property tests prove absence of a class of bug.
- The same checks are a job an operator can run on demand and that the daily control cycle calls (V3-BOP-006). "We can prove the ledger balances at any instant" is a sales claim competitors can't make.

### BC-4 — Every credit decision is bit-exact replayable. **[enforce: G2]**
A regulator or complaint asks "why did this subscriber get this limit on this date." Best-in-class answers deterministically from retained inputs; competitors reconstruct and guess.
- Decision replay (V1-CRD-010 / V2-SCR-011) is proven in CI to reproduce the **identical** output from the pinned `(feature_snapshot, rule_version, model_version, config_version)` — byte-identical `decision_id` result payload, not "approximately the same tier."
- Determinism is structural: no wall-clock, no map-iteration-order, no `Math.random` in the decision path; time and randomness (challenger bucketing) are injected and recorded. A replay-mismatch is a release-blocking defect.

### BC-5 — Isolation and safety are proven adversarially, every build. **[enforce: G0 ongoing]**
Already started well (SF-3 pack). The standard makes it permanent and total:
- Every new tenant-scoped repo method ships with a cross-tenant negative test in the same commit; a method without one is an incomplete change. (A CI check greps for repo methods touching tenant tables without a corresponding isolation test is a stretch goal; at minimum the reviewer enforces it per PR.)
- Every safety control (guardrail, protected-target list, redaction, arm-gate) has a **zero-config-floor test**: empty/missing config must fail closed, proven by a test that sets empty config and asserts the safe outcome (the reachability-invariant lesson, baked in from the start rather than discovered in audit).

### BC-6 — Correlation from customer tap to journal, from M1. **[enforce: G1]**
You cannot debug or audit a distributed money flow you can't trace. From the first saga:
- One `correlation_id` is generated at the channel edge and propagated through every command, event envelope, ledger posting, notification and audit row (V2-API-004). A financial event without a correlation lineage back to a customer action is a defect.
- Structured logs (`slog`) carry it; no PII in logs (V2-SEC-008) — a log-masking test asserts full MSISDN/NIN never appear. Metrics and trace scaffolding exist from M1 even if the backend is local (counters for offers/advances/fulfilments/unknowns/recoveries/ledger-postings), so observability is designed in, not bolted on.

### BC-7 — Errors are typed and boundary-mapped. **[enforce: G1]**
- Domain errors are typed values (or wrapped sentinels), mapped once at the HTTP boundary to the stable `error_code` families (V2 §6.2). Business error codes never leak transport codes and vice-versa; no stack traces or secrets in responses (V2-API-011). A test asserts each error family renders the documented envelope.

### BC-8 — Tests prove behaviour under adversity, not just the happy path. **[enforce: every gate]**
- Every state machine has an **exhaustive** (state × event) test: legal transitions succeed once, illegal are rejected and audited. Not a sample.
- Concurrency-sensitive paths (origination, reservation, recovery allocation, config activation) have `-race` tests that run N concurrent actors and assert the invariant, not the timing.
- The simulator fault catalogue (SIM-002..012) and EDG-001..040 each map to a named test; the coverage matrix is checked in CI (a documented edge case with no test is a red build).

---

## How the reviewer enforces this
- Each gate review (G0..G5) now checks the BC standards whose "enforce" milestone has arrived, in addition to the SRS MUSTs and the SF findings. A BC standard regressing is a gate finding at the severity of what it protects (money-safety BC-1/BC-3 = HIGH+).
- BC standards are verified the same way as everything else: **at source, by running the check** — never by the builder asserting the box is ticked.
- New BC standards may be added when a review surfaces a retrofit-expensive quality attribute not yet covered. They are not added lightly; proportionality is a feature.

## Immediate actions (before M1 saga code)
1. **BC-1:** land the `Money` type + rounding ADR as the first M1 primitive.
2. **BC-2:** add the lint/vuln/security/coverage CI stage now, commit `.golangci.yml`, drive the current tree to zero warnings (trivial at this size).
Both are cheap today and compounding-expensive every week they wait.
