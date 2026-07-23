# BridgeXtra Phase 1 S2 — Assumed Recharge-Webhook Contract (= the MTN ask)

**Living document.** S2 is built mock-first against the *assumed* contract below,
behind a config-selected auth/transport adapter, so the real MTN specifics are an
**adapter swap, not a rebuild**. Every assumption here is marked **UNVERIFIED**
and is pinned by boundary-contract mock tests — when the real MTN spec lands, each
assumption is either confirmed or the test fails loudly. **This section is the
list of questions to put to MTN.**

Security design hardened by an adversarial pass (27 candidates → 22 fixes incl. a
BLOCKER kill-switch fail-open); see `project_bridgextra_phase1_s2` memory / the
handler for the full middleware chain.

## Assumed wire contract (UNVERIFIED — confirm with MTN)

**Transport** (assumed `webhook_push`): MTN POSTs each recharge event to
`POST /v1/telcos/{telco}/recharge-webhook`. *Ask MTN: push webhook or pull
(SFTP/poll)? delivery guarantees (at-least-once? ordering? retry policy)?*

**Headers** (names are config, not code — assumed):
- `X-Bx-Key-Id` — public credential id (maps to telco + HMAC secret). *Ask: does MTN send a key id / how do they identify themselves?*
- `X-Bx-Timestamp` — epoch **seconds**. *Ask: format & unit (seconds vs millis vs RFC3339)?*
- `X-Bx-Signature` — lowercase hex HMAC-SHA256. *Ask: algorithm, encoding (hex/base64), and exactly what bytes are signed?*

**Signed content** (assumed): `"bridgextra.recharge_webhook.v1" \n key_id \n timestamp \n rawBody` (domain-separated; body last). *Ask: MTN's canonical signing string.*

**Body fields** (assumed JSON; the field-mapping adapter maps these → canonical):
| Assumed field | Maps to | Notes / MTN ask |
|---|---|---|
| stable **event_id** | `source_event_id` (namespaced `wh:<id>`) | **HARD requirement** — the money-core idempotency key. *Ask: is it present, globally unique, stable across retries?* |
| **MSISDN** (or token) | `msisdn_token` | *Ask: raw MSISDN or a token? If raw, how do we map to our subscriber (no tokenizer exists yet)?* Unknown token → preserved UNMATCHED (never dropped). |
| **recharge amount** | `amount` (integer **minor units** + currency) | *Ask: minor units or decimal? currency field? We reject non-integer / non-NGN.* |
| **borrowed balance** | *dropped (untrusted)* | Documented, ignored — never a money authority. *Ask: what does MTN mean by this; is it needed for recon?* |
| **timestamp** | `occurred_at` | business event time (distinct from the signing timestamp). |

## Fail-closed posture (built now)

- Feed **DISABLED** by default; arming needs an explicit `enabled:true` telco-scope config **and** S3 EOD recon live (the completeness control-total). Kill-switch gates the money path (Step 0, before any HMAC/body work).
- Telco derives **only** from the authenticated credential (`key_id → telco`); path/body telco is cross-checked, never trusted (TEN-002/003) → `403 TENANT_CONTEXT_MISMATCH` + audit.
- HMAC verified constant-time on decoded 32-byte MAC; uniform `401` (+ dummy HMAC) for unknown/revoked key_id, empty secret, bad signature, bad/absent timestamp — no oracle.
- Freshness: `-replay_window ≤ (now − ts) ≤ +future_skew` (asymmetric; no future-dating). Window clamped 30–300s.
- Replay defense-in-depth: a seen (decoded-MAC) nonce is idempotent success (never a 409 against a legit retry); recovery idempotency by `source_event_id` is the durable backstop; body capped before read.
- Blast-radius clamps: per-event amount + per-telco daily ceiling → over-limit **HELD** (not ingested) + audit + alert.
- Secrets are **env-var names only** (`telco_webhook_credentials.secret_env`, unique per credential); the secret never enters config, DB, or logs.

## Provisioning (out-of-band, owner)

1. Register a credential: `telco_webhook_credentials(key_id, telco_id, secret_env, label)`.
2. Provision the HMAC secret as the named env var on the API service.
3. Activate a `telco.recharge_feed` telco-scope config `{enabled:true, ...}`.
4. (Prereq) S3 EOD recon live so the recharge stream is reconciled against the daily control-total.

## Go-live gate

The mock adapter is UNVERIFIED. Real cutover is gated on a **captured real MTN
sample** replayed through the real-adapter implementation of the same interface —
so any byte-level drift from these assumptions fails in test, never silently at
cutover.
