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
  exactly one canonical `recon_item` per (run, match_key).

- **Slice C — Period / watermark / bounded scope.** Stop rescanning all history:
  reconcile a governed period window with a durable watermark, so a run is a
  bounded, repeatable statement of one period.

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
