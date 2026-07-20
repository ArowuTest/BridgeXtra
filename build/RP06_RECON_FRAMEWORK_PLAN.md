# R-P0-6 — Production reconciliation framework (plan)

R-P0-6 (=AUD-P0-006, REC-001..013, FIN-004/005) is the last Gate-A slice and,
per the reviewer, **its own workstream**. The current recon is a walking
skeleton: it rescans all history each run, has no run header / manifest /
control totals, writes fresh rows every rerun with no supersession, does no
input dedup, loads `auto_resolve` but never uses it, and is fulfilment-only.
The money-correctness point-fixes already landed (R-P0-3 currency-before-amount,
R-P0-4 amount ceiling + overflow-safety, R-P0-5 SafeClient egress). This plan
turns the skeleton into a production framework in verifiable slices.

## Slices (each = own commit, adversarial tests, full gate, CI-green)

- **Slice A — Immutable run header + manifest + control totals + supersession.**
  New `recon_runs` header (telco, programme, layer, period, source manifest =
  record_count + monetary_control_total + source_hash, platform manifest,
  outcome counts, state ACTIVE/SUPERSEDED, superseded_by). A rerun over the same
  (telco, programme, layer, period) SUPERSEDES the prior ACTIVE run atomically —
  one ACTIVE run per key (partial unique index). `recon_items.run_id` becomes a
  `NOT VALID` FK to the header (new items must reference a real run; legacy
  orphan items grandfathered — the R-P0-7 legacy-data lesson). This is the
  foundation the rest build on. **← building now.**

- **Slice B — Deterministic input dedup + one-canonical-item-per-match-key +
  duplicate-source classification (R-P2-5=AUD-P2-010).** A stable `match_key`
  per source record; a duplicate telco success record for the same key is
  classified (`BREAK_DUPLICATE_TELCO_RECORD`), never silently double-counted;
  exactly one canonical `recon_item` per (run, match_key). **← building now.**

**Slice A verified clean (reviewer, 8f6e7c6). Two forward notes captured:**
- **(→ Slice D)** the completeness floor correctly rejects an empty/truncated
  feed but will also reject a *legitimately* low-volume day — needs a
  **maker-checker accept-anyway override** so a genuinely quiet day is not stuck
  unreconciled. Fold into the Slice E/D break/exception-resolution path.
- **(→ evidence pack, Slice E)** `source_record_count` is over ALL records but
  `source_control_total_minor` is SUCCESS-only — a defensible but *different*
  population. Surface this explicitly in the statement/evidence pack so the two
  are never read as the same population.

- **Slice C — Period / watermark / bounded scope.** Stop rescanning all history:
  reconcile a governed window `[watermark, now − lag)` and record it on the run
  header, so a run is a bounded statement and the watermark advances. Platform
  side bounded by `advances.activated_at`, telco side by the feed's
  `credited_at`; `recon_lag_seconds` (governed) keeps still-settling records out.
  First run bootstraps from genesis (epoch) → one bounded full-history pass; then
  incremental. One ACTIVE run per `(telco, programme, layer, period_start)` so
  distinct periods coexist and a re-reconcile of one period supersedes only that
  period. **← building now.**

**Slice B verified clean (reviewer). Forward note captured:**
- **(→ Slice C/D)** EDG-006 contradictory-status: a telco feed carrying BOTH a
  FAILED and a SUCCESS record for the same key currently drops the FAILED and
  reconciles the SUCCESS — a data-quality anomaly that should be *flagged*, not
  silently resolved. Classify it in Slice C or D.

- **Slice D — Multi-layer.** Extend the same header/manifest machinery to the
  RECOVERY, SETTLEMENT and BUREAU layers (fulfilment is the reference impl).

- **Slice E — Maker-checker break resolution + signed evidence pack.** Wire the
  governed `auto_resolve=false` floor into an explicit two-actor break-resolution
  path (building on `recon_break_actions`), and emit a signed, reproducible
  evidence pack per run (manifest + hashes + outcome + resolutions).

## Invariants held throughout
- Currency-before-amount, amount ceiling, overflow-safety (R-P0-3/4) unchanged.
- Telco fetch stays behind `egress.SafeClient` (R-P0-5).
- `auto_resolve` floor: a break is NEVER force-matched; ops must resolve it.
- recon_items / recon_runs are append-only money-trail (supersession is a state
  flip + new run, never a rewrite of prior items).
- Manifest control totals make a run self-verifying: source_control_total and
  platform_control_total are recomputable and hashed, so a tampered or partial
  source set is detectable.
