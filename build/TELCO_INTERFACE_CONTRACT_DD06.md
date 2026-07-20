# Telco Interface Contract — Disclosure & Consent Evidence (DD-06 / R-P0-7)

Status: **R1 pre-pilot**. This documents the responsibility split for consent /
channel disclosure evidence between the platform and the telco/aggregator that
owns the customer-facing channel (USSD gateway, app). It is the reference the
DD-06 telco delta-feed work and the pilot integration test against.

## Why this exists (R-P0-7 / AUD-P0-007, REG-002, PRD-002/003, EDG-028)

A confirm used to prove only *which offer existed*. It reconstructed the terms
server-side and hardcoded `Channel:"USSD"`. It did **not** prove that *those
exact terms were rendered to the customer and accepted through a real channel
session*. That is a regulatory conduct gap. The fix binds every advance to the
exact disclosure the customer was shown and to channel/session/acceptance
evidence.

## The flow

1. **Platform mints a disclosure snapshot** at menu generation (`GET /v1/offers`),
   one per offer, 1:1 and append-only. It carries the pinned template id +
   version, locale, the **exact rendered disclosure text**, the total-cost
   representation, the money amounts, a server-computed **content hash**, and an
   expiry equal to the offer's (short-lived). The offer response returns
   `disclosure_ref`, `disclosure_text`, `total_cost_text`, `disclosure_locale`,
   `template_id`, `template_version`, `disclosure_expires_at`.

2. **Channel renders `disclosure_text` verbatim** to the customer and captures
   the customer's explicit acceptance keypress, the channel session id, and the
   acceptance timestamp.

3. **Channel confirms** (`POST /v1/advances`) echoing:
   - `disclosure_ref` — the reference from the offer (proves *which* disclosure);
   - `channel` — must be one the programme's `disclosure.policy.allowed_channels`
     permits (this de-hardcodes USSD);
   - `session_id` — the telco channel session identifier;
   - `accepted_at` — RFC3339; must fall within the disclosure's validity window;
   - `telco_evidence` — OPTIONAL, see below.

4. **Platform verifies and records.** The confirm is refused unless the echoed
   `disclosure_ref` resolves to the canonical snapshot **for that offer**, the
   channel is allowed, session/acceptance evidence is present, and `accepted_at`
   is inside the disclosure window. On success the consent record stores the
   snapshot binding, its content hash, the channel/session/acceptance evidence,
   and the exact disclosed text — inside the confirm transaction, so an advance
   cannot exist without it.

## Responsibility split

| Evidence | Owner | Notes |
|---|---|---|
| Disclosure template + version + locale | Platform (`disclosure.policy` config) | Governed, maker-checker; no hardcoding. |
| Rendered disclosure text + content hash | Platform | Minted at menu time; append-only; the integrity anchor. |
| Verbatim rendering to the customer | **Telco/channel** | Must render `disclosure_text` unchanged. |
| Channel `session_id` | **Telco/channel** | Opaque; supplied on confirm. |
| `accepted_at` | **Telco/channel** | The customer's acceptance moment. |
| `channel` | **Telco/channel** | Validated against the allow-list. |
| `telco_evidence` (acceptance signature) | **Telco/channel** | See below. |

## `telco_evidence` — the telco acceptance signature (DD-06, not yet enforced)

Some jurisdictions/telcos can produce a cryptographic, third-party-verifiable
signature over the acceptance (e.g. the USSD gateway signing the session +
disclosure hash + keypress). Where the telco can supply it, it is passed as the
optional `telco_evidence` object and stored alongside the consent record.

For R1 this field is **accepted and retained but not required or verified** — it
depends on telco capability that DD-06 will formalise. The platform-side binding
(append-only snapshot + content hash + session/acceptance capture) is the
enforced integrity guarantee; `telco_evidence` strengthens it where available.
When DD-06 lands, this contract will specify the exact signing payload
(canonical: `disclosure_ref | content_hash | session_id | accepted_at`), the key
distribution, and whether verification becomes mandatory per telco.

## Design note (for reviewers)

The platform-side integrity guarantee is a **server-persisted, append-only,
content-hashed disclosure snapshot**, not an HMAC-signed stateless token. This
was a deliberate choice over a signed token:

- The snapshot is the codebase's proven evidence pattern (mirrors
  `decision_snapshots`), append-only with RLS, and directly queryable for the
  regulatory / support-timeline retention R-P2-11 wants.
- A client cannot fabricate a `disclosure_ref` that resolves to another offer —
  binding is enforced by DB lookup, at least as strong as an HMAC for the
  platform's own verification, with **no new secret / key-management surface**
  (which would itself be a review finding given the standing KMS concern).
- The genuinely telco-originated, offline-verifiable **signature** is scoped to
  DD-06 (`telco_evidence`), exactly where the reviewer placed it.
