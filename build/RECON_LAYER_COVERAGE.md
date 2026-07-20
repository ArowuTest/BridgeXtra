# Recon layer coverage (R-P0-6 Slice D4)

The `recon_runs.layer` column and every index/uniqueness key already carry the
layer, and as of D4 the engine (`reconcileLayer` + `layerSpec`) is layer-generic:
a layer supplies only its name and how to fetch its platform-side money records,
and the header / manifest / control-total / period-watermark / completeness /
override / supersession machinery is shared. Adding a layer is a registration,
not a fork of the engine.

**Only FULFILMENT is ARMED in production.** This is a deliberate, honest scoping
decision, not an omission — the other three layers named in the plan do NOT have
an independent telco-side pull source to reconcile against, and manufacturing one
(e.g. a fake `/sim/recoveries` endpoint) would be a stub reconciling the system
against a source it invented. Per the no-stubs / no-armed-but-dead rule a layer
is armed only when a genuine source exists.

| Layer | Independent source? | How it is reconciled today | To arm in this framework |
|-------|--------------------|-----------------------------|--------------------------|
| **FULFILMENT** | Yes — the telco credit log (`/sim/transactions` in M1; a real operator's reconciliation file exchange behind the same canonical shape). | **Armed here.** Platform advances (money OUT) vs telco credits, both directions, under the governed tolerance; the reference `layerSpec`. | — |
| **RECOVERY** | No separate pull source — recovery events are **pushed** to the platform (`recovery.Ingest`) and the platform's `recovery_events` ARE the telco's reported deductions. | Reconciled **at the door**: R-P0-2 gives idempotent event-hash dedup, exact-original-response on replay, and a DIVERGENT_DUPLICATE audit when a `source_event_id` is reused with a changed payload. Allocation + ledger posting is invariant-checked by BC-3. | A telco-authoritative deduction ledger that can be **pulled** independently of what was pushed (so the pulled ledger can be reconciled against the ingested copy). Register a `layerSpec` whose platform side is `recovery_events` and whose telco source is that ledger. |
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
