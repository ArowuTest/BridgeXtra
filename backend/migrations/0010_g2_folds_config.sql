-- 0010_g2_folds_config.sql — config changes for the G2 gate folds.
--
-- G2-F2: anti_gaming.spike_action becomes a REQUIRED, engine-implemented
-- policy decision (FLAG_ONLY | CAP_TO_STARTER). scoring.policy v2 is
-- superseded by v3 carrying the explicit conservative default FLAG_ONLY
-- (winsorisation remains the discount; the flag records the fact).
--
-- G2-F3: telco.adapter gains max_weekly_recharge_minor — the feature-feed
-- plausibility ceiling ingest quarantines against. The ACTIVE adapter row is
-- superseded DYNAMICALLY (content-preserving) because environments differ:
-- fresh databases carry the 0003 seed while deployed environments carry an
-- admin-repointed version. Seeded ceiling: 100,000,000 kobo (=NGN 1,000,000
-- per week) — generous for any real subscriber, fatal for unit-error rows.

-- ---- G2-F2: scoring.policy v3 --------------------------------------------
-- scoring.policy for this scope has only ever been the 0007/0008 seeds (no
-- admin edits in any environment), so a content-preserving jsonb_set on the
-- known v2 content is exact.
WITH cur AS (
  SELECT config_version_id, version_no, content
  FROM config_versions
  WHERE domain = 'scoring.policy' AND scope = 'programme:prg_sim_airtime01' AND state = 'ACTIVE'
  ORDER BY version_no DESC LIMIT 1
), closed AS (
  UPDATE config_versions c
  SET state = 'SUPERSEDED', effective_to = now(), updated_at = now()
  FROM cur WHERE c.config_version_id = cur.config_version_id
  RETURNING cur.version_no, cur.content
)
INSERT INTO config_versions
  (config_version_id, domain, scope, version_no, state, content, content_hash,
   effective_from, created_by, approved_by, reason)
SELECT
  'cfg_seed_scoring_policy_v' || (version_no + 1),
  'scoring.policy', 'programme:prg_sim_airtime01', version_no + 1, 'ACTIVE',
  jsonb_set(content, '{anti_gaming,spike_action}', '"FLAG_ONLY"'),
  encode(sha256(jsonb_set(content, '{anti_gaming,spike_action}', '"FLAG_ONLY"')::text::bytea), 'hex'),
  now(), 'seed:builder', 'seed:reviewer',
  'G2-F2: spike consequence is an explicit engine-implemented decision; seeded FLAG_ONLY (winsorisation is the discount, the flag records the fact)'
FROM closed;

-- ---- G2-F3: telco.adapter ceiling ----------------------------------------
WITH cur AS (
  SELECT config_version_id, domain, scope, version_no, content
  FROM config_versions
  WHERE domain = 'telco.adapter' AND scope = 'telco:SIM_NG' AND state = 'ACTIVE'
  ORDER BY version_no DESC LIMIT 1
), closed AS (
  UPDATE config_versions c
  SET state = 'SUPERSEDED', effective_to = now(), updated_at = now()
  FROM cur WHERE c.config_version_id = cur.config_version_id
  RETURNING cur.version_no, cur.content
)
INSERT INTO config_versions
  (config_version_id, domain, scope, version_no, state, content, content_hash,
   effective_from, created_by, approved_by, reason)
SELECT
  'cfg_seed_adapter_ceiling_v' || (version_no + 1),
  'telco.adapter', 'telco:SIM_NG', version_no + 1, 'ACTIVE',
  content || '{"max_weekly_recharge_minor":100000000}'::jsonb,
  encode(sha256((content || '{"max_weekly_recharge_minor":100000000}'::jsonb)::text::bytea), 'hex'),
  now(), 'seed:builder', 'seed:reviewer',
  'G2-F3: feature-feed plausibility ceiling (NGN 1,000,000/week) — ingest quarantines implausible rows; absent ceiling refuses the feed'
FROM closed;
