-- 0053_m6d_recovery_recon_layer.sql — Phase 1 S3-A: the RECOVERY reconciliation
-- layer's schema + config foundation. Per build/PHASE1_S3_DESIGN.md (hardened by
-- the S3 adversarial pass). DORMANT: seeds are fail-closed; nothing arms until the
-- four-eyes arming path (S3-C) sets a telco's RECOVERY layer live AND a confirmed
-- EOD recon proves the feed fresh (S3-B).
--
-- Three schema changes + two config domains:
--   1. recovery_events.msisdn_token — the raw token the recon RE-RESOLVES to a
--      subscriber at reconciliation time (the "symmetric point-in-time resolver"),
--      so an intra-day port or an unmatched-at-ingest event reconciles correctly.
--      Nullable: production has no wh:% rows yet (the webhook is dead-closed until
--      S3 arms), and the webhook populates it going forward. This is the ONLY touch
--      to the signed-off recovery core — additive, no journal/allocation/state
--      change (surfaced for reviewer source-verify).
--   2. recovery_events_recon_eod_ix — serves the per-subscriber-per-business-day
--      EOD sweep (the existing indexes do not: the received_at index is partial to
--      state='PENDING'). Partial to wh:% (the reconciled channel).
--   3. telcos.is_synthetic — a PRIVILEGED schema marker (un-flippable via config
--      approval) that the arm-maker and the recon loader consult so a REAL telco
--      can never be armed against a mock/self-derived feed (circularity guard).
--      Default false: a new telco is real until a DBA/migration marks it synthetic
--      (fail-closed). SIM_NG is marked here.
-- Config: recon.recovery (GLOBAL fail-closed default) + telco.recovery_feed
-- (SIM_NG mock). Both get registered validators in validators_recovery.go; the
-- recon loader re-asserts every floor at read time (a raw seed bypasses the
-- validator, so the floor lives in code too).

-- ---------------------------------------------------------------------------
-- 1. recovery_events.msisdn_token (symmetric resolver input) — additive, nullable.
--    INSERT grant is table-level (0004), so tcp_app can populate it; the
--    column-scoped UPDATE (0023, state-only) is unchanged — the token is written
--    once at ingest and never mutated.
-- ---------------------------------------------------------------------------
ALTER TABLE recovery_events ADD COLUMN msisdn_token TEXT;

-- ---------------------------------------------------------------------------
-- 2. EOD-sweep index. The recon reads wh:% events for a telco in an occurred_at
--    business-day window and groups by subscriber; INCLUDE carries the money +
--    token so the sweep is index-only. Partial to the reconciled channel.
-- ---------------------------------------------------------------------------
CREATE INDEX recovery_events_recon_eod_ix
  ON recovery_events (telco_id, occurred_at, subscriber_account_id)
  INCLUDE (amount_minor, currency, msisdn_token)
  WHERE source_event_id LIKE 'wh:%';

-- ---------------------------------------------------------------------------
-- 3. telcos.is_synthetic — privileged synthetic marker (config cannot flip it).
--    Default false (fail-closed: a real telco cannot be armed against a mock feed).
-- ---------------------------------------------------------------------------
ALTER TABLE telcos ADD COLUMN is_synthetic BOOLEAN NOT NULL DEFAULT false;
UPDATE telcos SET is_synthetic = true WHERE telco_id = 'SIM_NG';

-- ---------------------------------------------------------------------------
-- 3b. recovery_eod_feed — the synthetic EOD-feed store (source=mock). The seeder
--     (Seeder-C) writes it; the mock feed adapter reads it, RLS-scoped by telco.
--     This is the stand-in for MTN's daily recovery-attributed-deduction file; a
--     REAL telco's feed arrives over https (source=https) and never lands here.
--     One row per (telco, business day, subscriber token). recovery_deducted_minor
--     is MTN's reported per-subscriber-per-day recovery deduction (>=0);
--     closing_balance_minor is an optional cross-check only (never a money authority).
-- ---------------------------------------------------------------------------
CREATE TABLE recovery_eod_feed (
  telco_id                TEXT NOT NULL REFERENCES telcos(telco_id),
  business_date           DATE NOT NULL,
  msisdn_token            TEXT NOT NULL,
  recovery_deducted_minor BIGINT NOT NULL CHECK (recovery_deducted_minor >= 0),
  currency                CHAR(3) NOT NULL,
  closing_balance_minor   BIGINT,
  PRIMARY KEY (telco_id, business_date, msisdn_token)
);
ALTER TABLE recovery_eod_feed ENABLE ROW LEVEL SECURITY;
CREATE POLICY t_recovery_eod_feed ON recovery_eod_feed
  USING (telco_id = current_setting('app.telco_id', true))
  WITH CHECK (telco_id = current_setting('app.telco_id', true));
-- recon reads and the seeder writes, both inside a tenant tx (RLS-scoped).
GRANT SELECT, INSERT ON recovery_eod_feed TO tcp_app;

-- ---------------------------------------------------------------------------
-- 4. Config domain recon.recovery — the RECOVERY-layer recon knobs. Seeded GLOBAL
--    with the zero-tolerance / fail-closed floors from the hardened design:
--      amount_tolerance_minor=0     zero-tolerance money floor
--      auto_resolve=false           a money break is never auto-resolved (V1-FIN-005)
--      max_amount_minor=1e12        credible-amount / overflow ceiling
--      min_completeness_ratio=0.5   re-delivery supersession floor
--      min_confirmation_ratio=0.99  the fraction of BOOKED recovery money the feed
--                                   must confirm before freshness advances (S3-B)
--      recon_lag_seconds=3600       settling lag
--      rereconcile_lookback_seconds=1209600 (14d) late-arrival / backdate re-sweep
--      break_aging_alert_hours=24
--      business_timezone=Africa/Lagos   business-day bucketing (fixed-offset, no DST)
--      arm_freshness_max_seconds=172800 (48h) the freshness window (S3-B)
-- ---------------------------------------------------------------------------
WITH t AS (
  SELECT '{"amount_tolerance_minor":0,"auto_resolve":false,"max_amount_minor":1000000000000,"min_completeness_ratio":0.5,"min_confirmation_ratio":0.99,"recon_lag_seconds":3600,"rereconcile_lookback_seconds":1209600,"break_aging_alert_hours":24,"business_timezone":"Africa/Lagos","arm_freshness_max_seconds":172800}'::text AS c
)
INSERT INTO config_versions
  (config_version_id, domain, scope, version_no, state, content, content_hash,
   effective_from, created_by, approved_by, reason)
SELECT 'cfg_seed_recon_recovery_global_v1', 'recon.recovery', 'global', 1, 'ACTIVE',
       t.c::jsonb, encode(sha256(t.c::bytea), 'hex'),
       now(), 'seed:builder', 'seed:reviewer',
       'Seeded recon.recovery GLOBAL default (S3-A): zero amount tolerance, no auto-resolve, 0.99 money-confirmation floor, Africa/Lagos business-day bucketing, 48h arm-freshness window. Fail-closed floors re-asserted at load.'
FROM t;

-- ---------------------------------------------------------------------------
-- 5. Config domain telco.recovery_feed — the EOD feed adapter selector (per telco,
--    NO global inherit: a feed must be explicitly configured per telco or the
--    loader fails closed). SIM_NG seeded as a MOCK source (synthetic-only; a real
--    telco requires source=https + an envelope HMAC block). business_date_basis is
--    the single supported value — changing it is a coordinated code+config act.
-- ---------------------------------------------------------------------------
WITH t AS (
  SELECT '{"source":"mock","expected_currency":"NGN","business_date_basis":"occurred_at_lagos_date"}'::text AS c
)
INSERT INTO config_versions
  (config_version_id, domain, scope, version_no, state, content, content_hash,
   effective_from, created_by, approved_by, reason)
SELECT 'cfg_seed_recovery_feed_sim_v1', 'telco.recovery_feed', 'telco:SIM_NG', 1, 'ACTIVE',
       t.c::jsonb, encode(sha256(t.c::bytea), 'hex'),
       now(), 'seed:builder', 'seed:reviewer',
       'Seeded telco.recovery_feed for SIM_NG (S3-A): mock EOD feed (synthetic telco only). A real telco requires source=https + envelope_auth HMAC.'
FROM t;
