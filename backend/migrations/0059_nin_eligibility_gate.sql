-- 0059_nin_eligibility_gate.sql — Build 2: the NIN identity-verification go-live gate.
-- MTN verifies each subscriber's NIN and sends us a BOOLEAN flag (never the raw NIN).
-- Origination refuses to lend to a subscriber whose flag is not TRUE — NULL (unknown,
-- MTN hasn't sent it) or false both fail closed (reachability discipline: absent =
-- not verified = no loan).
--
-- ONE column, updated IN PLACE (no history rows — the owner is cost-conscious; this
-- must not accumulate). The flag rides the existing subscriber feature feed
-- (featureingest upserts it per msisdn_token); the raw NIN is never sent or stored.

-- ---------------------------------------------------------------------------
-- 1. nin_verified — nullable, default NULL (unknown). featureingest updates it in
--    place; origination reads it fail-closed. Column-scoped UPDATE grant for tcp_app
--    (the broad 0004 grant already covers it, but the column grant documents intent
--    and survives any future broad-revoke — EXT-3 discipline).
-- ---------------------------------------------------------------------------
ALTER TABLE subscriber_accounts ADD COLUMN nin_verified BOOLEAN;
GRANT UPDATE (nin_verified) ON subscriber_accounts TO tcp_app;

-- The synthetic pilot subscriber is verified, so the existing SIM_NG origination
-- flows (tests, simulator, walking skeleton) keep working. Real subscribers stay
-- NULL until MTN's feed sets them — and cannot borrow until then.
UPDATE subscriber_accounts SET nin_verified = true WHERE subscriber_account_id = 'sub_sim_0001';

-- ---------------------------------------------------------------------------
-- 2. origination.nin_gate — whether the NIN requirement is enforced is itself
--    governed config (no-hardcoding): seeded require_nin_verified=true (ON by
--    default) with a governed maker-checker opt-out path if ever needed. Origination
--    reads it fail-closed: an ABSENT config means REQUIRE (absent must never mean
--    "off"). GLOBAL scope (platform-wide regulatory requirement).
-- ---------------------------------------------------------------------------
WITH t AS (
  SELECT '{"require_nin_verified":true}'::text AS c
)
INSERT INTO config_versions
  (config_version_id, domain, scope, version_no, state, content, content_hash,
   effective_from, created_by, approved_by, reason)
SELECT 'cfg_seed_nin_gate_global_v1', 'origination.nin_gate', 'global', 1, 'ACTIVE',
       t.c::jsonb, encode(sha256(t.c::bytea), 'hex'),
       now(), 'seed:builder', 'seed:reviewer',
       'Seeded origination.nin_gate GLOBAL (Build 2): require_nin_verified=true. Origination refuses a subscriber whose nin_verified flag is not TRUE; enforcement is read fail-closed (absent config => require).'
FROM t;
