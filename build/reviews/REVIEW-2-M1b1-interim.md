# Interim Review — M1b-1 Credit-Core Schema + Ledger (commit f214668)

**Reviewer:** Claude (Fable 5), lead reviewer · **Date:** 17 Jul 2026
**Type:** commit-boundary review (not a gate) — the ledger and credit-core schema are the highest-stakes surfaces in the system, and this is the last cheap moment to adjust their shape before the saga builds on them.
**Method:** read migration 0004 in full, `internal/ledger`, FSM; independent `-race` run in golang:1.25 (all packages green, including new `ledger` and `dbmigrate`).

## Overall: **sound foundation, on the elevated standard** — 4 findings, none blocking continuation, all cheap now.

Verified strengths (at source): journals/entries append-only **by grants** (no UPDATE/DELETE for any runtime role — and a test proves the DB refuses); per-currency balance rejected before any write; posting idempotency with the DB as arbiter returning the original journal id; correlation_id mandatory (BC-6) and NOT NULL in the schema; chart of accounts fail-closed from governed config (no hardcoded account codes); `funding_no_overallocation` CHECK; the one-active index whose *name* is load-bearing for the SF-2 validator; transitive RLS on child tables via EXISTS-on-parent (correct — child rows are scoped through their tenant-scoped parent); Vol-2 FSM adopted (delinquency = overlay, not a state). The build-cache incident response was exemplary: distrust-host-builds rule + a regression test pinning the migration contract.

## Findings

### M1B-F1 — MEDIUM (process) — Migration cites "ASSUMPTIONS A-15"; the register has no A-15
`0004` comments the single-role ledger-write decision ("dedicated ledger DB role arrives post-M3... recorded in ASSUMPTIONS A-15") — but ASSUMPTIONS.md ends at A-14. This is a **deliberate deviation from BUILD_PLAN §3 / V2-LED-015** ("INSERT-only Postgres role" for the ledger): today, sole-writer is package discipline + review, not a DB role, because same-transaction atomicity with the saga needs one role. That trade-off is defensible and correctly reasoned — but it is exactly the class of decision the register exists to time-bound, and a dangling reference to a nonexistent row is the silent default V3-V3A-004 prohibits. **Fix: add A-15** (single-role ledger writes; trigger = ledger service separation post-M3; impact = package-level sole-writer until then).

### M1B-F2 — MEDIUM — Ledger accepts a duplicate business_event_key with *different* lines, silently
`Post` uses `ON CONFLICT DO NOTHING` and returns the original journal id — correct for the honest-retry case. But if a caller posts the same `(business_event_key, event_type)` with **different lines** (amount drift between retries, a mis-keyed second event — real bug classes in recovery allocation), the divergence is swallowed: caller gets `posted=false` + original id and nobody learns the books nearly went wrong. INV-003 says at-most-once; best-in-class adds *and never silently different*. **Fix: store a lines content hash on the journal; on conflict, compare and return a loud typed error (`ErrDivergentDuplicate`) when the hash differs.** The idempotency-records pattern already stores `request_hash` — apply the same discipline to the most important table in the system.

### M1B-F3 — LOW-MED — Offer money-identity CHECK covers only one fee model
`offer_money_identity` enforces `face = disbursed + fee` only for `DEDUCTED_UPFRONT`; for `ADDED_TO_REPAYMENT` nothing pins `disbursed = face` or `repayment = face + fee`, and for upfront nothing pins `repayment = face`. The comment says "money algebra pinned at snapshot time" — currently only half of it is. **Fix: make the CHECK exhaustive per model:**
`CHECK ((fee_model='DEDUCTED_UPFRONT' AND face_value_minor = disbursed_minor + fee_minor AND repayment_minor = face_value_minor) OR (fee_model='ADDED_TO_REPAYMENT' AND disbursed_minor = face_value_minor AND repayment_minor = face_value_minor + fee_minor))`.

### M1B-F4 — LOW — "Offer accepted once" is app-side only
`advances.offer_id` has no uniqueness. The one-active partial index blocks a *concurrent* second advance for the same subscriber, but a settled-then-reused offer (state-machine bug in offer transitions) would not be stopped by the database. Plan §3 promised "accepted once" as a snapshot property. **Fix: `CREATE UNIQUE INDEX advances_offer_uq ON advances (offer_id)`** — an offer births at most one advance, structurally, forever.

## Reminders travelling to G1
- **SF-7** (deferred constraint trigger for Σdebit=Σcredit, prototype-and-measure) — scheduled "at M1"; not yet done. Due before G1 closes.
- Ledger `Post` currently posts `ADVANCE_ISSUED`/`RECOVERY_APPLIED` with code-composed lines; the full config posting-template engine is M3 scope (noted in the chart seed reason) — acceptable, but the M3 deferral should appear in BUILD_PLAN §9 so it's a recorded deferral, not an implicit one.

## Disposition
Continue to M1b-2/3 (adapter, simulator fault catalogue, origination saga). Fold F1–F4 into the next commit — all four are one-liners-to-small. F2 is the one I care most about: it protects the ledger from its own callers, and the saga about to be written is exactly such a caller.
