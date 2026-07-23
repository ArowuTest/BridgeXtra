-- 0051_m6b_recharge_webhook.sql — Phase 1 S2 foundations: real MNO recharge-stream
-- webhook credentials + config domain. DORMANT: the seeded feed is DISABLED, so
-- nothing ingests until an operator explicitly arms it AND S3 EOD recon is live.
--
-- Security design (hardened by an adversarial pass — see the handler for the full
-- middleware chain):
--   * The telco is derived ONLY from the authenticated credential (public key_id
--     -> telco), never the request path or body (TEN-002/003). This table is the
--     key_id -> telco map, resolved BEFORE any tenant context — so it is NOT
--     RLS-scoped, exactly like telco_api_credentials.
--   * The HMAC shared secret is NEVER stored: only the NAME of the env var that
--     holds it (secret_env), resolved at verify time (the S1 pattern). A UNIQUE
--     index on secret_env prevents two credentials sharing one secret, which would
--     let one telco forge requests under another's public key_id.

-- ---------------------------------------------------------------------------
-- Webhook credentials (public key_id -> telco + env-var name for the HMAC secret)
-- ---------------------------------------------------------------------------
CREATE TABLE telco_webhook_credentials (
  key_id      TEXT PRIMARY KEY,                    -- PUBLIC identifier shared with the MNO
  telco_id    TEXT NOT NULL REFERENCES telcos(telco_id),
  secret_env  TEXT NOT NULL,                       -- env var NAME holding the HMAC secret (never the secret)
  status      TEXT NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE','REVOKED')),
  label       TEXT NOT NULL,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- One env var (=one secret) per credential: blocks cross-telco forgery via a
-- shared secret used under a different telco's public key_id.
CREATE UNIQUE INDEX telco_webhook_credentials_secret_env_uq
  ON telco_webhook_credentials (secret_env);

-- Resolved pre-tenant by the webhook handler (like telco_api_credentials); the
-- worker/owner reads it cross-tenant for administration. No RLS (identity lookup).
GRANT SELECT, INSERT, UPDATE ON telco_webhook_credentials TO tcp_app;
GRANT SELECT ON telco_webhook_credentials TO tcp_worker;

-- ---------------------------------------------------------------------------
-- Config domain telco.recharge_feed — protocol/limit knobs (NEVER a secret).
-- Seeded GLOBAL default DISABLED (zero-config floor OFF): the feed ingests only
-- when an operator sets enabled:true AND provides a telco-scope row, and only
-- once S3 EOD recon is live as the completeness control-total.
--   replay_window_seconds : freshness horizon (validator clamps [30,300]).
--   future_skew_seconds   : tolerance for a sender clock ahead of ours.
--   max_body_bytes        : cap enforced BEFORE the body is read (DoS guard).
--   expected_currency     : the only currency accepted (reject others).
--   per_event_amount_max_minor / per_telco_daily_ceiling_minor : blast-radius
--     clamps — an over-limit recharge is HELD (not ingested) + alerted, a
--     scaling-bug / forged-feed tripwire, not a business limit.
-- ---------------------------------------------------------------------------
WITH t AS (
  SELECT '{"enabled":false,"transport":"webhook_push","auth":"hmac_sha256","key_id_header":"X-Bx-Key-Id","signature_header":"X-Bx-Signature","timestamp_header":"X-Bx-Timestamp","replay_window_seconds":120,"future_skew_seconds":60,"max_body_bytes":65536,"expected_currency":"NGN","per_event_amount_max_minor":50000000,"per_telco_daily_ceiling_minor":50000000000}'::text AS c
)
INSERT INTO config_versions
  (config_version_id, domain, scope, version_no, state, content, content_hash,
   effective_from, created_by, approved_by, reason)
SELECT 'cfg_seed_recharge_feed_global_v1', 'telco.recharge_feed', 'global', 1, 'ACTIVE',
       t.c::jsonb, encode(sha256(t.c::bytea), 'hex'),
       now(), 'seed:builder', 'seed:reviewer',
       'Seeded telco.recharge_feed global default DISABLED (zero-config floor OFF): webhook_push + hmac_sha256, 120s replay window, 60s future skew, 64KiB body cap, NGN, generous per-event/daily blast-radius clamps. Arming requires enabled:true + a telco-scope row + S3 EOD recon live.'
FROM t;
