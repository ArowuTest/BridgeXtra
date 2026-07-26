# Recon layer coverage (R-P0-6 Slice D4)

> **GO-LIVE CONTROL (reviewer VR-52, registered at G4 pass):** this file is a
> go-live control map, not just documentation. When a real independent
> telco/bank/bureau source becomes available for RECOVERY, SETTLEMENT, or BUREAU
> (via DD-06 or bureau integration), that layer **must be armed** as a `layerSpec`
> before the corresponding product goes live. Tracked as a Gate-C/D item.

The `recon_runs.layer` column and every index/uniqueness key already carry the
layer, and as of D4 the engine (`reconcileLayer` + `layerSpec`) is layer-generic:
a layer supplies only its name and how to fetch its platform-side money records,
and the header / manifest / control-total / period-watermark / completeness /
override / supersession machinery is shared. Adding a layer is a registration,
not a fork of the engine.

**FULFILMENT and RECOVERY are ARMABLE in production; SETTLEMENT and BUREAU are
not (no independent source yet).** RECOVERY was armed by Phase 1 S3 once a genuine
independent telco-side source was defined — MTN's EOD recovery-attributed-deduction
feed (`build/PHASE1_S3_SEEDER_CONTRACT.md`). Manufacturing a source for SETTLEMENT
or BUREAU would still be a stub; per the no-stubs / no-armed-but-dead rule they stay
un-armed until a genuine source exists.

**VR-52 — RECOVERY forward path CLOSED (Phase 1 S3).** A telco's RECOVERY layer is
armed only through the four-eyes path (`recon-arm-propose` + a distinct
`recon-arm-approve`), gated on: telco ACTIVE, a configured `telco.recovery_feed`,
mock-only-on-synthetic (`telcos.is_synthetic`), and a human-confirmed feed
reversal + business-date basis. "Live" is freshness-coupled (armed ⇒ not live
until a confirmed recon; ages out closed on a stale feed), and the armed+fresh
marker is the SOLE satisfier of the S2 recharge-webhook gate. **The reversal
money-path is an accepted, feed-unreconcilable gap** (a per-subscriber-per-day
`≥0` feed cannot represent a clawback) **with a compensating control** — the
reversal-aware NET platform figure + a ledger self-check (S3-C4 follow-on) — NOT a
silently-claimed full closure. Pen-test scope: the cross-day-reversal + Lagos
business-day-boundary cases (design §5).

| Layer | Independent source? | How it is reconciled today | To arm in this framework |
|-------|--------------------|-----------------------------|--------------------------|
| **FULFILMENT** | Yes — the telco credit log (`/sim/transactions` in M1; a real operator's reconciliation file exchange behind the same canonical shape). | **Armed here.** Platform advances (money OUT) vs telco credits, both directions, under the governed tolerance; the reference `layerSpec`. | — |
| **RECOVERY** | **Yes (Phase 1 S3)** — MTN's EOD recovery-attributed-deduction feed, per subscriber per Lagos business day, pulled independently of the pushed webhook events. | **Armed here (forward path).** The `recoverySpec` reconciles the reversal-aware NET of the telco's `wh:%` `recovery_events` per `(msisdn_token, business-day)` against the EOD feed: a booked recovery the feed does not confirm = `BREAK_MISSING_TELCO` (phantom/forged); a feed deduction not booked = `BREAK_MISSING_PLATFORM` (dropped); never auto-resolved. Still ALSO reconciled at the door (R-P0-2 dedup) — the recon layer adds the independent-source check on top. | Forward path **armed**. The reversal money-path remains a feed-unreconcilable gap (compensating control: NET figure + ledger self-check). |
| **SETTLEMENT** | The counterpart is the platform's own ledger, not a telco feed. | Reconciled by a **purpose-built** mechanism: `settlement.VerifyReproducible` regenerates a statement's lines from the ledger under the pinned terms version and fails `ErrNotReproducible` on any disagreement (statement vs books). Duplicating this as a recon "layer" would be redundant and circular. | Only if a telco/bank **settlement file** (an external counterparty statement) becomes available to reconcile the platform statement against — then a `layerSpec` over `settlement_statements` vs that file. |
| **BUREAU** | No — the bureau pipe is **dormant** (`bureau_export_batches` exists; there is no live bureau, hence no acknowledgement/return source). | Not reconciled — nothing to reconcile against yet. | When a bureau is integrated and returns acknowledgement/rejection files, register a `layerSpec` over `bureau_export_batches` vs the bureau return file. |

## Why this is the right call

- **No stubs.** Arming RECOVERY/SETTLEMENT/BUREAU against fabricated sources would
  reconcile the system against data it made up — worse than not reconciling,
  because it reads as coverage that isn't there.
- **No duplication.** RECOVERY and SETTLEMENT already have correctness controls
  fit to their real data flow (ingest-time dedup; ledger reproducibility). The
  recon framework's value is the two-sided **pull** reconciliation FULFILMENT
  needs; forcing the others through it would not add assurance.
- **Ready, not hollow.** The engine is genuinely layer-generic (proven by
  `recon_rp06d4_test.go`, which drives a second layer end-to-end through the same
  code and shows layer-scoped coexistence + supersession). The moment a real
  source appears for any layer, arming it is a `layerSpec` registration.
