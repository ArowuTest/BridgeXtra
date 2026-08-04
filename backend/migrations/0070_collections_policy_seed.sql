-- 0070 — Collections write-off DISPUTE GATE policy (Wave B.3 Phase B). A new governed domain
-- collections.policy (validated in validators_collections.go): which complaint categories count
-- as a DEBT dispute that gates a write-off, and whether an audited override may proceed. Seeded
-- per programme (mirrors delinquency.buckets / writeoff.policy scope). Owner decision:
-- soft-block on a debt dispute with an audited override on the checker; scoped to debt disputes
-- only so an unrelated service complaint cannot block a legitimate write-off.

INSERT INTO config_versions
  (config_version_id, domain, scope, version_no, state, content, content_hash,
   effective_from, created_by, approved_by, reason)
VALUES
  ('cfg_seed_collections_policy_v1', 'collections.policy', 'programme:prg_sim_airtime01', 1, 'ACTIVE',
   '{"debt_dispute_categories":["DISPUTED_ADVANCE","DISPUTED_RECOVERY"],"allow_override_on_dispute":true}',
   encode(sha256('{"debt_dispute_categories":["DISPUTED_ADVANCE","DISPUTED_RECOVERY"],"allow_override_on_dispute":true}'::bytea),'hex'),
   now(), 'seed:builder', 'seed:reviewer',
   'Seeded collections.policy (Phase B write-off dispute gate): DISPUTED_ADVANCE/DISPUTED_RECOVERY gate a write-off, override permitted (audited on the checker).');
