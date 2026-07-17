-- 0008_m2_scoring_policy_v2.sql — supersede the seeded scoring.policy v1.
--
-- Why: v1 seeded anti_gaming.winsor_upper_bps = 9500. On the canonical
-- 13-week window, nearest-rank 95th percentile is rank 13 — the maximum
-- itself — so the winsorisation cap NEVER bound and one giant recharge week
-- could buy the top tier (EDG-013 wash pattern, caught by the engine test
-- pack). The validator now rejects > 9230 bps (12/13); this migration moves
-- the active seed to 9200 so the deployed config matches what the validator
-- would accept. 0007 is immutable (already applied in deployed environments),
-- hence a superseding version rather than an edit.

UPDATE config_versions
SET state = 'SUPERSEDED', effective_to = now(), updated_at = now()
WHERE config_version_id = 'cfg_seed_scoring_policy_v1' AND state = 'ACTIVE';

INSERT INTO config_versions
  (config_version_id, domain, scope, version_no, state, content, content_hash,
   effective_from, created_by, approved_by, reason)
VALUES
  ('cfg_seed_scoring_policy_v2', 'scoring.policy', 'programme:prg_sim_airtime01', 2, 'ACTIVE',
   '{"gates":{"min_tenure_days":90,"blocked_statuses":["BARRED","SELF_EXCLUDED","CLOSED"],"require_activity_days":30},"staleness":{"accept_hours":48,"degrade_hours":168,"degrade_tier_cap":"TIER_01"},"missing_policy":"REJECT","anti_gaming":{"window_days":90,"winsor_upper_bps":9200,"spike_ratio_max_bps":30000,"min_active_days":10},"tiers":[{"code":"TIER_01","max_face_minor":5000,"min_recharge_90d_minor":30000},{"code":"TIER_02","max_face_minor":10000,"min_recharge_90d_minor":90000},{"code":"TIER_03","max_face_minor":20000,"min_recharge_90d_minor":200000},{"code":"TIER_04","max_face_minor":50000,"min_recharge_90d_minor":500000}],"starter_tier":"TIER_01","one_tier_up_max":1,"decision_valid_hours":168}',
   encode(sha256('{"gates":{"min_tenure_days":90,"blocked_statuses":["BARRED","SELF_EXCLUDED","CLOSED"],"require_activity_days":30},"staleness":{"accept_hours":48,"degrade_hours":168,"degrade_tier_cap":"TIER_01"},"missing_policy":"REJECT","anti_gaming":{"window_days":90,"winsor_upper_bps":9200,"spike_ratio_max_bps":30000,"min_active_days":10},"tiers":[{"code":"TIER_01","max_face_minor":5000,"min_recharge_90d_minor":30000},{"code":"TIER_02","max_face_minor":10000,"min_recharge_90d_minor":90000},{"code":"TIER_03","max_face_minor":20000,"min_recharge_90d_minor":200000},{"code":"TIER_04","max_face_minor":50000,"min_recharge_90d_minor":500000}],"starter_tier":"TIER_01","one_tier_up_max":1,"decision_valid_hours":168}'::bytea),'hex'),
   now(), 'seed:builder', 'seed:reviewer',
   'Supersede v1: winsor_upper_bps 9500 -> 9200. At 9500 the 13-week nearest-rank cap equals the max and winsorisation is disarmed (EDG-013 wash pattern buys top tier). 9200 caps at the second-highest week.');
