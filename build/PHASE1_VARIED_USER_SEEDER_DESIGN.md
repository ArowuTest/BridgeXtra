# PHASE1_VARIED_USER_SEEDER_DESIGN.md

**Component:** `cmd/simseed` (loop mode, build-tagged) + `internal/simseed/{loop,adapter,profiles}.go` (build-tagged)
**Status:** design, buildable, tied to verified signatures. Extends Seeder-A/B/C. Supersedes the un-hardened draft.
**Thesis:** a build-tag-fenced, in-process loop drives a deterministic MIXED cohort through the REAL usecases — `featureingest.IngestRaw → scoringrun.Run → origination.Confirm(+ResolveOutcome) → recovery.Ingest` — against a telco-guarded synthetic `mno.Client`, then closes the RECOVERY recon by routing loop recoveries through `recovery.Ingest` with a **disjoint** `wh:loop-` namespace plus exact-sum `recovery_eod_feed` rows that `ReconcileRecoveryDay` reconciles MATCHED — with nothing armed and no direct loop INSERT into the money core.

This revision folds in **34 adversarial findings across 5 lenses**. The five load-bearing corrections are: (1) `ReconcileRecoveryDay`, not `ReconcilePeriod` (that reconciles FULFILMENT); (2) replay-aware origination + recovery derived from immutable `adv.FaceValue`, not live `Outstanding`; (3) `AcceptedAt = time.Now()`, not a fixed civil instant (else every first-run `Confirm` fails `ErrDisclosureExpired`); (4) a three-way fence on the always-CONFIRMED adapter; (5) a disjoint `wh:loop-` recovery namespace that cannot collide with `SeedRecoveryDay`.

---

## 0. Findings disposition matrix (every finding: fixed, or residual+mitigation)

| # | Lens · finding | Disposition | Where |
|---|---|---|---|
| D1 | origination re-run trips `ErrDivergentDuplicate` (accepted OfferID can't be fed back) | **FIX**: replay-aware `repo.Advances.GetByIdemKey` pre-check; never re-select from `GetOffers` on replay | §1.4, §5.2 |
| D2 | recovery re-run trips `ErrDivergentRecovery` (amount from live `Outstanding`) | **FIX**: derive amount from immutable `adv.FaceValue`; replay short-circuits before the money core | §4.3, §5.2 |
| D3 | "read `Outstanding` from `ConfirmResult`" false on replay | **FIX**: use `adv.FaceValue` (booked repayment under DEDUCTED_UPFRONT); never the live row | §4.3 |
| D4 | scored TIER is a function of first-run wall clock | **FIX**: drop the "(Seed,Day)⇒tier" claim; hard post-score `StalenessOutcome=="FRESH"` guard | §5.3, §2 |
| D5 | re-run idempotency bounded to `decision_valid_hours` (168h) + `offer_expiry` (24h) | **FIX**: replay path bypasses offers entirely; document the fresh-decision re-run window; fail loudly on expiry | §5.3 |
| D6 | `AcceptedAt=lagosAsOf` breaks first-run `Confirm`; determinism-theater | **FIX**: `AcceptedAt=time.Now().UTC()`; classified non-dedup wall input | §1.4, §5.3 |
| D7 | `sim.FeatureFile`/`lagosAsOf` don't exist; must match `fileShape` json tags; nil vs `[]` | **FIX**: seeder-local `featureFile` struct with exact tags; explicit non-nil pointers/slices | §1.3, §2 |
| D8 | `occurred_at` hashed at nanosecond precision | **FIX**: reuse the verbatim whole-second Lagos builder from `recovery.go:129` | §4.2 |
| D9 | "no wall-clock in generated data" is unattainable via real usecases | **FIX (re-scope)**: → "no wall-clock in **dedup/recon-material** rows"; whitelist outbox/audit/AcceptedAt | §5.3 |
| P1 | **CRITICAL** synthetic adapter fabricates CONFIRMED for any telco, prod-importable, no fence | **FIX (3 ways)**: telco-id guard + `//go:build simseed_loop` + CI import-fence | §3 |
| P2 | default outcome CONFIRMED (maximally permissive) | **FIX**: default `FAILED`; unmapped token in cmd path → hard error | §3 |
| P3 | DSN role unconstrained → RLS binding not guaranteed | **FIX**: `RunLoop` asserts `current_user` is non-`BYPASSRLS` | §5.1 |
| P4 | recon method wrong; the "fix" (`RunRecovery`) silently arms via freshness | **FIX**: `ReconcileRecoveryDay` only; `RunRecovery` forbidden by grep-invariant | §4.4, §5.1 |
| P5 | `VerifySyntheticOnly` is a cmd/env concern, not enforced in `RunLoop` | **FIX**: literal first statement of `RunLoop`, ungated | §5.1 |
| P6 | Slice-4 arming: no four-eyes routing, no teardown | **FIX**: four-eyes `Propose+Approve` only; `DisarmRecovery` teardown; assert not-live after | §6 (Slice 4) |
| P7 | "recon is read-only" is false | **FIX (re-word)**: dormancy basis = "creates no arming row (only `SetLive`/`SetLiveArmed` do)" | §5.4 |
| U4 | config enum omits recon configs | **FIX**: name all four (`recovery.allocation`, `recon.recovery`, `telco.recovery_feed`, `is_synthetic`); assert `buildFeedAdapter`→mock | §1.2, §6 |
| U5 | recovery-via-`Ingest` ≠ full webhook path (occurred_at/HELD clamps) | **FIX (scope honestly)**: loop exercises the `Ingest` money core; handler clamps out of scope by dormancy | §4.1 |
| U6 | clean-day MATCHED is tautological | **FIX (label)**: Slice-3 = plumbing-proof; detection-proof is Slice-4 break injection | §6 |
| U7 | async resolver unexercised outside Slice-4 | **FIX (scope honestly)**: cmd proves happy-fulfilment + varied scoring/recovery; UNKNOWN→resolver is Slice-4 | §3, §6 |
| U8/PR7 | feature tokens must be exactly `msisdnToken(loopSeed,i)` | **FIX**: reuse the unexported helper verbatim (loop.go is in-package); Slice-2 token-join invariant | §1.3, §2 |
| R2 | recovery recon is per-telco, not per-programme | **FIX**: pass `(telcoID, businessDay)` only; drop programme + prebuilt window | §4.4 |
| R3 | read-only false → hold-assertion ordering bug | **FIX**: assert hold present **before** recon, then assert recon **clears** it (positive invariant) | §4.4, §6 |
| R4 | **HIGH** `wh:`/feed key collides byte-identically with `SeedRecoveryDay` | **FIX**: disjoint internal `loopSeed` cohort + `wh:loop-` prefix; reserved-namespace guard | §4.2 |
| R5 | amount/token/day correct **only** while no reversals | **FIX (affirm+invariant)**: loop never calls `recovery.Reverse`; assert zero negative allocations | §4.3, §6 |
| R7 | zero tolerance → feed must equal Σ exactly (odd-remainder trap) | **FIX**: exact-close via single-event full-close; feed = tracked `subjTotal`, not recomputed | §4.3 |
| R8 | day-alignment invariant unasserted | **FIX**: assert feed `business_date` == Lagos date of events' `occurred_at`; recon that same date | §4.2, §6 |
| PR1 | **HIGH** no declined profile — all 4 originate | **FIX**: add a 5th DECLINED profile (`MISSING_FIELDS`→REJECT, ineligible, no advance) | §2 |
| PR2 | policy is v3 (0010), defaulter depends on `spike_action=FLAG_ONLY` | **FIX**: re-anchor to `cfg_seed_scoring_policy_v3`; Slice-1 precondition asserts `spike_action` present | §1.2, §2 |
| PR3 | thin-file `SHORT_HISTORY` dead (tenure gate returns first) | **FIX**: `tenure_days=90` so the thin-file gate is reached; assert exact `COLD_START_THIN_FILE` | §2 |
| PR4 | defaulter raw sum 236000 → **TIER_03** not TIER_04 | **FIX**: corrected falsification value (raw 236000→TIER_03; winsorised 39000→TIER_01) | §2 |
| PR5 | `activity_days_30d` / `require_activity_days` are dead inputs | **FIX**: annotated "not read by `Score()`"; no invariant asserts on it | §2 |
| PR6 | DEGRADED collapses the whole spread to TIER_01 | **FIX**: hard `StalenessOutcome=="FRESH"` guard aborts the run (not silent-degrade) | §5.3, §6 |
| — | `EnquireStatus` `tokenFor()` is underivable from a one-way hash | **FIX**: adapter keys `EnquireStatus` on `platformRequestID` (advance id) map; drop `tokenFor` | §3 |

**Residual risks retained (not fully closable in design):** RR-1 (cross-DB persistent-arming state), RR-2 (`decision_valid_hours` re-run ceiling), RR-3 (Slice-4 admin-pool poke needs the 3-pool harness). See §7.

---

## 1. Architecture

### 1.1 Shape
A new **build-tagged** `simseed.RunLoop` (invoked from `cmd/simseed` behind `-loop`) constructs the real usecases in-process on the single `tcp_app` pool and drives them per synthetic subscriber. It mirrors `e2e/walking_skeleton_test.go`'s `newStack()` but (a) fills the two stages the e2e fakes (`featureingest`, `scoringrun`), (b) swaps the HTTP sim for a telco-guarded `mno.Client`, and (c) routes recoveries through `recovery.Ingest` (disjoint `wh:loop-`), never the raw channel API.

```go
// internal/simseed/loop.go   //go:build simseed_loop
type LoopPlan struct {
    Seed        string // operator seed; loop derives a DISJOINT internal cohort seed
    BusinessDay string // YYYY-MM-DD Lagos — single temporal ANCHOR (as_of + occurred_at + feed date)
    Repeat      int    // members per profile (default 1 → 5 subscribers)
    ProgrammeID string // "prg_sim_airtime01"
}
func RunLoop(ctx context.Context, appPool *pgxpool.Pool, plan LoopPlan) (LoopResult, error)
```

### 1.2 Constructors + verified config prerequisites (zero runtime config writes)
All wiring is on `appPool` (`tcp_app`), per Seeder-A discipline:

| Stage | Constructor (verified) |
|---|---|
| config | `configsvc.New(appPool)` |
| ledger | `ledger.New(appCfg)` |
| **synthetic adapter** | `&simseed.SyntheticAdapter{...}` (§3) — replaces `mno.NewHTTPAdapter` |
| feature ingest | `featureingest.New(appPool, appCfg, log)` — `IngestRaw(ctx, telcoID, source, raw)` (`ingest.go:120`) |
| scoring | `scoringrun.New(appPool, appCfg, log)` — `Run(ctx, telcoID, programmeID, featureFileID)` (`run.go:56`) |
| origination | `origination.New(appPool, appCfg, led, adapter, log)` — `GetOffers` (`:323`), `Confirm` (`:508`) |
| recovery | `recovery.New(appPool, appCfg, led, log)` — `Ingest(ctx, IngestCmd)` (`recovery.go:80`) |

**Every config the loop touches is migration-seeded ACTIVE for SIM_NG / `prg_sim_airtime01` (verified — the loop writes NO config):**
- `scoring.policy` **v3** = `cfg_seed_scoring_policy_v3` (0010:36 `jsonb_set(...'{anti_gaming,spike_action}','"FLAG_ONLY"')` over 0008:21). Tiers `TIER_01` 5000/≥30000, `TIER_02` 10000/≥90000, `TIER_03` 20000/≥200000, `TIER_04` 50000/≥500000; `min_tenure_days 90`, `min_active_days 10`, `winsor_upper_bps 9200`, `spike_ratio_max_bps 30000`, `spike_action FLAG_ONLY`, staleness `accept 48h / degrade 168h / cap TIER_01`, `missing_policy REJECT`, `starter_tier TIER_01`, `one_tier_up_max 1`, `decision_valid_hours 168`.
- `product.airtime` (0003:34): `denominations_minor [5000,10000,20000,50000]`, `fee_bps 1000`, `fee_model DEDUCTED_UPFRONT`, `offer_expiry_minutes 1440`.
- `disclosure.policy` (0034:103): `allowed_channels [USSD,APP]`.
- `telco.adapter` (0010:60): `max_weekly_recharge_minor 100000000` — the `IngestRaw` fail-closed ceiling (`ingest.go:132`). `fulfilment_url` is irrelevant (adapter injected).
- `treasury.guardrails` (0011:233): `max_daily_disbursed_minor 50000000`, `max_open_exposure_bps_of_committed 8000`, `trip=SUSPEND_PROGRAMME`.
- funding pool `pool_sim_01` (0004:313): committed `100000000` NGN.
- **recovery leg (was omitted in the draft — U4/R6):** `recovery.allocation` (0003:52, `programme:prg_sim_airtime01`), `recon.recovery` GLOBAL (0053:99: `amount_tolerance_minor 0`, `min_confirmation_ratio 0.99`, `business_timezone Africa/Lagos`, `arm_freshness_max_seconds 172800`), `telco.recovery_feed` SIM_NG (0053:118: `source=mock`, `business_date_basis occurred_at_lagos_date`), `telcos.is_synthetic=true` for SIM_NG (0053:51) — the circularity guard `buildFeedAdapter` enforces (`recon_recovery.go:466-474`).

### 1.3 Feature file (seeder-local struct, exact tags — D7/U8)
`featureingest.fileShape`/`rowShape` are **unexported**, so the loop defines a byte-compatible local struct with **identical** json tags (`ingest.go:48-63`):

```go
// tags MUST match ingest.go:48-63 exactly, or rows silently quarantine.
type featureFile struct {
    TelcoID string        `json:"telco_id"`
    AsOf    time.Time     `json:"as_of"`
    Rows    []featureRow  `json:"rows"`
}
type featureRow struct {
    MSISDNToken         string   `json:"msisdn_token"`
    TenureDays          *int     `json:"tenure_days"`
    ActivityDays30d     *int     `json:"activity_days_30d"` // NOT read by Score() (PR5) — set, never asserted
    ActiveDays90d       *int     `json:"active_days_90d"`
    WeeklyRechargeMinor []int64  `json:"weekly_recharge_minor"`
    Currency            string   `json:"currency"`
    QualityFlags        []string `json:"quality_flags"` // always non-nil []string{} (nil→null→different hash)
}
```
- Tokens are `msisdnToken(loopSeed, i)` **verbatim** (`simseed.go:100`) so `BulkEnsureByToken` (`ingest.go:193`) resolves to the existing loop cohort rather than minting parallel subscribers (U8/PR7).
- `AsOf = time.Date(y,m,d,0,0,0,0, lagosLoc).UTC()` — whole-second, fixed-location, zero sub-second (D7/D8). Pointers are always set (never nil) and `QualityFlags` is always `[]string{}` when empty, so `content_hash = sha256(raw)` (`ingest.go:136`) is byte-stable ⇒ re-ingest is a recorded no-op (`ingest.go:147-150`).

### 1.4 Per-member sequence (replay-aware — D1/D6)
1. **Guards** (§5.1): `VerifySyntheticOnly` first line; `current_user` non-BYPASSRLS; `TCP_SEED_ALLOW`/`TCP_SIMSEED_DSN` in the cmd.
2. **Cohort**: `SeedCohort(ctx, appPool, CohortPlan{Seed: loopSeed, Count: 5*Repeat})` (Seeder-A, idempotent). Member `i` → profile `i % 5`.
3. **Feature file** (one): marshal `featureFile{...}`; `featureingest.IngestRaw(ctx, "SIM_NG", "simseed:loop-feature-file", raw)` → `Summary.FeatureFileID`.
4. **Score**: `scoringrun.Run(ctx, "SIM_NG", ProgrammeID, featureFileID)` → real `is_current` decisions. **Hard FRESH guard** (§5.3/PR6).
5. **Originate per member** (`platform.WithTenant(ctx,"SIM_NG")`), **replay-aware**:
   ```go
   idem := stableID("idem", loopSeed, fmt.Sprintf("adv|%s|%06d", BusinessDay, i))
   // D1: replay-aware pre-check — reuse the booked advance, never re-select from GetOffers.
   adv, found := getAdvanceByIdemKey(ctx, appPool, idem) // repo.Advances.GetByIdemKey, tenant tx
   if !found {
       if profile == DECLINED { assertDeclined(ctx, ...); continue } // §2 — no advance
       views, _ := orig.GetOffers(ctx, ProgrammeID, token)
       v := topRung(views)                                  // highest FaceValue ≤ MaxFaceValue, tie-break OfferID
       res, _ := orig.Confirm(ctx, origination.ConfirmCmd{
           ProgrammeID: ProgrammeID, OfferID: v.Offer.OfferID, MSISDNToken: token,
           IdemKey: idem, CorrelationID: stableID("corr", loopSeed, idem),
           DisclosureRef: v.Disclosure.DisclosureSnapshotID, Channel: "USSD",
           SessionID: stableID("sess", loopSeed, idem),
           AcceptedAt: time.Now().UTC(),                    // D6/U1 — NOT a fixed civil instant
       })
       adv = res.Advance
   }
   ```
   All profiles map to `CONFIRMED`, so `ResolveOutcome`'s CONFIRMED branch (`origination.go:925+`) runs inline inside `Confirm` → **ACTIVE** synchronously; no resolver, no admin-pool poke.
6. **Recover per member** (§4): 0–1 `recovery.Ingest` (`wh:loop-`) + one exact-sum `recovery_eod_feed` row.

**No direct loop INSERT** into `advances`, `offers`, `decision_snapshots`, `consents`, `fulfilment_attempts`, `recovery_events`, `recovery_allocations`, or ledger tables. The loop's ONLY direct writes are the cohort (Seeder-A) and the `recovery_eod_feed` telco-SOURCE rows (the feed mints nothing — contract §0/§2). A Slice-1 grep/AST invariant enforces this over `loop.go`.

---

## 2. The profiles — exact feature values → verified outcome

**Five** profiles (the draft's four all originated; PR1 requires a genuine decline). Common: `Currency "NGN"`, `AsOf` = Lagos-midnight of `BusinessDay` (FRESH ≤ 48h), all pointer fields explicit, `QualityFlags` non-nil. Values are literal per-profile constants (auditable), never hashed. `activity_days_30d` is set but **not read** by `Score()` (PR5).

| Profile | tenure | active_90d | quality_flags | weekly_recharge_minor | winsor total90 | Decision (engine.go verified) | MaxFace | Recovery |
|---|---|---|---|---|---|---|---|---|
| **good-payer** | 720 | 90 | `[]` | `[40000]×13` | 520000 | Eligible **TIER_04** (`RECHARGE_TIER_TIER_04`) | 50000 | full-close → CLOSED + hold |
| **varied-limit** | 365 | 90 | `[]` | `[10000]×13` | 130000 | Eligible **TIER_02** | 10000 | partial (5000) → PARTIALLY_RECOVERED |
| **thin-file** | **90** | **8** | `["SHORT_HISTORY"]` | `[5000]×13` | (gate returns) | Eligible **starter TIER_01** (`COLD_START_THIN_FILE`, `STARTER_POLICY_TIER_01`) | 5000 | full-close → CLOSED + hold |
| **defaulter** | 200 | 90 | `[]` | `[200000, 3000×12]` | **39000** | Eligible **TIER_01** (`SPIKE_PATTERN_DETECTED`) | 5000 | **none** → stays ACTIVE |
| **declined** | 400 | 90 | `["MISSING_FIELDS"]` | `[20000]×13` | — (gated at missing) | **Ineligible** (`MISSING_DATA_REJECTED`), face 0 | 0 | none — **no advance** |

Gate-order facts (verified `engine.go:160-288`), which fix three draft errors:

- **thin-file (PR3):** `tenure_days=90` makes `90 < min_tenure_days(90)` **false** (`:197`), so the cold-start-**tenure** return is skipped and the thin-file gate (`SHORT_HISTORY || active_90d<10`, `:204`) fires → reason `COLD_START_THIN_FILE`. Slice-2 asserts the **exact** code, not a `COLD_START_*` wildcard. (At `tenure=30` the draft would have produced `COLD_START_TENURE` and `SHORT_HISTORY` would be dead.)
- **defaulter (PR2/PR4):** spiky (`max/median = 200000/3000 = 66.7 > 3.0`, `:222`). `spike_action=FLAG_ONLY` (v3, 0010) → winsor-only; **absent** `spike_action` on the stale v2 would make the engine **error** → `run.go:189` **skips** the member (no advance to default on) — so Slice-1 asserts the resolved policy carries `spike_action`. Winsor cap = `percentileValue(9200bps)` = 12th-of-13 sorted = 3000 (`:347-359`); `total90 = 13×3000 = 39000 ≥ 30000` → **TIER_01**. Falsification (corrected): **raw** sum `200000 + 12×3000 = 236000 → TIER_03` (≥200000); the raw-sum expectation MUST fail, the winsorised (TIER_01) MUST hold.
- **declined (PR1):** `MISSING_FIELDS` + `missing_policy REJECT` → `MISSING_DATA_REJECTED`, `Eligible=false` (`:183-187`), `run.go:201-203` writes face 0. `GetOffers`→`requireValidDecision` returns `ErrDecisionUnavailable` ("not eligible", `origination.go:314`). The loop treats this as the **expected** declined outcome and skips origination; Slice-2 additionally reads `decision_snapshots` (read-only) to assert `is_current`, `Eligible=false`, reason `MISSING_DATA_REJECTED`.

Cold-start (verified `engine.go:266-267`, `run.go:177-178`): a fresh subscriber's `PriorTierCode` is `""` (`SEED→""`) ⇒ `prior=-1` ⇒ the one-tier movement cap is skipped, so good-payer reaches TIER_04 on the first cycle. The offer ladder (`buildLadder`, `:425`) filters `denominations ≤ MaxFaceValue`; the loop always picks the top rung.

Default `Repeat=1` → 5 subscribers; borrower Σface = 50000+10000+5000+5000 = 70000, Σdisbursed ≈ 63000 — trivially under the ₦500k/day treasury cap and the 80%-of-₦1m pool (Slice-2 proves the guardrail is real by also tripping it with an oversized cohort).

---

## 3. Synthetic telco adapter — fenced three ways (P1/P2)

The always-CONFIRMED, no-network adapter is the single most dangerous object (wired into `cmd/worker` it would mark advances ACTIVE and post the issuance journal for real subscribers with no airtime delivered). It is fenced **three independent ways**:

**(a) runtime telco-id guard** — refuses any telco but SIM_NG (the guard the draft's `_tid` ignored). `SubmitFulfilment`/`EnquireStatus` both receive `telcoID`:
```go
// internal/simseed/adapter.go   //go:build simseed_loop
type SyntheticAdapter struct {
    Seed    string
    ByToken map[string]mno.Outcome // explicit per-member; NO permissive default
    ByAdv   map[string]mno.Outcome // advanceID → outcome, for EnquireStatus (no tokenFor())
}
func (a *SyntheticAdapter) SubmitFulfilment(_ context.Context, telcoID, _idem string, req mno.FulfilmentRequest) (mno.Result, error) {
    if telcoID != SyntheticTelco { return mno.Result{}, fmt.Errorf("SyntheticAdapter refuses non-synthetic telco %q", telcoID) }
    return a.decide(req.PlatformRequestID, a.outcome(req.MSISDNToken)), nil
}
func (a *SyntheticAdapter) EnquireStatus(_ context.Context, telcoID, platformRequestID string) (mno.Result, error) {
    if telcoID != SyntheticTelco { return mno.Result{}, fmt.Errorf("SyntheticAdapter refuses non-synthetic telco %q", telcoID) }
    return a.decide(platformRequestID, a.ByAdv[platformRequestID]), nil // keyed on advance id (P4 unverified-claim fix)
}
func (a *SyntheticAdapter) outcome(token string) mno.Outcome {
    o, ok := a.ByToken[token]
    if !ok { return mno.OutcomeFailed } // P2: default FAILED, never CONFIRMED; cmd path panics on unmapped
    return o
}
func (a *SyntheticAdapter) decide(advanceID string, o mno.Outcome) mno.Result {
    ref := fmt.Sprintf("SIM-%016x", stableHash64(a.Seed+"/telcoref/"+advanceID)) // deterministic, no platform.NewID/time.Now
    switch o {
    case mno.OutcomeFailed:  return mno.Result{Outcome: mno.OutcomeFailed,  TelcoReference: ref, ResponseEvidence: []byte(`{"status":"FAILED"}`)}
    case mno.OutcomeUnknown: return mno.Result{Outcome: mno.OutcomeUnknown, ResponseEvidence: []byte(`{"status":"PENDING"}`)}
    default:                 return mno.Result{Outcome: mno.OutcomeConfirmed, TelcoReference: ref, ResponseEvidence: []byte(`{"status":"SUCCESS"}`)}
    }
}
```
Interface verified `mno/adapter.go:74-78`; `Outcome` consts `:34-41`; `FulfilmentRequest.PlatformRequestID` (== advance id) `:58`; `Result` `:66-70`.

**(b) build tag** — `adapter.go`, `loop.go`, `profiles.go` all carry `//go:build simseed_loop`; `cmd/simseed` gets a tagged `loop_main.go` (`-loop`) and an untagged stub. The adapter is **not compiled** into any normal build. There are zero `//go:build` tags in the backend today — this is the first, and its whole purpose is to keep the always-CONFIRMED object out of `cmd/api`/`cmd/worker` binaries.

**(c) CI import-fence** — a test/CI check forbids `cmd/api` and `cmd/worker` from importing `internal/simseed` at all (today only `cmd/simseed` does). Grep-based, matching the existing single-door fence pattern.

The cmd maps all five profiles to `CONFIRMED` (declined never originates, so its mapping is moot). `FAILED`/`UNKNOWN` are reserved for the Slice-4 e2e harness.

---

## 4. Recovery → `wh:loop-` + exact EOD feed → `ReconcileRecoveryDay` MATCHED

### 4.1 Path and scope (U5)
The loop calls `recovery.Ingest` **directly** — the money core (`classifyAndApply`: fee-first waterfall + ledger `EventRecoveryApplied`, no direct `recovery_events` INSERT for the loop). This is condition-1-clean, condition-4-clean (`wh:%` recon scope), condition-5-clean (bypasses the arming-gated webhook). Stated honestly: the loop exercises the **`Ingest` core**; the webhook handler's `occurred_at` clamp and per-event HELD blast-radius clamps (`recharge_webhook.go:253-315`) are recovery **business rules that are out of scope by dormancy design** — not "shell." For the loop's small, in-window amounts the booked result is identical.

### 4.2 Disjoint namespace + business-day bucketing (R4/R8/D8)
The draft's `wh:` id and feed key were **byte-identical** to `SeedRecoveryDay` (`simseed/recovery.go:92-94,146-150`) over the **same** cohort tokens — a real collision (a shared `(seed,day)` run makes the loop's `Ingest` hit the dedup backstop and book nothing while recon MATCHES the *direct* rows: a false green). Fix — **structural disjointness**:
- Internal `loopSeed := plan.Seed + "|loop"`. The cohort, all ids, and all tokens derive from `loopSeed`, so loop tokens `msisdnToken(loopSeed,i)` can never equal any plain-seed `SeedRecoveryDay` token ⇒ `recovery_eod_feed (telco, business_date, msisdn_token)` keys are disjoint.
- Source ids: `src := "wh:loop-" + stableID("looprecev", loopSeed, key)` — still `LIKE 'wh:%'` (in recon scope) but a disjoint event namespace (belt-and-braces).
- **Reserved-namespace guard:** `RunLoop` and the doc forbid running `SeedRecoveryDay` with a seed literally ending in `|loop`.

`OccurredAt` reuses the **verbatim** whole-second Lagos builder (D8): `time.Date(y, m, day, eventWallHours[k], 0, 0, 0, loc).UTC()` (`recovery.go:129`) — never a fresh `lagosWallHour` that could re-introduce sub-second precision into `recoverySourceHash` (which formats `RFC3339Nano`, `recovery.go:161`). Feed `business_date` == Lagos date of the events' `occurred_at` (R8), both driven off the single `-business-day` flag.

### 4.3 Amounts from immutable `FaceValue`; exact close; no reversals (D2/D3/R5/R7)
Under `DEDUCTED_UPFRONT` the booked repayment obligation is `adv.FaceValue` (`buildLadder:448` `repayment=face`; `Outstanding=offer.Repayment`, `:690`). Recovery amounts are derived from **`adv.FaceValue`** (immutable), **never** the live `Outstanding` (which run-1 already mutated — reading it on replay yields a different amount → `ErrDivergentRecovery`, or 0 → "amount must be positive").

`amount_tolerance_minor = 0` (0053) ⇒ the feed must equal platform NET **to the minor**. Exact-close is guaranteed by **single-event full-close** (no odd-remainder split, R7):

| Profile | `recovery.Ingest` | Advance end | hold | feed row | recon |
|---|---|---|---|---|---|
| good-payer | 1 event `= FaceValue (50000)` | CLOSED | yes (`wh:` close, `recovery.go:346`) | 50000 | MATCHED |
| varied-limit | 1 event `= FaceValue/2 (5000)` | PARTIALLY_RECOVERED | no | 5000 | MATCHED (partial) |
| thin-file | 1 event `= FaceValue (5000)` | CLOSED | yes | 5000 | MATCHED |
| defaulter | none | ACTIVE | no | none | absent both sides → no break |
| declined | none (no advance) | — | — | none | absent |

```go
for k, amt := range recoveryAmounts(profile, adv.FaceValue) { // amt derived from FaceValue, ≤ outstanding, >0
    src := "wh:loop-" + stableID("looprecev", loopSeed, fmt.Sprintf("%s|%06d|%d", BusinessDay, i, k))
    r, _ := rec.Ingest(ctx, recovery.IngestCmd{
        SourceEventID: src, MSISDNToken: token,
        Amount: mustMoney(amt, "NGN"),
        OccurredAt: time.Date(y, m, day, eventWallHours[k], 0, 0, 0, loc).UTC(),
        CorrelationID: stableID("corr", loopSeed, src),
    })
    subjTotal += amt // feed = SUM of EVENT amounts (recon SUMs re.amount_minor, recon_recovery.go:86)
}
// one recovery_eod_feed row = subjTotal (tracked, NOT recomputed from adv.Outstanding)
```
The loop **never** calls `recovery.Reverse` (R5) — the only source of negative `recovery_allocations` that would make platform NET < Σamount. A Slice-3 invariant asserts zero negative allocations on the loop path, so gross==NET==feed by construction. Feed insert reuses `SeedRecoveryDay`'s `ON CONFLICT (telco_id, business_date, msisdn_token) DO NOTHING` pattern (`recovery.go:146-150`) → run-1 bytes preserved on re-run.

**Determinism note (D3):** the feed value and recovery amounts are pure functions of `adv.FaceValue` (fixed denomination + fixed config) ⇒ byte-identical per `(loopSeed, BusinessDay)`. On replay, `rec.Ingest` sees the existing `wh:loop-` id and returns the original outcome (`Replayed`) before touching the advance, so a CLOSED advance's `Outstanding=0` is irrelevant.

### 4.4 The correct recon entrypoint + hold ordering (P4/R2/R3)
The draft's `recon.ReconcilePeriod("SIM_NG","prg_sim_airtime01", window)` reconciles **FULFILMENT** (`recon.go:321` `reconcileLayer(fulfilmentSpec(), …)`) — it never touches `wh:%` recoveries. The RECOVERY layer is reconciled ONLY by:
```go
recon.ReconcileRecoveryDay(ctx, "SIM_NG", BusinessDay) // recon_recovery.go:132 — telco + date string, NO programme
```
It uses `recoverySpec()` (platform NET = `SUM(amount_minor + neg allocations) WHERE source_event_id LIKE 'wh:%'`, keyed by `msisdn_token`, `recon_recovery.go:56-113`) and the sentinel programme `__RECOVERY__`. Crucially it **does NOT advance arming freshness** (`:129-131`). `RunRecovery` (`:223`) DOES (`AdvanceFreshness`, `:264`) and is therefore **forbidden** in the seeder/dormant path — a grep-invariant asserts the loop and Slice-3 tests never call it (on a DB whose arming row exists but aged out, `RunRecovery` would re-freshen → live).

Recon is **not read-only** (P7/R3/U3): `reconcileRecoveryDayWith` writes `recon_runs` and, when `!Rejected && !NothingToReconcile`, `clearRecoveryHolds` deletes the `recovery_holds` rows the `wh:` closes created (`:160-164`). So the Slice-3 hold assertions are **ordered**: assert the uncleared hold **before** running recon (good-payer/thin), then assert recon **clears** it — a positive invariant, not a contradiction. Recon is run only by an adversarial **test**; the cmd never calls it.

---

## 5. Determinism + prod-safety + dormancy

### 5.1 Prod-safety guards (P3/P5)
`RunLoop`'s **literal first statements** (ungated by any env — the test caller passes a pool with env unset; P5):
```go
func RunLoop(ctx context.Context, appPool *pgxpool.Pool, plan LoopPlan) (LoopResult, error) {
    if err := VerifySyntheticOnly(ctx, appPool); err != nil { return LoopResult{}, err }     // P5
    var bypass bool                                                                            // P3
    if err := appPool.QueryRow(ctx, `SELECT rolbypassrls FROM pg_roles WHERE rolname=current_user`).Scan(&bypass); err != nil {
        return LoopResult{}, fmt.Errorf("simseed loop: cannot verify role (fail-closed): %w", err)
    }
    if bypass { return LoopResult{}, fmt.Errorf("simseed loop: current_user has BYPASSRLS — refusing (RLS SIM_NG binding not guaranteed)") }
    // ...
}
```
The cmd retains `TCP_SEED_ALLOW=1` + explicit `TCP_SIMSEED_DSN` (no localhost fallback). The `TCP_SIMSEED_DSN` test SKIP-guard controls *whether the test runs*, not *what it may touch* — the two `RunLoop` asserts are the real controls.

### 5.2 Idempotency mechanism (D1/D2)
- **Origination:** replay-aware pre-check reads the advance by the deterministic `IdemKey` (`repo.Advances.GetByIdemKey`) and reuses it — the loop never re-selects from `GetOffers` on a re-run, so the accepted-offer→divergent-hash trap (`confirmRequestHash` binds programme+offer+token, `origination.go:94-103`) never fires.
- **Recovery:** amounts from immutable `adv.FaceValue`; `rec.Ingest` replays on the stable `wh:loop-` id (`source_event_id` dedup, `recovery.go:104-124`).
- **Scoring:** `ErrDuplicateRun` → `Resumed` (`run.go:76-80`); feature file dedups on `content_hash`.

### 5.3 Re-scoped determinism (D4/D5/D6/D9)
Condition-2's "no wall-clock in generated data" is re-scoped (D9) to **"no wall-clock in dedup/recon-material rows."** Driving real usecases necessarily writes `time.Now` into `outbox_events.occurred_at`, `audit.created_at`, `advances.AcceptedAt/UpdatedAt`, `scored_at`/`valid_until` — none are dedup/recon keys.

| Class | Rows | Guarantee |
|---|---|---|
| **Byte-identical (material)** | loop cohort tokens/ids; feature-file bytes; `IdemKey`/`CorrelationID`/`SessionID` (`stableID`); `wh:loop-` `SourceEventID`s; `recovery_eod_feed` rows (from `FaceValue`); recovery `OccurredAt` (whole-second Lagos) | same `(loopSeed, BusinessDay)` ⇒ identical |
| **Converges by natural key / replay** | `scoring_run_id`/`decision_snapshot_id`+`scored_at`/`valid_until`; `offer_id`/consent/attempt/`advance_id`; `recovery_event_id`/allocation ids; `TelcoReference` (stored, not a key) | idempotent via `ErrDuplicateRun` / `IdemKey` replay / `source_event_id` replay |
| **Wall-clock, non-material (whitelisted)** | `AcceptedAt`; outbox/audit timestamps; `advances.UpdatedAt` | inert (never a dedup/recon key) |

Tests assert on natural keys/amounts/state/tier/reason — **never** on minted ids.

**Staleness (D4/D5/PR6):** the scored TIER depends on `age = scoredAt − FeatureAsOf` with `scoredAt = Run.StartedAt` (wall clock, `run.go:95`) — it is **not** a pure function of `(Seed, BusinessDay)`. FRESH tiers require running within `accept_hours (48h)` of `as_of`. This is enforced as a **hard guard**, not a convention: after scoring, `RunLoop` reads each member's decision and **aborts** if any `StalenessOutcome != "FRESH"` (a mistimed run would otherwise silently collapse the whole spread to TIER_01 via `degrade_tier_cap`, `engine.go:276-281`, yet still look green). Re-run idempotency is bounded to the original decision's `valid_until = firstScoredAt + 168h`; past that, `GetOffers`→`requireValidDecision` fails — the loop detects an expired/replayed current decision and **fails loudly** with guidance rather than emitting a confusing empty-offers error.

**AcceptedAt (D6/U1):** `snap.IssuedAt` is minted at `GetOffers` wall-clock (`origination.go:241`) and `Confirm` requires `AcceptedAt ∈ [IssuedAt−2m, ExpiresAt+2m] ∧ ≤ now+2m` (`:667-669`, `acceptanceSkew=2m`). A fixed civil instant is hours before `IssuedAt` → `ErrDisclosureExpired` on the **first** run. `AcceptedAt = time.Now().UTC()` (captured between `GetOffers` and `Confirm`) is always in-window; it is **not** in `confirmRequestHash`, so pinning it bought zero determinism — it is a whitelisted wall input.

### 5.4 Dormancy (P7)
The loop reaches ACTIVE advances and reconciling recoveries **without any arm toggle**:
- Fulfilment: in-process guarded `SyntheticAdapter` — no live egress.
- Scoring: `scoringrun.Run` directly — `scoringsched` stays disarmed.
- Recovery: `recovery.Ingest` directly — the `recharge_webhook` `IsLayerLive(SIM_NG, RECOVERY)` gate stays **false**.
- **Dormancy basis (corrected):** arming rows are created **only** by `SetLive`/`SetLiveArmed` (four-eyes `ApproveArmRecovery`, `recon_arming.go:143`); the loop calls neither, and never `RunRecovery` (freshness). The prior "recon is read-only" premise was false and is dropped. A Slice-3 invariant asserts `IsLayerLive(SIM_NG, RECOVERY)==false` throughout.

---

## 6. Slice plan (gated) — invariants → tests

Each slice is independently buildable and green (owner gate between slices). Integration tests are SKIP-guarded on the DSN env, **fail-not-skip under `CI=1`** (owner "run the suite yourself" rule); the suite is run, not merely read.

### Slice 1 — Loop core (one good-payer, in-process, CONFIRMED)
Wire `configsvc/ledger/SyntheticAdapter/featureingest/scoringrun/origination` in `loop.go` (`//go:build simseed_loop`); add tagged `cmd/simseed -loop -business-day`; drive one good-payer to ACTIVE. No recovery.
- **No direct loop INSERT** — grep/AST over `loop.go`: zero INSERTs into advances/offers/decisions/consents/attempts/ledger.
- **Real settlement** — advance ACTIVE via `ResolveOutcome` (`outbox FulfilmentConfirmed` + state history), not a shortcut.
- **Guards fire** — `VerifySyntheticOnly` first; BYPASSRLS role → refuse; missing `TCP_SEED_ALLOW`/DSN → fatal.
- **Precondition asserts** — resolved `scoring.policy` carries `anti_gaming.spike_action` (PR2); `IngestRaw` `Quarantined==0`, `FeatureFileID` non-empty (ceiling present).
- **FRESH guard** — decision `StalenessOutcome=="FRESH"`, `valid_until>now`; a deliberately stale `as_of` aborts the run (PR6).
- **Replay** — second run same `(Seed,BusinessDay)` → 0 new advances (`GetByIdemKey` reuse), decision replays (`ErrDuplicateRun`), feature `content_hash` identical.

### Slice 2 — Varied population (5 profiles) + scoring outcomes
Add the 5 profiles (§2) + the explicit `ByToken` map (borrowers CONFIRMED; declined unmapped-but-never-originated).
- **Exact outcomes + reason codes** — good-payer TIER_04/50000; varied TIER_02/10000; thin-file `COLD_START_THIN_FILE` starter TIER_01/5000; defaulter TIER_01/5000 (`SPIKE_PATTERN_DETECTED`); declined `MISSING_DATA_REJECTED`, ineligible, **no advance**.
- **Winsorisation falsification (corrected)** — defaulter raw sum 236000 would be **TIER_03**; the raw expectation MUST fail, the winsorised (TIER_01) MUST hold.
- **Token join (U8)** — `Summary.Rows == cohort size`; every cohort token has an `is_current` decision; no non-`tok_seed_%` subscriber was created.
- **Ladder cap** — each advance's rung ≤ `MaxFaceValue`.
- **Treasury floor is real** — default cohort does NOT trip `SUSPEND_PROGRAMME`; an oversized cohort DOES.
- **No assertion on `activity_days_30d`** (PR5, dead input).

### Slice 3 — Loop recoveries → `wh:loop-` + `ReconcileRecoveryDay` MATCHED (condition-4 closer; plumbing-proof)
Add profile-driven `recovery.Ingest` (`wh:loop-`) + exact-sum `recovery_eod_feed` rows.
- **Feed adapter readable** — assert `buildFeedAdapter` returns `MockAdapter` for SIM_NG (needs `telco.recovery_feed=mock` + `is_synthetic`) (U4/R6).
- **Real recovery core** — `recovery_allocations` (fee-first) + ledger `EventRecoveryApplied`; the loop inserts only into `recovery_eod_feed`.
- **Namespace disjoint (R4)** — every loop event `source_event_id LIKE 'wh:loop-%'`; no loop token equals any plain-seed `SeedRecoveryDay` token.
- **Zero reversals (R5)** — no negative `recovery_allocations` on the loop path.
- **RECON MATCHED** — `ReconcileRecoveryDay(ctx,"SIM_NG",BusinessDay)` → 0 breaks; good-payer/varied/thin MATCHED; defaulter/declined absent. **Labelled plumbing-proof, not detection-proof** (U6 — both sides derive from the same Σ; detection is Slice-4).
- **Exact tolerance (R7)** — `amount_tolerance_minor=0`: feed == platform NET to the minor.
- **Hold ordering (R3)** — assert good-payer/thin hold **present before** recon; assert recon **clears** it; varied `PARTIALLY_RECOVERED`, no hold.
- **Day alignment (R8)** — feed `business_date` == Lagos date of events' `occurred_at`; recon invoked for that date.
- **Dormancy** — `IsLayerLive(SIM_NG,RECOVERY)==false` throughout; `RunRecovery` never called (grep-invariant, P4).
- **Determinism** — feed rows byte-identical on re-run; `Ingest` replays, never `ErrDivergentRecovery`.

### Slice 4 (optional integration seam — e2e `testutil` 3-pool harness)
Ambiguity + break detection + dormancy proof, where the admin-pool `next_enquiry_at` poke exists.
- **Resolver path (U7)** — `UNKNOWN`-submit advance parked `FULFILMENT_UNKNOWN` → admin-pool `next_enquiry_at` poke (SQL `now()-interval`, not a persisted wall value) → `fulfilmentresolver.RunOnce` + `EnquireStatus CONFIRMED` (keyed on advance id) → ACTIVE via the SAME `ResolveOutcome`.
- **Detection-proof break classes** — phantom (`wh:loop-` event, no feed) → `BREAK_MISSING_TELCO`; drop (feed, no event) → `BREAK_MISSING_PLATFORM`; mutate (amounts differ) → `BREAK_AMOUNT_MISMATCH`; asserted vs DB/engine truth; `auto_resolve=false`.
- **Arming via four-eyes ONLY (P6)** — `ProposeArmRecovery` + `ApproveArmRecovery` (distinct actor); NEVER `repo.ReconArming.SetLive` directly.
- **Teardown (P6)** — `DisarmRecovery` (`SetDown`) at teardown; assert `IsLayerLive==false` **after** teardown (not merely "throughout") — no latent live-state in a persistent e2e DB.
- **Dead-closed proof** — with the layer unarmed, a live `recharge_webhook` POST is rejected (`RECHARGE_RECON_NOT_LIVE`), proving the loop used the dormant direct-`Ingest` path.

### Files touched
- **new** `internal/simseed/loop.go`, `internal/simseed/adapter.go`, `internal/simseed/profiles.go` — all `//go:build simseed_loop`.
- **new** `cmd/simseed/loop_main.go` (`//go:build simseed_loop`, `-loop`/`-business-day`) + untagged stub.
- **reuse unchanged** `internal/simseed/{simseed.go, recovery.go}` (guards, `stableID`, `msisdnToken`, `SeedCohort`, `eventWallHours`, feed pattern), `build/PHASE1_S3_SEEDER_CONTRACT.md`.
- **new** CI import-fence (cmd/api, cmd/worker ⊄ internal/simseed).
- **tests** `internal/simseed/loop_test.go` (Slices 1-3), `e2e/*` (Slice 4).
- No `cmd/api`/`cmd/worker` changes.

---

## 7. Residual risks + verify-before/while-building checklist

### Retained residual risks (not fully closable in design)
- **RR-1 — cross-DB persistent arming state.** Slice-4 arms a real (four-eyes) layer in a shared/persistent e2e DB; a later `RunRecovery`/scheduler/webhook against that DB could ingest real-shaped money. *Mitigation:* four-eyes arm only, `DisarmRecovery` at teardown, post-teardown `IsLayerLive==false` assert, and Slice-4 confined to an ephemeral `testutil` DB. Slices 1-3 never arm.
- **RR-2 — `decision_valid_hours` (168h) re-run ceiling.** Re-runs beyond the first decision's `valid_until` fail at offers (not a clean replay). *Mitigation:* replay path reuses the booked advance without re-scoring; `RunLoop` detects an expired current decision and fails loudly. A fresh `(Seed, BusinessDay)` (new day) is a new self-consistent seed.
- **RR-3 — UNKNOWN→resolver needs the admin pool.** The single `tcp_app` cmd cannot issue the RLS-privileged `next_enquiry_at` poke. *Mitigation:* the cmd is honestly scoped to the synchronous happy path; UNKNOWN→resolver lives in Slice-4's 3-pool harness (U7).

### Verify against real code before/while building
1. **`repo.Advances.GetByIdemKey` not-found signal** — confirm it returns `repo.ErrNotFound` (not a zero-value + nil) so the replay pre-check branches correctly (`repo/credit.go`, used at `origination.go:569,626`).
2. **`entity.Money` API** — `NewMoney(int64, Currency)`, `Amount()`, `Currency()`, `IsPositive()` (used throughout) — confirm exact names for `mustMoney`/`recoveryAmounts`.
3. **`recon.Service` construction** for the Slice-3 test (Pool/Config/Arming/HTTPClient/Log) — wire as `recon_recovery_test.go` already does; do not add a cmd path to it.
4. **`configsvc.New`/`ledger.New` signatures** — confirm against `e2e/walking_skeleton_test.go`'s `newStack()` (the recipe this loop mirrors).
5. **`repo.RecoveryHolds.HasUncleared` / `ClearMatched`** — confirm the Slice-3 hold before/after assertions read the same rows recon clears (`origination.go:600`, `recon_recovery.go:172`).
6. **product.airtime is `DEDUCTED_UPFRONT`** at build time — the "amount from `adv.FaceValue`" identity (repayment==face) holds only under that fee model (verified now, 0003:34; re-confirm if config changes). If ever `ADDED_TO_REPAYMENT`, derive the target from the booked `Outstanding` captured on the **first** `ConfirmResult`, persisted by the loop, not re-read.
7. **`fulfilmentresolver.New` + `RunOnce`** and the admin-pool poke SQL (Slice-4 only) — confirm against `walking_skeleton_test.go:192-199`.
8. **CI import-fence mechanism** — confirm the repo's existing single-door/authz fences' harness to reuse it for the `cmd/api,cmd/worker ⊄ internal/simseed` rule.

**Net:** Slices 1-3 as a build-tagged cmd are structurally dormant and SIM_NG-bound iff the DSN is the non-BYPASSRLS `tcp_app` role and `VerifySyntheticOnly` runs first — both now enforced inside `RunLoop`. The load-bearing prod-safety hole (a no-network always-SUCCESS adapter in a prod-importable package) is fenced three ways before anything else proceeds, and the recon leg is closed through the correct, freshness-neutral `ReconcileRecoveryDay` over a namespace that cannot collide with the existing seeder.