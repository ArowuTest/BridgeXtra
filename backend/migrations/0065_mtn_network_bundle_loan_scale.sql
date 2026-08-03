-- 0065 — MTN NETWORK "MARKET BUNDLE": loan-scale correction (coordinated).
-- ============================================================================
-- Loan amounts are stored in KOBO (minor units; ÷100 = ₦). The walking-skeleton
-- seed put the offer ladder + every coupled guardrail at a kobo-MISREAD scale
-- ([5000,10000,20000,50000] kobo = ₦50/₦100/₦200/₦500) when the real product
-- lends ₦-scale advances. Correcting the loan size means moving EVERY coupled
-- monetary value together — a naïve "×N the denominations" leaves the guardrails
-- and the underwriting gate behind and SILENTLY breaks lending (empty offer
-- ladder, or an auto-suspended programme), because the config validators only
-- check >0 / ascending, not scale. Source: 6-agent scale-dependency audit.
--
-- This migration IS the per-network bundle TEMPLATE. To onboard another network
-- (Airtel/Glo): copy this file, change the programme scope + the network's
-- NUMBERS below (denomination ladder, tier ladder, guardrail caps, pool capital)
-- and KEEP the % invariants — fee_bps (the fee RATE), max_open_exposure_bps
-- (self-scales off committed capital), and the anti-gaming ratios (bps).
--
-- Each config ships as a NEW superseding ACTIVE version using the DYNAMIC
-- supersede pattern (mirrors 0010): read the CURRENT ACTIVE, close it, and insert
-- version_no+1 with jsonb_set changing ONLY the rescaled key. This is robust to
-- earlier migrations that already bumped a domain (0010 pushed scoring.policy to
-- v3 and added anti_gaming.spike_action) — jsonb_set preserves every other field,
-- and version_no+1 never collides. content_hash is sha256 over the canonical
-- jsonb text, exactly as 0010 computes it.
--
-- MTN BUNDLE NUMBERS (kobo):
--   Advance range           ₦100 – ₦10,000        (10000 – 1000000)
--   Denomination ladder     ₦100/500/1k/2k/5k/10k [10000,50000,100000,200000,500000,1000000]
--   Tier limits (10× gate)  T1 ₦500  gate ₦5,000    (50000 / 500000)
--                           T2 ₦1,000 gate ₦10,000  (100000 / 1000000)
--                           T3 ₦5,000 gate ₦50,000  (500000 / 5000000)
--                           T4 ₦10,000 gate ₦100,000 (1000000 / 10000000)
--   Fee                     10% upfront (fee_bps 1000 — INVARIANT rate)
--   DAILY_DISBURSED cap     ₦5,000,000/day (500000000) — GENEROUS PILOT DEFAULT;
--                           a velocity/anomaly tripwire, NOT a capital limit.
--                           SIZE against real projected daily originations × avg
--                           face before any volume scale-up.
--   Funding pool capital    ₦50,000,000 (5000000000) — generous pilot committed
--                           capital; open-exposure stays 80%-of-committed (bps).
--   Recharge daily ceiling  ₦5,000,000,000/day (500000000000) — recovery-flow
--                           headroom for a scaled book; per-event clamp ₦500k
--                           keeps confirmed headroom (unchanged).
-- ============================================================================

-- ── 1. product.airtime — the OFFER DENOMINATION LADDER ──────────────────────
-- The offer menu. buildLadder clamps each denomination to the subscriber's
-- decision_snapshots.max_face_value_minor; if the smallest denomination exceeds
-- that ceiling the ladder is EMPTY → silent total-lending outage. New floor
-- (₦100) sits at/under every tier limit (lowest ₦500), so the invariant holds.
WITH cur AS (
  SELECT config_version_id, version_no, content FROM config_versions
  WHERE domain = 'product.airtime' AND scope = 'programme:prg_sim_airtime01' AND state = 'ACTIVE'
  ORDER BY version_no DESC LIMIT 1
), closed AS (
  UPDATE config_versions c SET state = 'SUPERSEDED', effective_to = now(), updated_at = now()
  FROM cur WHERE c.config_version_id = cur.config_version_id
  RETURNING cur.version_no, cur.content
)
INSERT INTO config_versions
  (config_version_id, domain, scope, version_no, state, content, content_hash,
   effective_from, created_by, approved_by, reason)
SELECT 'cfg_seed_product_airtime_v' || (version_no + 1), 'product.airtime',
       'programme:prg_sim_airtime01', version_no + 1, 'ACTIVE',
       jsonb_set(content, '{denominations_minor}', '[10000,50000,100000,200000,500000,1000000]'::jsonb),
       encode(sha256(jsonb_set(content, '{denominations_minor}', '[10000,50000,100000,200000,500000,1000000]'::jsonb)::text::bytea), 'hex'),
       now(), 'seed:builder', 'seed:reviewer',
       'MTN bundle: real ₦100–₦10,000 denomination ladder (was kobo-misread ₦50–₦500). Fee rate 10% invariant.'
FROM closed;

-- ── 2. scoring.policy — the TIER LADDER + underwriting gate ──────────────────
-- max_face_minor IS the loan size a tier may borrow; min_recharge_90d_minor is
-- the affordability gate. They move in lockstep to preserve the 1:10
-- advance-to-90d-recharge ratio (locked by TestScoringPolicyBundle_RatioInvariant).
-- The engine reads these keys verbatim and passes max_face straight to
-- decision_snapshots — no code change. jsonb_set replaces only `tiers`, keeping
-- gates / staleness / anti_gaming (incl. 0010's spike_action) / starter / movement.
WITH cur AS (
  SELECT config_version_id, version_no, content FROM config_versions
  WHERE domain = 'scoring.policy' AND scope = 'programme:prg_sim_airtime01' AND state = 'ACTIVE'
  ORDER BY version_no DESC LIMIT 1
), closed AS (
  UPDATE config_versions c SET state = 'SUPERSEDED', effective_to = now(), updated_at = now()
  FROM cur WHERE c.config_version_id = cur.config_version_id
  RETURNING cur.version_no, cur.content
)
INSERT INTO config_versions
  (config_version_id, domain, scope, version_no, state, content, content_hash,
   effective_from, created_by, approved_by, reason)
SELECT 'cfg_seed_scoring_policy_v' || (version_no + 1), 'scoring.policy',
       'programme:prg_sim_airtime01', version_no + 1, 'ACTIVE',
       jsonb_set(content, '{tiers}', '[{"code":"TIER_01","max_face_minor":50000,"min_recharge_90d_minor":500000},{"code":"TIER_02","max_face_minor":100000,"min_recharge_90d_minor":1000000},{"code":"TIER_03","max_face_minor":500000,"min_recharge_90d_minor":5000000},{"code":"TIER_04","max_face_minor":1000000,"min_recharge_90d_minor":10000000}]'::jsonb),
       encode(sha256(jsonb_set(content, '{tiers}', '[{"code":"TIER_01","max_face_minor":50000,"min_recharge_90d_minor":500000},{"code":"TIER_02","max_face_minor":100000,"min_recharge_90d_minor":1000000},{"code":"TIER_03","max_face_minor":500000,"min_recharge_90d_minor":5000000},{"code":"TIER_04","max_face_minor":1000000,"min_recharge_90d_minor":10000000}]'::jsonb)::text::bytea), 'hex'),
       now(), 'seed:builder', 'seed:reviewer',
       'MTN bundle: tier limits ₦500/₦1k/₦5k/₦10k with a true 1:10 min_recharge_90d gate (was ₦50–₦500 at ~6–10×). Anti-gaming/starter/movement invariant.'
FROM closed;

-- ── 3. treasury.guardrails — DAILY_DISBURSED velocity cap ────────────────────
-- DAILY_DISBURSED breach aborts the confirm AND auto-suspends the programme
-- (2-person re-arm). At the old ₦500k/day, the bigger advances would trip it on
-- a normal pilot day and halt lending platform-wide. Raised to a generous pilot
-- default; exposure stays 80%-of-committed (bps, self-scales off the pool).
WITH cur AS (
  SELECT config_version_id, version_no, content FROM config_versions
  WHERE domain = 'treasury.guardrails' AND scope = 'programme:prg_sim_airtime01' AND state = 'ACTIVE'
  ORDER BY version_no DESC LIMIT 1
), closed AS (
  UPDATE config_versions c SET state = 'SUPERSEDED', effective_to = now(), updated_at = now()
  FROM cur WHERE c.config_version_id = cur.config_version_id
  RETURNING cur.version_no, cur.content
)
INSERT INTO config_versions
  (config_version_id, domain, scope, version_no, state, content, content_hash,
   effective_from, created_by, approved_by, reason)
SELECT 'cfg_seed_treasury_guardrails_v' || (version_no + 1), 'treasury.guardrails',
       'programme:prg_sim_airtime01', version_no + 1, 'ACTIVE',
       jsonb_set(content, '{max_daily_disbursed_minor}', '500000000'::jsonb),
       encode(sha256(jsonb_set(content, '{max_daily_disbursed_minor}', '500000000'::jsonb)::text::bytea), 'hex'),
       now(), 'seed:builder', 'seed:reviewer',
       'MTN bundle: DAILY_DISBURSED ₦500k→₦5M/day (generous pilot velocity tripwire; size to real projected volume before scale-up). Exposure bps invariant.'
FROM closed;

-- ── 4. telco.recharge_feed — recharge-ingest daily ceiling (scope global) ────
-- Blast-radius tripwire. A scaled book drives more recovery-recharge ingest;
-- raise the daily ceiling so HELDs don't stall recovery. Per-event clamp keeps
-- its confirmed headroom. Feed stays DORMANT (enabled:false).
WITH cur AS (
  SELECT config_version_id, version_no, content FROM config_versions
  WHERE domain = 'telco.recharge_feed' AND scope = 'global' AND state = 'ACTIVE'
  ORDER BY version_no DESC LIMIT 1
), closed AS (
  UPDATE config_versions c SET state = 'SUPERSEDED', effective_to = now(), updated_at = now()
  FROM cur WHERE c.config_version_id = cur.config_version_id
  RETURNING cur.version_no, cur.content
)
INSERT INTO config_versions
  (config_version_id, domain, scope, version_no, state, content, content_hash,
   effective_from, created_by, approved_by, reason)
SELECT 'cfg_seed_recharge_feed_global_v' || (version_no + 1), 'telco.recharge_feed',
       'global', version_no + 1, 'ACTIVE',
       jsonb_set(content, '{per_telco_daily_ceiling_minor}', '500000000000'::jsonb),
       encode(sha256(jsonb_set(content, '{per_telco_daily_ceiling_minor}', '500000000000'::jsonb)::text::bytea), 'hex'),
       now(), 'seed:builder', 'seed:reviewer',
       'MTN bundle: per-telco daily recharge ceiling ₦500M→₦5B/day (recovery-flow headroom for a scaled book). Per-event clamp unchanged; feed stays DORMANT.'
FROM closed;

-- ── 5. Seed-row corrections (0004 fixtures are immutable; correct in place) ──
-- funding_pools: committed capital for the walking-skeleton pool → ₦50M so the
-- 80% open-exposure cap doesn't bind at the new advance scale.
UPDATE funding_pools SET committed_minor = 5000000000
WHERE pool_id = 'pool_sim_01' AND committed_minor = 100000000;

-- decision_snapshots: the one HAND-SEEDED (not engine-scored) fixture ceiling.
-- Every seeded-cohort subscriber is engine-scored, so they auto-scale off the new
-- scoring.policy; only sub_sim_0001's fixed snapshot must be corrected → ₦10,000
-- (new TIER_04) so its offer ladder isn't clamped to the old ₦500 sub-floor.
UPDATE decision_snapshots SET max_face_value_minor = 1000000
WHERE decision_snapshot_id = 'dec_sim_0001' AND max_face_value_minor = 50000;
