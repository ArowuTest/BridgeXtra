# Build Plan — Telco Digital Credit Platform (Release 0 → Release 1)

**Status:** Rev 2 — reviewer conditions SF-1..7 folded in · 17 July 2026
**Governing documents:** SRS v3.0 Volumes 1–3 (docs/), ADR-0001 (stack + data-access rule + SF amendments), build/REVIEW_GATES.md (gates G0–G5), build/ASSUMPTIONS.md (time-bound assumptions per V1-GOV-007)
**Purpose:** complete work breakdown so nothing is discovered mid-build, plus the binding engineering decisions on idempotency, concurrency, indexing and query efficiency. Every milestone maps to V3 requirement domains; anything not in a milestone is explicitly listed in §9 (deferred) so it is deliberately out, not forgotten.

**Requirement-citation convention (SF-1, binding):** all requirement IDs carry a volume prefix — `V1-`/`V2-`/`V3-`. `EDG-`, `INV-`, `DD-`, `SIM-` are globally unique and stay bare. In §3–§7 below, citations have been prefix-corrected; note V1-TRE-010 (funding outage fails closed) vs V2-TRE-010 (forecasting) as the live example of why this matters. Test names in CI embed the prefixed ID (e.g. `TestV2_ADV_004_...`).

---

## 1. Milestones (local build → test → push at credible milestone)

| Milestone | Contents | Exit criteria (push-to-main gate) |
|---|---|---|
| **M0 — Foundation** | Repo scaffold per ADR-0001; migration runner (fresh-DB validated, RLS-aware); tenant context middleware; config service core (versions, effective dating, maker-checker); outbox (per-aggregate FIFO dispatch per ADR-0001 SF-4) + worker skeleton; idempotency store; CI (build, vet, race tests, fresh-DB migration check); simulator skeleton (happy-path fulfilment + recharge events) | `go test -race ./...` green; migrations apply from zero; a request carries tenant context end to end; config version activates and pins; **SF-1 prefix convention live in CI test names; SF-3 tenant-isolation negative pack green** (wrong-credential + conflicting payload telco_id per V2-TEN-002/003 + EDG-026, adversarial RLS-bypass attempt per repo method); ASSUMPTIONS.md exists and is current (SF-6) — per reviewer gate G0 |
| **M1 — Walking Skeleton (V3-DLV-003)** | Offer snapshot (seeded static decision) → USSD-style confirm API with idempotency → advance FSM (REQUESTED→…→ACTIVE) → exposure + funding reservation → simulator fulfilment incl. **timeout-after-success** → recovery event → allocation → **balanced ledger** → daily reconciliation of platform-vs-simulator records | End-to-end demo against simulator; EDG-001/002/004/005/007/009/018 tests green; ledger rebuild equals stored balances; this is the first push to main |
| **M2 — Credit Core** | Feature ingestion (batch file → features), scoring engine (config-driven eligibility gates, tiers, one-tier movement, spike winsorisation), real offer generation + expiry, real-time overlays (SIM-swap/barring/self-exclusion/funding), concurrency default 1 enforced, disclosure/consent evidence records, notifications (SMS adapter + evidence) | Anti-gaming scenarios (EDG-013), stale-data policy (EDG-014), decision replay (V1-CRD-010) green; scoring run on 1M synthetic subscribers within window |
| **M3 — Money Core Complete** | Full recovery matrix (partial, over-recovery, reversal-before-original, post-write-off), delinquency buckets + write-off, funding pools + guardrails (auto-suspend + maker-checker re-arm), settlement calculation + statements, reconciliation breaks workflow, bureau export pipeline (dormant), complaints register | EDG-019/020/021/024/025/027 green; settlement statement reproducible from ledger; guardrail trips under simulated mass over-approval |
| **M4 — Portals + Ops Readiness** | Next.js portal: admin (config lifecycle), risk (policies/guardrails), finance (ledger/recon/settlement), ops (ambiguity queues), support (masked timeline); auth (httpOnly session, MFA-ready), RBAC + tenant scoping server-side; dashboards; runbook-critical tooling (replay, status-enquiry trigger) | V2-UI-001..015 core met; maker-checker works end to end in UI; fault-injection demo runnable by a non-engineer |
| **M5 — Hardening & Certification-Ready** | Full simulator fault catalogue (SIM-002..005), load test (USSD peak + recharge burst + batch scoring concurrently), chaos (worker kill, DB failover locally), security pass (authz matrix tests, secret scan, log-masking scan), R1 proportionality annex, evidence pack generator | EDG catalogue 100% covered by tests; 5x burst without acknowledged event loss; V2-TST-016 release gate wired |

Rule carried from V3 (V3-DLV-004): financial and operational controls are built **with** their features, never deferred to a hardening phase — M5 hardens, it does not introduce controls.

## 2. Complete scope inventory (nothing-forgotten check)

Mapping every V2 §32 workstream to a milestone: Platform Foundations→M0; Telco Integration (adapter SDK, USSD, notifications, simulator)→M0/M1/M2; Credit Decisioning→M2; Advance & Recovery→M1/M3; Financial Core→M1/M3; Compliance & Customer (bureau, evidence, complaints, portals)→M2/M3/M4; Data Platform→M2 (M5 for scale proof); Quality & Migration→every milestone + §9. Incumbent migration tooling (MIG-*) is **deliberately deferred** (§9) — it needs a real incumbent dataset and telco contract.

## 3. Core schema, keys and indexes (binding)

Conventions: ULID primary keys (time-ordered → index-friendly inserts, no hotspot UUID churn); `telco_id` + (where applicable) `programme_id` on every tenant row (V2-TEN-001); money `BIGINT` minor units + `currency CHAR(3)`; `occurred_at`/`received_at`/`processed_at` kept distinct (V2-API-006); RLS enabled on tenant tables with the migration runner's FORCE-RLS handling; **every index below exists because of an enumerated query in the repo layer — new queries require an index review in PR**.

| Table | Keys & constraints (idempotency/concurrency enforced in the schema) | Indexes (query pattern → index) |
|---|---|---|
| `subscriber_accounts` | PK; **partial unique** `(telco_id, msisdn_token) WHERE effective_to IS NULL` — one live identity period per number (V2-SUB-001, EDG-017) | lookup by token: covered by the partial unique; `(telco_id, status)` for ops lists |
| `decision_snapshots` | PK; `(subscriber_account_id, as_of)` unique | hot path point-read: `(telco_id, subscriber_account_id) WHERE current` partial — the USSD offer lookup is ONE indexed point read (V2-TAR-004) |
| `offers` | PK; immutable snapshot columns; state | `(subscriber_account_id, state, expires_at)` for active-offer fetch; no index on wide JSON |
| `advances` | PK; **UNIQUE `(telco_id, idempotency_key)`** = request idempotency at DB level (V2-ADV-004); `version INT` for optimistic locking; **partial unique `(subscriber_account_id) WHERE state IN (open states)`** = max-one-active-advance enforced by the database, not app logic (V1-ADV-007). **SF-2 guard:** while this index exists, config validation REJECTS `max_concurrent_advances > 1` (armed-but-dead prevention); enabling N>1 is a schema change gated by architecture review (see ADR-0001 amendment) | `(telco_id, state, updated_at)` ambiguity/aging queues (V3-AFO-001); `(subscriber_account_id, created_at DESC)` timeline |
| `fulfilment_attempts` | PK; UNIQUE `(advance_id, attempt_no)`; UNIQUE `telco_idempotency_key`; stores raw telco request/response evidence (V2-TEL-002) | `(state, submitted_at) WHERE state='UNKNOWN'` — the FULFILMENT_UNKNOWN worker scan is index-only |
| `recovery_events` | PK; **UNIQUE `(telco_id, source_event_id)`** = event dedup at DB level (V2-COL-001, EDG-018) | `(state, received_at) WHERE state='PENDING'` worker claim; `(subscriber_account_id)` |
| `recovery_allocations` | PK; FK both ways; `CHECK (amount > 0)`; usecase enforces Σ ≤ event amount and Σ ≤ outstanding in the same txn | `(advance_id)`, `(recovery_event_id)` |
| `pending_reversals` | UNIQUE `(telco_id, original_source_event_id)` — reversal-before-original parking (EDG-019) | `(received_at)` for aging |
| `journals` | PK; **UNIQUE `(business_event_key, event_type)`** = posting idempotency (V2-LED-004); `template_version`; period FK; **INSERT-only Postgres role** (V2-LED-015) | `(accounting_period, posted_at)`; `(telco_id, programme_id, posted_at)` statements |
| `journal_entries` | PK; FK journal; `CHECK ((debit=0) <> (credit=0))`; balance (Σdebit=Σcredit per currency) asserted in the posting txn + nightly rebuild job (V2-LED-008) | `(account_code, posted_at)` as-of balances; monthly **partition-ready** (PK includes posted_at) |
| `exposure_positions` | one row per subscriber/programme; reserved/utilised columns | none extra — accessed by PK with conditional UPDATE |
| `funding_pools` | PK; `CHECK (reserved + utilised <= committed)` — over-allocation impossible even under bugs (V2-TRE-002) | none extra — PK conditional UPDATE |
| `idempotency_records` | **UNIQUE `(telco_id, operation, key)`**; stores outcome hash + response (V2-API-003). **SF-5:** TTL is a config record with seeded floor ≥ longest legitimate retry window (USSD gateway retry + SMS fallback + support re-query); sweep NEVER deletes records whose advance is in a non-terminal state | TTL sweep: `(created_at)` |
| `outbox` | PK ULID; `aggregate_id` column — **per-aggregate FIFO dispatch** (claim only if no older unpublished row for same aggregate, ADR-0001 SF-4); consumers remain pending-match tolerant (V2-EVT-005) | `(created_at) WHERE published_at IS NULL` partial + `(aggregate_id, id) WHERE published_at IS NULL` for the FIFO guard |
| `config_versions` | PK; immutable after approval; content hash; `EXCLUDE` overlapping effective ranges per (domain, scope) via GiST (V2-CFG-006) | `(domain, scope, state, effective_from)` |
| `consents/disclosure_acks` | PK; UNIQUE `(advance_id)`; content hash (V2-REG-001) | — |
| `audit_events` | append-only; monthly partition-ready | `(actor_id, occurred_at)`, `(target_type, target_id, occurred_at)` |

Partitioning: `recovery_events`, `journal_entries`, `audit_events` are designed partition-ready (time in PK) but **stay unpartitioned until R2 volume triggers** — premature partitioning complicates local dev for zero benefit (V2-DAT-009 satisfied by design-readiness).

## 4. Idempotency matrix (every boundary, per V2-TAR-003)

| Boundary | Mechanism | Where enforced |
|---|---|---|
| Channel confirm → create advance | client idempotency key; replay returns original outcome | `idempotency_records` UNIQUE + advances UNIQUE — **DB is the arbiter, app returns stored response** |
| Fulfilment submit → telco | fresh `telco_idempotency_key` per attempt; timeout ⇒ FULFILMENT_UNKNOWN, **no blind retry** (INV-009) | fulfilment_attempts UNIQUE + FSM guard |
| Telco callback (confirm/fail) | dedupe on `(advance_id, telco_ref)`; illegal transitions rejected + audited (V2-ADV-008); late-success-after-failure = controlled correction (V2-ADV-011) | FSM transition table + optimistic version |
| Recovery / reversal events | `source_event_id` UNIQUE; reversal-before-original parked in `pending_reversals` | DB unique + matcher worker |
| Journal posting | `business_event_key` UNIQUE — same economic event posts at most once (INV-003) | DB unique inside posting txn |
| Outbox consumers | per-consumer processed-event table (event_id UNIQUE) | DB unique |
| Notifications | keyed by triggering event id; failure never rolls back the financial event (V2-NOT-009) | notification_jobs UNIQUE |
| Replay/backfill | bounded scope + dry-run counts; all downstream boundaries above make replay safe (V2-EVT-009) | operational tooling M4 |

## 5. Concurrency controls (binding patterns)

1. **Reservation = one atomic conditional statement.** `UPDATE funding_pools SET reserved = reserved + $1 WHERE pool_id=$2 AND committed - reserved - utilised >= $1 RETURNING …` — zero rows = fail closed (V1-TRE-010). Same pattern for subscriber exposure. Never read-then-write.
2. **Origination serialization per subscriber:** `SELECT … FOR UPDATE` on the subscriber_account row inside the origination txn; the partial unique index on open advances is the **backstop** so even a locking bug cannot double-lend (EDG-002). Two independent guards, both in the DB.
3. **FSM transitions:** `UPDATE advances SET state=$new, version=version+1 WHERE id=$1 AND version=$2 AND state = ANY($allowed)` — zero rows = concurrent-modification, reload and re-evaluate (V2-ADV-007).
4. **Never hold a DB txn across a network call** (V2-ADV-006/V2-API-012): commit REQUESTED+reservation → call telco/simulator → new txn records outcome. The saga state, not the transaction, spans the remote call.
5. **Workers claim with `FOR UPDATE SKIP LOCKED`** + lease timestamps (V2-RES-006); every job checkpointable and resumable without double financial effect (V2-RES-007).
6. **Config activation is atomic to readers** (V2-CFG-003): decisions read one immutable version set resolved at request start.
7. `SELECT FOR UPDATE` ordering discipline (always subscriber → advance → pool) to make deadlocks structurally impossible.

## 6. Query-efficiency rules (enforced in review)

- Hot path (offer fetch, confirm) = **point reads on covering indexes only**; no aggregation at request time (V2-TAR-004). Target: ≤3 indexed statements per USSD step.
- Portal lists = read-model repos with purpose-built queries + **keyset pagination** (V2-API-008); no OFFSET on big tables; no N+1 (repo methods return composed rows).
- Batch jobs = set-based SQL (one statement per phase), never Go row loops; scoring writes via COPY/batched inserts into a fresh snapshot then atomic publish flip (V3-CRO-002).
- `EXPLAIN` check in CI for the enumerated hot-path queries against a seeded volume DB (catches seq-scan regressions).
- No `SELECT *` in repos; every query lists columns (keeps covering indexes honest).

## 7. Testing plan (gates per milestone)

- **Property tests:** ledger balance per currency, allocation Σ-caps, recovery-never-negative (INV-004/006) — `gopter`/hand-rolled.
- **Tenant-isolation negative pack (SF-3, from M0):** adversarial cross-tenant read/write attempts per repo method, wrong-credential and conflicting payload `telco_id` (V2-TEN-002/003, EDG-026), cache-key and export scoping checks — runs in CI with `-race` at every milestone (V2-TST-005).
- **Ledger balance structural enforcement (SF-7, evaluate at M1):** prototype a deferred constraint trigger asserting Σdebit=Σcredit per (journal, currency) at COMMIT; adopt if posting-throughput cost <~10%. The app-layer assertion in the posting txn remains either way.
- **FSM exhaustive tests:** every (state, event) pair — legal transitions succeed once, illegal rejected + audited.
- **Concurrency tests:** N goroutines same subscriber (one advance wins), parallel duplicate recharge events (one allocation), reservation storms vs pool cap — run with `-race` (local Docker Postgres, Nirvet pattern).
- **Simulator fault pack:** every EDG-001..040 applicable case becomes a named test; coverage matrix checked in CI (SIM-010).
- **Migration test:** fresh empty DB apply + seeded-defaults assertions every CI run (from-zero lesson).
- **Golden reconciliation test:** known transaction set → expected statement totals to the kobo.

## 8. Configuration-first discipline (owner standing rule)

Every threshold in this plan — tier tables, spike caps, offer expiry, delinquency buckets, guardrail deviations, allocation waterfall, reservation TTLs, SLA timers — is a **config record with a seeded conservative default**, never a constant. Seeds land in migrations; the config service pins versions to every decision (V1-CFG-007). Legal-entity identity (names, licence refs, contacts, sender IDs) is config (V1-BUS-003).

## 9. Explicitly deferred (deliberate, not forgotten)

| Item | Trigger to build |
|---|---|
| Incumbent migration tooling (MIG-*) | telco contract + incumbent data access |
| Live bureau submission (export stays dormant-capable M3) | DD-13 + mandate |
| Second telco adapter | R3; certification harness proves core-code-free onboarding (V2-TAR-002) |
| Broker migration (Kafka etc.), table partitioning activation | R2/R3 volume triggers |
| Dedicated-tenant deployment, tenant-specific KMS keys | contract trigger (V2-TEN-008/010) |
| Multi-language USSD beyond English scaffolding | DD-20 |
| DR infrastructure (multi-region) | pre-production phase; local design keeps RPO path open |
| Data/voice bundle products | R2 (framework supports; product configs only) |

## 10. Immediate next actions when owner says "start"

1. `git init` + scaffold per ADR-0001; commit plan + ADRs.
2. M0: migration runner + `0001_core.sql` (tenants, programmes, config, idempotency, outbox, audit) with seeded defaults.
3. Local Postgres via Docker (`telco-credit-postgres`, port **5434** per ASSUMPTIONS A-14, avoiding Nirvet's 5433).
4. Tenant middleware + config service + first passing race-enabled test suite.
5. Simulator happy path → begin M1 walking skeleton.
