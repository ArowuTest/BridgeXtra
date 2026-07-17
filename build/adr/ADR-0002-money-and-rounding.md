# ADR-0002: Money Type and Rounding Policy

**Status:** Accepted · 17 July 2026 · Implements BC-1 (ENGINEERING_STANDARD)
**Context:** BC-GAP-1 (HIGH, reviewer VR-3): money as bare `int64 + string` enforces nothing. The Money value object must exist before the M1 saga/ledger/recovery pass money around (V2-API-005, V2-LED-013).

## Decision

1. **`entity.Money` is the ONLY representation of money in `entity/` and above.** Unexported fields (`amount int64` minor units, `currency Currency`) — construction only via `NewMoney`/`MustMoney`, so an unvalidated or currency-less amount cannot exist. Bare `int64` money crossing a function boundary is a review-blocking defect. The DB boundary maps `Money ↔ (BIGINT, CHAR(3))` in the repo layer only.
2. **Mixing currencies is an error, never a coercion.** `Add/Sub/Cmp` on mismatched currencies return `ErrCurrencyMismatch`. The zero value `Money{}` is unusable (every operation errors) — an unset amount can never silently act as ₦0.
3. **All arithmetic is overflow-checked** (`ErrMoneyOverflow`), using `math/big` intermediates where products can exceed int64 (percentage, allocation). Silent wraparound is a corruption class, not an edge case.
4. **No floats, ever, in the money path.** CI greps `float32|float64` out of `backend/internal/entity` and `backend/internal/ledger` (BC-1 guard).
5. **Rounding policy: HALF-UP (round half away from zero), in exactly ONE place.**
   - `PercentBps` (fee/share computation in basis points) is the **single rounding site** in the codebase. Rationale for half-up over banker's: it is the convention Nigerian customers and telco partners reconcile against by hand, it is directionally unbiased at the population level for our distributions, and it is trivially explainable in a regulator/customer dispute (V1 explainability principle). Deterministic: same inputs → same output, always.
   - `AllocateByRatio` (settlement splits, waterfall shares) does **not round at all**: largest-remainder allocation guarantees `Σ parts == total` exactly — nothing is ever lost or created by a split. Ties in remainder distribution break deterministically toward the lowest index.
   - Any future operation that cannot avoid a residual must post it as an **explicit residual entry** (V2-LED-013), never absorb it silently.

## Consequences
- Fee math is expressed in basis points (`int64`), not percentages-as-floats — config records store bps.
- Multi-currency arithmetic is structurally impossible to do by accident; FX (if ever) becomes an explicit conversion service with its own ADR (V1 §21 currency-mismatch edge: reject/quarantine, never auto-convert).
- `money_test.go` carries the exhaustive + property pack (BC-8): 10k randomized allocation cases asserting exact-sum and max-deviation-1-minor-unit, overflow table, mismatch table, half-up boundary table including negatives.
