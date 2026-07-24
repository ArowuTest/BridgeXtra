# Phase 1 — Shared assumed contract for S3 (RECOVERY recon) + the seeder (Day-0 freeze)

**Frozen Day 0. This is the ONE interface both tracks build against.** The seeder
(`cmd/simseed`) *emits* synthetic data in these shapes; S3 (the RECOVERY recon
layer) *consumes* them. If the two ever diverge, the seeded data silently fails to
reconcile — a parallel-build failure that looks green. A change here is a
**coordinated change to both tracks**, not a unilateral edit.

Every wire assumption is **UNVERIFIED** (mock-first, adapter-swappable like S1/S2)
and is a question for MTN — see the MTN-ask list in `PHASE1_S2_ASSUMED_CONTRACT.md`.

---

## 0. Why this exists — the reviewer's key finding

The EOD feed must give a recovery-**attributed** deduction per subscriber per day,
**not a raw balance**. A balance moves for many reasons (usage, top-ups, recovery),
so a balance cannot reconcile *recoveries*. Without an independently-pullable,
recovery-attributed figure, **RECOVERY reconciliation is impossible — and without
it the recharge feed (S2) cannot safely go live.** This is the independent
telco-side pull source that R-P0-6 Slice D4 / VR-52 recorded as the one thing
RECOVERY lacked (`RECON_LAYER_COVERAGE.md:26`).

---

## 1. The EOD recovery-attributed-deduction feed (the reconciliation source)

MTN supplies, **per subscriber, per business day**, the amount it deducted toward
**loan recovery** for this telco's subscribers. This is a reconciliation SOURCE —
it never mints `recovery_events`; it is *compared against* the events the platform
already booked.

**Assumed row shape** (UNVERIFIED — the MTN ask):

| Field | Type | Notes |
|---|---|---|
| `msisdn_token` | string | subscriber identity; resolved to `subscriber_account_id` at recon time (same resolver as recovery ingest). An unresolvable identity is **surfaced, never dropped**. |
| `recovery_deducted_minor` | integer, ≥ 0 | the amount deducted toward loan recovery, **minor units** (we reject non-integer / non-NGN). This is the authoritative figure. |
| `currency` | string | ISO-4217; must equal the telco's `expected_currency` (NGN). |
| `closing_balance_minor` | integer, optional | **cross-check only — never a money authority.** Present so a gross balance sanity-check is possible; it does not drive any reconciliation decision. |

**Per-day envelope (completeness control-total).** Each business-day delivery
carries its own manifest so a truncated/partial feed is detectable, mirroring the
recon engine's `sourceManifest`:

| Field | Type | Notes |
|---|---|---|
| `business_date` | date `YYYY-MM-DD` | the NG business day (see §3). |
| `record_count` | integer | number of subscriber rows in the day. |
| `recovery_deducted_total_minor` | integer | Σ `recovery_deducted_minor` over the day; the monetary control total. |
| `content_hash` | hex sha256 | over the canonical, order-independent row set (material fields), so a tampered/partial set is caught even if the count is spoofed. |

**Transport + auth: pluggable, mock-first.** Assume a daily pull (SFTP/HTTPS file
or poll). The adapter is config-selected and isolated exactly like S1 (outbound
auth) and S2 (inbound HMAC) — the real MTN specifics are an adapter swap, not a
rebuild.

---

## 2. Namespaces (disjoint channels)

- `wh:<event_id>` — recharge-webhook recovery events (S2, existing). The webhook
  channel that the EOD feed reconciles.
- Raw (no prefix) — the internal channel-API recovery events (`POST /v1/recovery/events`).
- **The EOD feed is NOT a recovery-event channel.** It mints nothing; it is the
  independent SOURCE the RECOVERY recon layer compares `recovery_events` against.
- **Recon scope = the webhook channel** (`source_event_id LIKE 'wh:%'`) for the
  telco. MTN's EOD deduction report covers what MTN pushed via the webhook; the
  internal channel-API is a separate, non-MTN path and is out of RECOVERY-recon
  scope. (This keeps the reconciled population and the control-total aligned.)

**Seeder rule:** the seeder emits, from the SAME synthetic ground truth, both the
`wh:`-namespaced recovery events AND the matching EOD feed rows — so a clean day
reconciles exactly, and an injected break (drop / forge / mutate) is caught.

---

## 3. Business-day bucketing (the correctness pin — hardest to get right)

Both sides bucket by **business-event time at `Africa/Lagos`, truncated to day** —
**NOT `received_at`, NOT UTC.**

- Platform side: `(recovery_events.occurred_at AT TIME ZONE 'Africa/Lagos')::date`.
- Feed side: the feed's `business_date`.

**Why:** a late-delivered event (occurred day *N*, received day *N+1*) must land on
day *N* on **both** sides, or it manufactures a false break on *N* (missing
platform) AND *N+1* (missing telco). The existing `DailyIngestedMinor` correctly
uses `received_at`+UTC — but that is the blast-radius **ceiling** (a delivery-rate
control), a different purpose. Recon MUST use `occurred_at` at the business tz.

The timezone is **config, not hardcoded** — a `business_timezone` knob on the
RECOVERY recon config (§5), seeded `Africa/Lagos`.

---

## 4. Match granularity + reconciliation semantics

- **Match key = `(subscriber_account_id, business_date)`.** The feed's `msisdn_token`
  is resolved to `subscriber_account_id` (same resolver as ingest); an unresolvable
  token or a platform event with a NULL subscriber (UNMATCHED) is surfaced as a
  break, never silently dropped.
- **Platform amount** for a key = `SUM(recovery_events.amount_minor)` over the
  telco's `wh:%` events in that subscriber-business-day.
- **Feed amount** for a key = `recovery_deducted_minor`.
- Classification (through the existing generic `reconcileLayer`):
  - **MATCHED** — within `amount_tolerance_minor`.
  - **BREAK_AMOUNT_MISMATCH** — both present, amounts differ beyond tolerance.
  - **BREAK_MISSING_TELCO** — platform booked recoveries the feed does **not**
    confirm → a **phantom / forged recovery** (money we think we recovered but MTN
    did not deduct).
  - **BREAK_MISSING_PLATFORM** — feed reports a deduction we did **not** record → a
    **dropped recovery** (money recovered but unbooked).
- **Breaks are NEVER auto-resolved** (`auto_resolve=false` floor, V1-FIN-005) —
  two-actor maker-checker resolution, the existing recon invariant.
- **Completeness floor** applies per business-day run (the `min_completeness_ratio`
  guard), with the existing maker-checker accept-anyway override for a genuinely
  low-volume day.

---

## 5. RECOVERY recon config (no hardcoding)

New sibling config domain **`recon.recovery`** (mirrors how `scoring.schedule` is a
sibling of `scoring.policy`), scope `programme:<id>` with `→ global` fallback.
Fail-closed GLOBAL seed, DISABLED-equivalent floors. Carries at least:

- `amount_tolerance_minor` — ≥ 0, ≤ `max_amount_minor`; **seed 0** (zero-tolerance floor).
- `auto_resolve` — **must be `false`** (a money break is never auto-resolved).
- `min_completeness_ratio` — `(0,1]`.
- `recon_lag_seconds` — settling lag, `0..604800`.
- `break_aging_alert_hours` — `≥ 1`.
- `max_amount_minor` — overflow-guard ceiling, `1..1e15`.
- `business_timezone` — IANA tz string, seed `Africa/Lagos` (§3).
- `arm_freshness_max_seconds` — the freshness window (§6), range-bounded so a typo
  can neither disable the check nor open an unbounded window.

(The exact knob partition between `recon.recovery` and the existing per-programme
`recon.tolerance` is finalized by the S3 design pass; the domain name and the
fail-closed posture are frozen here.)

---

## 6. Arming gains freshness coupling — YES (fail-CLOSED on a stale feed)

**Today's gap:** `IsLayerLive(telco, RECOVERY)` is pure row-presence in
`recon_layer_arming`; `live_at` is stamped once at first arm and never read or
refreshed. So a telco armed once with a **dead feed** still passes the gate — the
webhook keeps taking money with no live reconciliation. That **fails OPEN**.

**Decision (frozen):** RECOVERY "live" becomes **freshness-coupled**.
`IsLayerLive(telco, RECOVERY)` is true only if the telco is armed **AND** the last
successful EOD recon run is within `arm_freshness_max_seconds`. A stale feed ⇒ NOT
live ⇒ the S2 webhook fails closed (`RECHARGE_RECON_NOT_LIVE`). The freshness
watermark is advanced by S3 on each accepted EOD recon run; the arming production
path (worker/ops) is owned by S3 (today `SetLive("RECOVERY")` has no production
caller — the table ships empty and the webhook is dead-closed until S3 arms it).

**The armed+fresh marker is the SOLE satisfier of S2's recharge-feed arming gate.**

---

## 7. Seeder prod-safety + determinism (binds to this contract)

- **No silent localhost DSN fallback.** Unlike `cmd/seed-operators` / `cmd/migrate`
  (which default to `postgres://…@localhost:5434/…` when the env is unset), the
  seeder **refuses to run** without an explicit DSN and an explicit allow flag.
- **Synthetic-telco bind only.** The seeder writes ONLY the `SIM_NG` synthetic
  telco's data and verifies the target telco is in a synthetic allowlist
  (mirroring the `ops.fault_demo` `SIM_NG`-only governed guard). It **refuses any
  DB holding real (non-synthetic) telco data** — fail-closed.
- **Deterministic.** Seeded generation (stable hashing like the simulator's
  `stableHash`, or a fixed-seed RNG) + **fixed base timestamps** (NO wall-clock
  `time.Now()` in generated data) — a re-run produces byte-identical data, so
  recovery `source_event_id` dedup / idempotency is not tripped (a wall-clock
  timestamp would make a re-run look like a fraudulent divergent duplicate). It
  **avoids `platform.NewID`** (non-deterministic ULID) for any row that must be
  byte-identical across runs — IDs derive from `(seed, index)`.

---

## Sequencing

1. **Day 0 (this doc):** freeze the interface above. ← done.
2. **Parallel:** Seeder A→(B/C) and S3 A→C build independently against this doc.
3. **Integration seam:** the seeder's arm-and-prove-recon-catches-breaks slice runs
   once S3 has landed (it arms the RECOVERY layer and asserts the recon catches an
   injected drop / forge / amount-mutation — asserting against what is actually in
   the DB and what the engine produces, **never** against the seeder's own intent).
