# BridgeXtra Phase 1 — Scope (design gate, pre-build)

**Goal:** replace the simulator with real MTN feeds + partner authentication — the
first real step toward a live-MNO launch. Design-gated: scoped for
owner/reviewer sign-off BEFORE code, and it needs the MTN integration spec
(below) to build against real endpoints. Mock-first S1 is approved provided it is
boundary-contract-pinned. No code lands against an *unpinned* partner assumption.

Status: scope endorsed by reviewer 23 Jul with four additions (folded in below).

---

## What Phase 1 replaces (current state)

- **Outbound** — `telco.adapter` calls a `fulfilment_url` (the simulator) with
  **NO authentication** to disburse an airtime advance / enquire status.
- **Inbound feeds** — all currently simulator-served:
  - `featureingest.Run` pulls `{fulfilment_url}/v1/telcos/{telco}/feature-file`
    (the scoring input).
  - Recovery ingestion receives recharge/recovery events via the channel API.
  - `recon` reconciles against a telco settlement/fulfilment feed. The recon
    framework is already MULTI-LAYER (`layerSpec`, R-P0-6 Slice D4); only the
    FULFILMENT layer is live — **RECOVERY and SETTLEMENT layerSpecs are dormant,
    waiting for real feeds (Phase 1 arms them).**

## MTN 3-feed model → existing pipeline entry points

| MTN feed | Drives | Pipeline entry | Recon layer it ARMS (addition #1) |
|---|---|---|---|
| **Recharge stream** (real-time top-ups) | Recovery (each recharge repays outstanding advance) | recovery ingestion (event-hash dedup R-P0-2) | — (recovery events; reconciled under RECOVERY layer via the EOD control-total) |
| **EOD balance snapshot** (per-subscriber balance/activity) | Scoring (feature file) + subscriber-status freshness (DD-06 / #46) | featureingest | **registers the RECOVERY layerSpec and is the completeness control-total** (the authoritative daily total the recharge stream is reconciled against) |
| **Settlement file** (daily settlement) | Recon + settlement | recon | **registers the SETTLEMENT layerSpec** |
| **(outbound)** fulfilment + status | Disbursement | `telco.adapter` → MTN | FULFILMENT layer (already live) |

So the feeds are not just data sources: EOD arms RECOVERY reconciliation + is its
completeness floor, and the settlement file arms SETTLEMENT reconciliation.

## Partner authentication (the crux — currently none)

**Outbound (platform → MTN).** The adapter must present credentials. Scheme is
MTN's to specify — likely API-key header, OAuth2 client-credentials (token
endpoint), or mTLS. **Mechanism built here; secrets (keys/certs) are owner-
provisioned Render env / config refs — never held by the builder, never
committed.** Fail-closed: configured-but-secret-absent must refuse, not send
unauthenticated. Routed through the existing SSRF-safe egress client.

**Inbound (MTN → platform).** Every feed must be:
1. **Verified genuinely-from-MTN** — HMAC/signature on the recharge stream, or
   mTLS + IP allowlist + shared secret on the batch files. Unverified = rejected
   (fail closed).
2. **Replay-protected (addition #2)** — a timestamp/nonce window on top of the
   existing payload idempotency, so a captured-and-replayed authentic message is
   rejected even though its idempotency key is well-formed.
3. **Tenant-bound to the authenticated credential, NEVER the payload (addition
   #3, TEN-002/003)** — the telco/tenant is derived from *who authenticated*
   (the presented credential → telco mapping), not from any `telco_id` field in
   the body. A payload claiming a different telco than the credential is rejected.

Config-driven: auth scheme + endpoints extend the `telco.adapter` domain
(validated, seeded); secrets are env references. No hardcoding.

## Slice plan (design-gated; each own commit + falsification tests + CI-green)

- **S1 — Outbound partner auth (mock-first, boundary-contract-pinned).** Pluggable
  adapter auth (`none|apikey|oauth2|mtls`, config-selected; secret via env),
  OAuth2 token cache + refresh, fail-closed on missing secret, via the SSRF-safe
  egress client. **Boundary-contract test pins the exact auth wire format against
  a mock MTN endpoint** so when the real scheme/endpoints arrive the contract
  either matches or fails loudly (no silent drift).
- **S2 — Inbound recharge stream.** Verify (sig/mTLS) + replay-protect +
  tenant-from-credential; ingest real recharge events → recovery, idempotent +
  ordered. Replaces the sim recharge driver.
- **S3 — Real EOD/feature feed.** Verify + parse MTN's balance/feature format →
  featureingest; **register the RECOVERY layerSpec + wire the completeness
  control-total.** Replaces the sim feature-file. Fold in the Phase-0 LOW here
  (strengthen the arming proof to a full `GetOffers` once real features flow).
- **S4 — Real settlement file.** Verify + parse MTN settlement → **register the
  SETTLEMENT layerSpec.** Settlement recon reconciles against **RECOGNIZED fee**
  (addition #4) — respecting the deferred-fee fix (fee income follows recognition
  as recovery lands, not issuance), so the SETTLEMENT layer's expected-fee side
  is the recognized amount, not gross-at-issuance.
- **S5 — Integration proof.** End-to-end against an MTN sandbox/UAT if available,
  else a high-fidelity MTN-format simulator; real-infra smoke + contract tests
  (stub-tested partner assumptions are a HIGH-finding class).

## Design-gate inputs needed from owner/partner BEFORE building real endpoints

1. **Outbound auth scheme** (API key / OAuth2 / mTLS?) + endpoints (token URL,
   fulfilment URL, status URL).
2. **Feed formats + transport** for each of the 3 feeds: push (webhook) vs pull
   (SFTP/poll); JSON/CSV/fixed-width; field specs; frequency/cutoffs.
3. **Inbound feed authentication** (how MTN signs/authenticates what it sends us)
   + the credential→telco mapping (for addition #3) + the replay window semantics
   (for addition #2).
4. **Sandbox/UAT availability** for integration testing.
5. **Recharge stream guarantees** — idempotency keys, ordering, replay semantics.

S1 can start mock-first now (contract-pinned) without 1–5; S2–S5 need them.

---

**Recommendation:** arm the simulator pilot now (owner config step — proof-of-life,
de-risks the loop end to end), start S1 mock-first (contract-pinned), and hold
S2–S5 at this gate until the 5 inputs land.
