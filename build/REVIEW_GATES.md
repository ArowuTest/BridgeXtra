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

### VR-2 — 17 Jul 2026: G0 gate review (M0 commits 0889b82 + 86843d5)
Full source read + independent `-race` run in golang:1.25 vs telco-credit-postgres. Record: `build/reviews/REVIEW-1-M0.md`.
**Verdict: G0 CONDITIONAL PASS.** Builder suite all green; SF-1..6 verified implemented at source (not just referenced); suspected audit-RLS-rejection cleared (NULLIF guards it, test proves the row lands).
- **G0-F2 (HIGH, must-fix before G1):** outbox head-of-line starvation. Unhandled-type or max-attempts-parked events re-fill the claim batch every cycle; 50 poison events on distinct aggregates stall all dispatch. Reviewer reproducer `outboxdispatch/reviewer_starvation_test.go` FAILS (delivered=0/10 cycles). Confirmed real. Blast radius opens at M1 (ledger/recovery/fulfilment events ride this backbone; V2-EVT-012). Fix = filter claim by registered event types + dead-letter at max-attempts; keep per-aggregate FIFO guard. G1 gated on this + reproducer green.
- **G0-F3 (LOW):** VR-1a bare ARC-002 in ADR, VR-1b plan §10.3 "port TBD" — still open; opportunistic.

### VR-3 — 17 Jul 2026: Elevated to best-in-class standard (owner directive)
Owner directive: build correctly + best-in-class + better than competition, from the ground up. Created **build/ENGINEERING_STANDARD.md** (BC-1..8) — binding at ADR authority, enforced at every gate alongside SRS MUSTs + SF findings. Selection rule = retrofit-expensive × competitively-differentiating. Two urgent foundational gaps confirmed at source (both pre-M1):
- **BC-GAP-1 (HIGH, pre-M1):** no typed `Money` value object — money is bare `int64 + currency string`. No floats yet (good) but nothing enforces it. Must land `Money` type + rounding-policy ADR as the FIRST M1 primitive, before the saga/ledger/recovery/settlement pass raw int64 (retrofit = multi-week rewrite across the money path). Compile-time currency safety + single rounding site + CI float-ban = the ledger differentiator.
- **BC-GAP-2 (HIGH, immediate/G0-amend):** CI has vet/build/migrate/test but NO golangci-lint, govulncheck, gosec, gofmt-check, go mod verify, or coverage floor. Clean static/security baseline is free at ~30 files, unachievable at 3000. Add the quality+security merge-blocking stage NOW; commit .golangci.yml; drive tree to zero warnings (trivial at current size).
Gate criteria upgraded: G0 now also requires BC-2; G1 requires BC-1/BC-3/BC-6/BC-7; G2 requires BC-4; BC-5/BC-8 continuous. A BC regression is a gate finding at the severity of what it protects.

### VR-4 — 17 Jul 2026: Pre-M1 batch verified — G1 ENTRY CLEARED (commits 9fc85ac, 852c4c0, e4e4bb6, 1f159f4)
Verified at source + independent -race run (golang:1.25 container). **All G1-entry conditions met; builder cleared to proceed to M1b (walking-skeleton saga).**
- **G0-F2 CLOSED:** migration 0002 (dead_lettered_at + claim-window index excludes DLQ; FIFO-guard index deliberately unchanged), type-filtered ClaimBatch (`event_type = ANY($2)`, empty registry claims nothing), dead-letter at max-attempts, operator Requeue (V2-EVT-009), DeadLetteredCount metric. Critical subtlety implemented exactly as prescribed: inner NOT-EXISTS ignores BOTH type filter and DLQ marker — quarantined head still blocks its own aggregate, never skips. **Reviewer reproducer now GREEN in my own run**; builder's deadletter_test proves block-own-aggregate + flow-other-aggregates + requeue-unblocks + empty-registry-untouched.
- **BC-GAP-1 CLOSED (BC-1):** entity.Money + ADR-0002. Unexported fields (zero value unusable, errors on all ops), overflow-checked arithmetic incl. MinInt64/-1 pre-check, PercentBps = THE single rounding site (half-up away from zero, big.Int intermediates), AllocateByRatio = largest-remainder, Σparts==total exact, deterministic tie-break; JSON round-trips through the constructor. 10k-case property test. LOW polish note: AllocateByRatio q.Int64() lacks IsInt64 guard for the |MinInt64| single-ratio nano-edge (lands correct by two's-complement accident) — add guard opportunistically.
- **BC-GAP-2 CLOSED (BC-2):** CI quality job merge-blocking: gofmt, mod verify+tidy-diff, money-path float-ban grep, golangci-lint (zero warnings; documented pgx-Rollback exclusion), govulncheck (clean after toolchain 1.25.12 bump — 19 stdlib CVEs were toolchain not code), gosec (clean; 2 auditable #nosec), coverage floor 70% (at 73.9%). 
- **G0-F3 CLOSED:** VR-1a (V2-ARC-002 prefixed) + VR-1b (port 5434 aligned) both fixed.
- **M1a (beyond plan, owner no-hardcoding directive):** admin config API (draft→submit→approve→activate over HTTP, actor from credential, BC-7 typed errors mapped once: 409/422/404), migration 0003 (admin_credentials hash-only + 6 seeded M1 domains), validators_m1.go — armed-but-dead prevention with teeth: telco.adapter retry_budget FORCED 0 citing INV-009 (admin cannot configure the blind-retry double-credit vector), recon.tolerance seeded ZERO w/ auto-resolve off (fail-closed floor), auto_resolve+zero-tolerance rejected as no-op-that-reads-as-control. OpenAPI 0.1.0 same-commit; spec-drift test updated.
**Next checkpoint: G1** (M1b walking skeleton, first push to main). I will independently run EDG-001/002/004/005/007/009/018, read the saga for no-txn-across-network-call (V2-ADV-006), verify BC-3 (property-based invariant checker), BC-6 (correlation edge→journal), SF-7 trigger measurement, and LED-006 (no journal for PENDING/UNKNOWN).

### VR-5 — 17 Jul 2026: Interim review of M1b-1 (f214668) — credit-core schema + sole-writer ledger
Commit-boundary review (not a gate): last cheap moment to adjust ledger/schema shape before the saga consumes it. Record: `build/reviews/REVIEW-2-M1b1-interim.md`. Independent -race run green (incl. new ledger + dbmigrate packages).
Verified: append-only-by-grants (test proves DB refusal), balance-before-write per currency, posting idempotency w/ original-id return, mandatory correlation_id (BC-6), fail-closed governed chart of accounts, funding CHECK, load-bearing advances_one_active_uq name, transitive RLS via EXISTS-on-parent, Vol-2 FSM (delinquency=overlay). Build-cache incident response exemplary (distrust-host-builds + migration-contract regression test).
**Findings (fold into next commit, none block M1b-2/3):** M1B-F1 MED dangling "A-15" reference — the single-role ledger-write deviation from V2-LED-015 is deliberate but NOT in ASSUMPTIONS.md; add the row. M1B-F2 MED ledger swallows divergent duplicates — same business_event_key with DIFFERENT lines returns original silently; add lines-hash + loud ErrDivergentDuplicate. M1B-F3 LOW-MED offer money-identity CHECK only covers DEDUCTED_UPFRONT; make exhaustive per model. M1B-F4 LOW advances.offer_id lacks UNIQUE (offer-accepted-once is app-side only). Reminders to G1: SF-7 measurement due; posting-template-engine M3 deferral should be recorded in plan §9.

### VR-6 — 17 Jul 2026: M1B-F1..F4 fold verified + M1b-2 adapter review (68d351e, 55f3265)
Independent -race run green (incl. new mno package). **All four interim findings verified genuinely closed:**
- M1B-F1 ✓ A-15 row is thorough (deviation, why-safe, trigger = tcp_ledger role at post-M3 service split, impact).
- M1B-F2 ✓ lines_hash = sorted-concatenation sha256 (NOT xor — collision-safe), unambiguous field encoding (`Account|Side|Currency|Amount` joined by \n); divergent duplicate → typed ErrDivergentDuplicate, writes nothing; three-path test (identical / reordered-identical / drifted).
- M1B-F3 ✓ migration 0005 replaces the CHECK, exhaustive over both fee models incl. repayment pinning. Correct discipline: new migration, not an edit to applied 0004.
- M1B-F4 ✓ advances_offer_uq.
**M1b-2 adapter verified:** canonical Client interface (saga depends on interface, not HTTP); INV-009 structural (no retry code path exists); conservative classification per V2 Appendix D — transport error/5xx/200-with-garbage/unrecognised status → Unknown (V2-TEL-009 quarantine); enquiry read-only and safely repeatable; OutcomeNotFound = provably-never-landed → safe fail; endpoint+timeout from telco.adapter config at call time (proven by re-point-via-admin-API test); raw wire evidence retained both directions (V2-TEL-002); EDG-005 timeout-after-success primitive green at the adapter layer.
**Two new findings (LOW tier, fold opportunistically before G1):**
- M1B2-F1 (LOW): `ledger.accounts` config domain has NO registered validator — an empty/malformed chart is approvable. Posting fails closed (safe direction) but it's an admin foot-gun outage vector; add validator: non-empty accounts, code pattern ^[A-Z][A-Z0-9_]+$ (also removes the hash-encoding nano-ambiguity).
- M1B2-F2 (LOW-MED): SubmitFulfilment classifies ALL 4xx as Failed. 408 (edge/aggregator timed out) and 429 can arrive while the telco backend still processes — the exact shape INV-009 guards. Narrow Failed to an explicit definitive-rejection allowlist (400,401,403,404,409,422); everything else non-2xx → Unknown. Cheap, closes an edge double-credit vector at aggregator-fronted telcos.
Builder proceeds to M1b-3 (origination saga) as planned; fold the two LOWs with it.

### VR-7 — 17 Jul 2026: M1b-3 saga core review (e69b26e + 1aa0a34)
Independent -race run green (incl. new origination package). Read the full saga source.
**Verified at source:**
- VR-6 LOWs closed: definitive-rejection allowlist (table test proves 408/429/502/503 → Unknown); ledger.accounts validator (non-empty, code pattern) registered.
- **No-txn-across-network-call is structural** (V2-ADV-006): tx1 accept→advance→reserve→attempt→PENDING commits BEFORE the adapter call; tx2 resolves. Crash window between tx1/tx2 documented and safe: SENT attempt on PENDING advance → resolver treats stale SENT as UNKNOWN → enquiry (EDG-007 exactly-once recovery).
- **Deadlock fix is structurally sound** (mig 0006): advance born pool-less so the one-active contest is decided at the INSERT before any pool lock — a losing contender never holds the pool row; `advances_pool_by_state` CHECK guarantees pool NOT NULL from EXPOSURE_RESERVED onward. The builder diagnosed from live Postgres deadlock DETAIL, not guesswork, and the EDG-002 test (8 concurrent confirms, exactly-one-success + deterministic block classes) caught both first-pass bugs before I saw them — BC-8 working as designed.
- **Ledger algebra traced for BOTH fee models**: DEDUCTED_UPFRONT Dr receivable(face)=Cr clearing(face-fee)+Cr fee(fee); ADDED_TO_REPAYMENT Dr receivable(face+fee)=Cr clearing(face)+Cr fee(fee). Balanced. Recognition at confirmed fulfilment only (V2-LED-006/A-10); UNKNOWN = no journal, no utilisation, reservation held.
- ResolveOutcome shared saga/resolver (semantics cannot drift); posting idempotency makes the dual path safe; OutcomeNotFound = provably-never-landed → safe fail; enquiry schedule from governed config whose validator enforces non-empty positive non-decreasing delays (no [0] panic via config).
- Offer double-accept blocked twice: FOR UPDATE + from-state guard, advances_offer_uq backstop. Exposure convention: reserve/utilise = repayment obligation (outstanding) — consistent everywhere and matches V1 §14 exposure language; conservative direction.
**Findings (all LOW, fold with M1b-4/5, none gate-relevant):**
- M1B3-F1 (LOW): GetOffers ladder generation is not concurrency-idempotent — two simultaneous first-time GetOffers both insert ladders → duplicate valid offers. Economics protected (one-active + offer-uq) but violates V2-OFR-009 SHOULD and doubles USSD menu entries. Fix: advisory xact lock on (subscriber, programme) around generate-if-absent, or unique (subscriber_account_id, programme_id, face_value_minor) WHERE state='GENERATED'.
- M1B3-F2 (LOW): repeated UNKNOWN enquiry cycles emit a fresh advance.FulfilmentUnknown outbox event per cycle — event noise; consider emitting only on state ENTRY (PENDING→UNKNOWN), with enquiry attempts visible via attempts table instead.
- M1B3-F3 (NIT): buildLadder comment says "skip loudly in caller logs" for fee-consumes-denomination but no log call exists — log it or drop the comment.
Remaining before G1: channel HTTP API (M1b-3 tail), recovery+resolver (M1b-4), recon + BC-3 checker + SF-7 measurement + E2E (M1b-5).

### VR-8 — 17 Jul 2026: M1b-4 recovery engine + resolver review (b11b235)
Independent -race run: all 11 packages green (first attempt hit transient container network loss mid-module-download — resolved with persistent gomodcache volume `tcp-gomodcache`; not a code issue).
**Verified at source:**
- **The critical recovery-safety property holds:** `FindOpenBySubscriber` filters `state IN ('ACTIVE','PARTIALLY_RECOVERED')` ONLY — garnishment can never recover against PENDING/UNKNOWN (value the customer may never have received). Locked FOR UPDATE, oldest-first (V2-COL-002).
- Recovery Ingest = one tx end-to-end (no network inside): DB-arbitered source-event dedup (replay returns original, touches nothing — EDG-018); applied-vs-excess split exact; waterfall config-driven with recovered-so-far accounting, components provably sum to outstanding (consume-fully guard is a real invariant); pool utilisation reduced by applied (V2-TRE-004); applied + suspense journals both balanced; over-recovery = explicit RECOVERY_SUSPENSE liability (EDG-020); unmatched events preserved as UNMATCHED, never discarded.
- **Builder self-caught a no-hardcoding violation** (quarantine path nearly booked programme-less events to a hardcoded programme) and resolved it correctly: suspense record now, ledger attribution honestly deferred to M3/DD-19 with a proper §9 register entry (verified present, line 130). The discipline is self-enforcing now.
- Resolver claims due UNKNOWN + stale-SENT (the tx1/tx2 crash window), enquires OUTSIDE any tx, applies via the SAME origination.ResolveOutcome — drift impossible. EDG-005 continuation, EDG-007 (CreditDirect fault injection — credited-but-never-heard recovered exactly once), EDG-008 (NOT_FOUND → FAILED + release) all covered.
- VR-7 LOWs closed: per-(subscriber,programme) advisory lock (6-racer test → one ladder); FulfilmentUnknown event on state ENTRY only (hanging-enquiry cycle test → zero new events); comment fixed.
**One NIT (M1B4-F1):** `allocate()` does raw-minor arithmetic (`compTotal.Amount() - compRecovered`, `MustMoney(recovered[a]+recovered[b])`) because `SumByComponent` returns `map[component]int64` — a small BC-1 deviation in the usecase layer. Have the repo return Money per component; fold whenever convenient.
**Remaining to G1:** HTTP surface (channel + recovery endpoints w/ OpenAPI), M1b-5 (recon vs simulator log, BC-3 property-based invariant checker, SF-7 measurement, wired E2E demo). G1 = full gate + first push.

### VR-9 — 17 Jul 2026: VR-8 NIT fold verified (60fe1ac)
Spot-check at source + targeted -race run (recovery + repo green). `SumByComponent` returns `map[component]entity.Money` built through the validated constructor with currency carried from the DB row — a cross-currency component now surfaces as a loud integrity error, and the waterfall runs entirely in Money operations. Zero bare-integer money above the SQL scan line. **Open findings: none.** Next review = G1 full gate (complete EDG pack re-run, BC-3 checker verification, SF-7 measurement check, saga/HTTP/recon sweep) at walking-skeleton completion.

### VR-10 — 17 Jul 2026: M1b-3 HTTP surface review (0df03ca)
Independent -race run: all 12 packages green. Read channel.go in full.
**Verified at source:** Correlation middleware mints-or-accepts + echoes + context-propagates (BC-6); wire test proves the customer's exact correlation id lands on the journal. BC-7 mapping lives in exactly one function (writeDomainErr); unmapped errors render opaque 500 with detail logged, never leaked (V2-API-011). Idempotency-Key validated 8..128 BEFORE the domain (V2-API-002); bodies bounded (MaxBytesReader 64KB). Customer-safe vocabulary correct: all pre-terminal/ambiguous states → PROCESSING with durable status_route; 201 ACTIVE / 200 replay / 202 UNKNOWN / 422 definitive-fail (V2-ADV-016, EDG-001/004 at wire level). Layering held (GetAdvance usecase accessor; handlers never touch repos). Cross-tenant advance lookups invisible via RLS. OpenAPI 0.1.1 same-commit, drift test updated.
**Two findings (LOW/NIT — fold with M1b-5):**
- VR10-F1 (LOW): caller-supplied X-Correlation-Id is unbounded/unvalidated — up to Go's ~1MB header limit could persist into IMMUTABLE journal rows and logs. Bound it (e.g. ≤64 chars, [A-Za-z0-9_-]); re-mint when invalid (still echo the minted one).
- VR10-F2 (NIT): GET /v1/advances/{id} 404 renders family OFFER_NOT_FOUND — misleading for the status route; render ADVANCE_NOT_FOUND (spec + drift test in same commit per §5a).
Next: G1 full gate at M1b-5 completion.

### VR-11 — 17 Jul 2026: **G1 FULL GATE — PASS** (3bcbd6c). First push to main AUTHORIZED.
Record: `build/reviews/REVIEW-3-G1.md`. Everything run/read by the reviewer directly: full 13-package -race suite green; named EDG pack (001/002/005×3/007/008/011/018/020 + G0-F2 reproducer) all PASS in reviewer's hands; wire-level walking skeleton PASS; BC-3 checker verified (11 set-based sweeps incl. ledger-vs-book + pool-vs-book cross-checks) and operator job run by reviewer → exit 0 "all invariants hold"; randomized-histories property test verified genuinely randomized (deterministic seed = right trade-off); SF-7 honesty check PASSED (+31.8% vs <10% bar → correctly declined, backstop proven firing, M3 revisit recorded); VR-10 folds verified in code. Zero open findings. Carried notes: G1-N1 NIT add INV-018 one-active sweep; G1-N2 LOW M5 randomized-seed soak variant; G1-N3 pre-production role-credential rotation (devlocal_* never reach prod). A-5/DD-14: DEON ruling 20 Jul — trigger not yet fired, re-review mandatory when it lands; conservative superset means no outcome changes the skeleton. Owner decision needed: git remote (recommend private GitHub/ArowuTest). Next: G2 credit core.

### VR-12 — 18 Jul 2026: **G2 FULL GATE — CONDITIONAL PASS** (M2 series through 1e5ffa9). Record: build/reviews/REVIEW-4-G2.md.
Independent 17-package -race suite green; named M2 tests (EDG-013 wash/spike, EDG-014 staleness, SCR-007 one-tier, BC-4 bit-exact + tamper, overlay-blocks-offer-and-confirm, consent-in-confirm-tx) all PASS in reviewer's hands. Repo confirmed PRIVATE (unauth 404); deployment commits reviewed post-hoc — G1-N3 properly closed (env-rotated role passwords), managed-cluster fallback CI-proven, gosec annotations audited genuine. Scale proof honest (89→2,690 subj/s via set-based staged-COPY; 1M in 15.8min).
**Three MEDIUM findings — fold + reviewer verification REQUIRED before M3 begins:**
- **G2-F1:** `missing_policy` validated (REJECT|STARTER, "silent imputation forbidden") but consumed by NOTHING — armed-but-dead inside the validator layer itself. Implement or delete; validated-and-ignored may not survive.
- **G2-F2:** `SPIKE_DISCOUNT_APPLIED` reason code asserts an un-taken action — spiky's only effect is the label; winsorisation runs identically either way. Rename to SPIKE_ANOMALY_DETECTED + make the spiky-consequence an explicit recorded policy decision (may be "flag only", but decided, not accidental). Regulator-facing explainability (V1-CRD-008).
- **G2-F3:** validateRow has NO upper bound on weekly recharge — corrupt near-int64-max row passes validation, overflows maxW*10_000 to negative (spike check silently bypassed), garbage tier assigned. EDG-014 violation at the boundary. Fix: config plausibility ceiling at ingest (quarantine above), + optional overflow-safe engine arithmetic.
Carried: G2-N1 unchecked engine ints (M5); G2-N2 worker-as-owner demo-only; G1-N1/N2 open; branch protection unverified (builder to confirm). Winsor-disarm fix verified GENUINE (validator floor 9230 w/ rationale, mig 0008, pinned test). Next: G2-F fold verification → G3 money core. DEON ruling Jul 20 → A-5 re-review.
