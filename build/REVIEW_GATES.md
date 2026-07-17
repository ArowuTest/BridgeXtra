# Reviewer Gates — Telco Digital Credit Platform

**Role:** Lead reviewer (appointed 17 Jul 2026). Continuous review at milestone/commit boundaries against SRS v3.0 Volumes 1–3, ADR-0001 and BUILD_PLAN.md. The builder agent implements; nothing merges to a milestone "done" state without a gate review recorded in `build/reviews/`.
**Method:** verify at source, not by report — read the decision function, run the tests, attempt the negative case. Red is information; tests are never weakened to pass a gate.

---

## Standing findings (open until closed — apply from M0)

| ID | Severity | Finding | Required action | Status |
|---|---|---|---|---|
| SF-1 | HIGH (process) | Requirement-ID collisions across volumes (V3 review D-1): `CFG-001`, `COL-001`, `REG-001`, `TEN-001`, `SEC-001`… are defined with different text in Vol 1, Vol 2 and Vol 3. BUILD_PLAN itself cites bare IDs. The moment tests/evidence name requirement IDs (M0 CI), traceability is ambiguous. | Adopt `V1-`/`V2-`/`V3-` prefixes in ALL code comments, test names, evidence and reviews from the first commit. EDG-, INV-, DD- and SIM- IDs are unique already and stay bare. | FOLDED-Rev2 — verify in CI test names at G0 |
| SF-2 | MEDIUM (design tension — record, don't change) | `advances` partial unique index on open states hard-codes max-one-active-advance in the schema, while V1-PRD-005 / ADV-008 and V2-COL-008 make concurrency **configurable per programme**. | Acceptable and preferred for R1 (DD-09 default = 1; DB backstop beats app logic). Record in an ADR note: enabling N>1 for a programme is a **schema-level change gated by architecture review** (drop/replace index + tested recovery waterfall), not a config flip. Config validation must REJECT `max_concurrent_advances > 1` while the index exists — config that silently cannot take effect is the Nirvet anti-pattern (armed-but-dead control). | FOLDED-Rev2 (ADR amendment + plan §3) — verify config-validation code at G0/G2 |
| SF-3 | MEDIUM (test gap) | BUILD_PLAN §7 testing plan omits **tenant-isolation negative tests** (V2-TST-005 / SEC-016): attempted cross-tenant reads, writes, cache access, event consumption and export must be exercised adversarially in CI, not assumed from RLS presence. | Add a `tenantisolation` test pack to M0 exit criteria (wrong-credential + conflicting payload telco_id per V2-TEN-003 / EDG-026; RLS bypass attempt via each repo method). Runs in CI with `-race` from M0 onward. | FOLDED-Rev2 (plan §7 + M0 exit) — verify pack green at G0 |
| SF-4 | LOW-MED (document the stance) | Postgres outbox dispatched with `FOR UPDATE SKIP LOCKED` does **not** preserve per-aggregate ordering (V2-EVT-004 promises ordering within partition key). | Two acceptable resolutions — pick one and write it into ADR-0001 consequences: (a) dispatcher serialises per `aggregate_id` (claim groups by aggregate, ordered by ULID); or (b) declare ordering NOT guaranteed and make every consumer sequence-/pending-match-tolerant (V2-EVT-005), with reversal-before-original as the proof case. Silent middle ground is not acceptable. | RESOLVED-Rev2: option (a) per-aggregate FIFO chosen + consumers stay tolerant; supporting index added — verify dispatcher SQL at G0 |
| SF-5 | LOW (config) | `idempotency_records` TTL sweep: V2-API-003 requires replay to return the original outcome for the legitimate retry window. | TTL must be a config record (per BUILD_PLAN §8) with a seeded default comfortably ≥ the longest channel retry window (USSD gateway + SMS fallback + support-initiated re-query), and the sweep must never delete records for advances still in non-terminal states. | FOLDED-Rev2 (plan §3 idempotency_records row) — verify at G1 |
| SF-6 | MEDIUM (process) | Gating decisions DD-01 (legal entity), DD-02 (first telco), DD-03 (funding model), DD-05 (USSD route), DD-14 (post-20-Jul regulatory baseline) are unresolved. V1-GOV-007 / V3-V3A-004 prohibit silent defaults. | Create `build/ASSUMPTIONS.md`: every DD the build implicitly answers (e.g. "simulator profiled on MTN-shaped contracts", "own-balance-sheet funding pool seeded") recorded as a time-bound assumption with a review trigger (DD-14 trigger = the 20 Jul 2026 DEON ruling). | CLOSED-Rev2: ASSUMPTIONS.md A-1..A-14 created; live doc re-reviewed at every gate; A-5 trigger = 20 Jul DEON ruling |
| SF-7 | SHOULD (structural preference) | Ledger balance (V2-LED-001) enforced by app-layer assertion inside the posting txn + nightly rebuild. Good, but the strongest form is structural: a deferred constraint trigger asserting Σdebit=Σcredit per (journal, currency) at COMMIT. | Evaluate in M1; if the trigger costs <~10% posting throughput, adopt it. App assertion remains either way. | SCHEDULED: prototype-and-measure at M1 (plan §7) |

## Milestone gates (exit review before a milestone is called done)

Each gate = builder self-evidence + my independent verification. Evidence lands in `build/reviews/REVIEW-<n>-M<k>.md`.

### G0 — Foundation (BUILD_PLAN M0)
- Fresh-DB migration from zero in CI (from-zero lesson); FORCE-RLS-aware runner proven with a seeded global row.
- Tenant middleware: wrong-tenant credential and conflicting payload `telco_id` rejected + security-audited (V2-TEN-002/003, EDG-026). **SF-3 pack green.**
- Config service: version immutability after approval, maker≠checker enforced at API level, decision pins version set (V1-CFG-002/003/007).
- Idempotency store: crash-after-commit replay returns original response (V2-API-003).
- `go test -race ./...` green; CI blocks merge on failure (V2-TST-016 wiring starts here).
- **SF-1 resolved before this gate closes** (prefix convention in place).

### G1 — Walking Skeleton (M1, first push to main)
- End-to-end demo vs simulator: offer → confirm (idempotent) → reserve → fulfil → recover → balanced ledger → reconciliation (V3-DLV-003).
- I independently run: EDG-001 (duplicate confirm), EDG-002 (concurrent requests — DB backstop proven by disabling the app lock in a test build), EDG-004, EDG-005 (timeout-after-success — THE case), EDG-007 (crash after telco success), EDG-009, EDG-018.
- Ledger rebuild equals stored balances (V2-LED-008); no journal for PENDING/UNKNOWN fulfilment (V2-LED-006).
- Reservation release exactly-once on failure (V2-ADV-010); no txn held across the simulator call (V2-ADV-006) — verified by reading the saga code, not the PR description.
- SF-4 stance written and demonstrated (reversal-before-original parked and resolved).

### G2 — Credit Core (M2)
- Anti-gaming: ₦20k-spike-on-₦2k-baseline scenario produces capped contribution + ≤1 tier movement + retained anomaly feature (V1-CRD-004/005/006, EDG-013).
- Stale/corrupt feed → quarantine, no partial silent update (EDG-014); staleness policy fail-closed default (V2-SCR-016).
- Decision replay reproduces original result from pinned versions (V1-CRD-010, V2-SCR-011).
- Real-time overlays actually suppress: SIM-swap flag mid-day kills the precomputed offer (EDG-012) — I test the overlay path, not the batch path.
- Disclosure/consent: content hash retained per advance; no pre-ticked/auto acceptance possible (V1-REG-002, V2-REG-001).
- 1M-synthetic-subscriber scoring run inside window; hot-path offer read = point read (V2-TAR-004) with EXPLAIN evidence.

### G3 — Money Core (M3)
- Full recovery matrix: partial, over-recovery capped + suspense (EDG-020), reversal-before-original (EDG-019), post-write-off (EDG-021), duplicate events (EDG-018 at volume).
- Guardrails: simulated mass over-approval trips suspension within threshold; re-arm requires maker-checker + incident record (V1-CRD-013/014, EDG-024). Verify the guardrail has a **zero-config floor** — empty/missing threshold config must fail closed, never disarm (reachability-invariant lesson).
- Funding: reservation storm vs pool cap — `CHECK (reserved + utilised <= committed)` holds under `-race` load; exhaustion mid-request = safe decline (EDG-023).
- Settlement statement reproducible from ledger to the kobo (golden recon test); breaks preserved, no force-match (EDG-027, V1-FIN-005).
- Bureau export dormant-capable: produces validated file + reconciles to eligible population without submitting (V1-REG-006/007).

### G4 — Portals (M4)
- Server-side authz: every portal query tenant-scoped at the API, UI hiding insufficient (V2-UI-001) — I attempt cross-tenant and cross-role fetches directly against the BFF.
- Maker-checker unbypassable in UI AND API (V2-UI-003); MSISDN masked by default with audited reveal (V2-UI-004).
- Raw operator/audit data surfaces = platform-admin scope only (Nirvet audit-authz lesson).
- Manual repair tooling creates events + ledger corrections, never state edits (V1-ADV-012, V3-AFO-011).

### G5 — Hardening / Certification-Ready (M5)
- EDG-001..040 coverage matrix 100% (each maps to a named test or an approved N/A with reason).
- Load: USSD peak + recharge burst + batch scoring concurrently; no acknowledged event loss (V3-SVC-009); no cross-tenant starvation (V2-TEN-009).
- Chaos: worker kill mid-saga, DB failover locally — no duplicate economic effect (V2-RES-005/006/007).
- Security pass: authz matrix tests, secret scan, log-masking scan (no full MSISDN/NIN in logs, V2-SEC-008).
- R1 Proportionality Annex drafted (V3 review strategic caution) for owner approval.

## Review rules of engagement
1. Builder notifies at each milestone candidate; I review from source at that boundary. Interim commits reviewable on request.
2. Findings are numbered (SF- standing, G<k>-F<n> gate findings) with severity; CRITICAL/HIGH block the gate, MEDIUM needs owner-visible disposition, LOW is tracked.
3. I never weaken a failing test to pass a gate; if a test is wrong, the fix is a reviewed change with rationale.
4. Any deviation from BUILD_PLAN §3–6 binding patterns (schema keys, idempotency matrix, concurrency patterns, SQL-in-repo rule) is an architecture defect (V2-ARC-007), not a style comment.
5. Requirement citations in reviews use volume prefixes (SF-1).

---

## Verification log

### VR-1 — 17 Jul 2026: SF disposition verified at source (pre-start)
Reviewed BUILD_PLAN Rev 2, ADR-0001 (with amendments), ASSUMPTIONS.md line-by-line + mechanical grep for unprefixed citations across build/.

**Result: all seven SF conditions genuinely folded in — cleared to start M0.**
- SF-1: zero unprefixed requirement citations remain in BUILD_PLAN Rev 2; the V1-TRE-010 vs V2-TRE-010 miscite correction is substantively right. Convention text + CI test-name format in ADR amendment. REVIEW_GATES.md itself normalized to the hyphenated format (reviewer holds own docs to the same rule).
- SF-2: closed as specified — config validation rejects N>1 with explanatory error while index exists; N>1 is architecture-gated schema change (ADR amendment + plan §3 advances row).
- SF-3: tenant-isolation negative pack in plan §7 + M0 exit criteria, adversarial per-repo-method, in CI with -race.
- SF-4: option (a) per-aggregate FIFO with a correct, conservatively race-safe claim predicate (a locked-but-uncommitted older row still blocks newer rows for the same aggregate); supporting partial index `(aggregate_id, id) WHERE published_at IS NULL` added; consumers remain pending-match tolerant because telco-INBOUND events have no ordering guarantee regardless — right reasoning.
- SF-5: TTL as config with seeded floor; sweep never deletes records for non-terminal advances (plan §3).
- SF-6: ASSUMPTIONS.md A-1..A-14 created; A-5 conservative-regulatory-superset is the correct posture ahead of the 20 Jul DEON ruling; expired-untouched assumptions are gate-blocking per its own standing rule.
- SF-7: prototype-and-measure scheduled at M1 (plan §7), <~10% threshold, app assertion retained either way.

**Residual LOW nits (no gate impact, fix opportunistically):**
- VR-1a: ADR-0001 Context line still cites bare `ARC-002` (should be V2-ARC-002). Prose-only; ARC exists only in V2 so unambiguous, but the convention is binding.
- VR-1b: BUILD_PLAN §10.3 says local Postgres "port TBD" while ASSUMPTIONS A-14 decides 5434 — align §10.3 to A-14.

**Next checkpoint: G0** (foundation exit). Builder notifies at M0 candidate; reviewer verifies fresh-DB migration, tenant middleware negatives, config maker-checker, idempotency crash-replay, SF-1 CI naming, SF-3 pack, SF-4 dispatcher SQL — at source.
