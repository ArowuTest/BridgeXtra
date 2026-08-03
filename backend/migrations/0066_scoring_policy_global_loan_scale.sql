-- 0066 — loan-scale correction, GLOBAL fallback scope (follows 0065).
-- ============================================================================
-- 0065 rescaled scoring.policy at scope='programme:prg_sim_airtime01' but NOT the
-- scope='global' default seeded at 0050:97. That global row is the programme→global
-- underwriting FALLBACK: any programme WITHOUT its own scoring.policy override resolves
-- to it. Left at the old 1/100th ladder (₦50–₦500 with 6×/9× gates), the very next
-- no-override programme — exactly the Airtel/Glo onboarding path the 0065 bundle
-- template is for — would lend at 1/100th scale and under a broken affordability ratio.
-- That is the silent under-collateralisation this epic exists to kill, so the global
-- fallback must carry the SAME corrected ₦-scale ladder + true 1:10 gate as the
-- programme scope. (0065 is immutable — already applied — so a new migration.)
--
-- Same dynamic jsonb_set supersede as 0065: read the current ACTIVE global row, close
-- it, insert version_no+1 with only `tiers` replaced — preserving gates / staleness /
-- anti_gaming (incl. spike_action) / starter / movement. Ratio locked by the
-- scope='global' arm of TestScoringPolicyBundle_RatioInvariant.
-- ============================================================================

WITH cur AS (
  SELECT config_version_id, version_no, content FROM config_versions
  WHERE domain = 'scoring.policy' AND scope = 'global' AND state = 'ACTIVE'
  ORDER BY version_no DESC LIMIT 1
), closed AS (
  UPDATE config_versions c SET state = 'SUPERSEDED', effective_to = now(), updated_at = now()
  FROM cur WHERE c.config_version_id = cur.config_version_id
  RETURNING cur.version_no, cur.content
)
INSERT INTO config_versions
  (config_version_id, domain, scope, version_no, state, content, content_hash,
   effective_from, created_by, approved_by, reason)
SELECT 'cfg_seed_scoring_policy_global_v' || (version_no + 1), 'scoring.policy',
       'global', version_no + 1, 'ACTIVE',
       jsonb_set(content, '{tiers}', '[{"code":"TIER_01","max_face_minor":50000,"min_recharge_90d_minor":500000},{"code":"TIER_02","max_face_minor":100000,"min_recharge_90d_minor":1000000},{"code":"TIER_03","max_face_minor":500000,"min_recharge_90d_minor":5000000},{"code":"TIER_04","max_face_minor":1000000,"min_recharge_90d_minor":10000000}]'::jsonb),
       encode(sha256(jsonb_set(content, '{tiers}', '[{"code":"TIER_01","max_face_minor":50000,"min_recharge_90d_minor":500000},{"code":"TIER_02","max_face_minor":100000,"min_recharge_90d_minor":1000000},{"code":"TIER_03","max_face_minor":500000,"min_recharge_90d_minor":5000000},{"code":"TIER_04","max_face_minor":1000000,"min_recharge_90d_minor":10000000}]'::jsonb)::text::bytea), 'hex'),
       now(), 'seed:builder', 'seed:reviewer',
       'Loan-scale: rescale the scoring.policy GLOBAL fallback to the ₦-scale ladder (₦500/₦1k/₦5k/₦10k, true 1:10 gate), matching 0065''s programme scope — so a no-override programme (Airtel/Glo onboarding) never lends at the old 1/100th scale. anti_gaming/starter/movement preserved.'
FROM closed;
