-- 0033_rp08b_adapter_circuit_cooldown.sql — R-P0-8b. The adapter circuit
-- breaker is now live and reads circuit_cooldown_seconds, which the seeded
-- telco.adapter config lacks. Add it by SUPERSEDING whatever version is
-- currently ACTIVE for telco:SIM_NG (0010 already bumped it to add the feed
-- ceiling), version_no + 1, merging the cooldown into the existing content —
-- the same data-driven supersede pattern as 0010, so no version number is
-- hardcoded and the exclusion constraint is satisfied.

WITH cur AS (
  SELECT config_version_id, version_no, content
  FROM config_versions
  WHERE domain = 'telco.adapter' AND scope = 'telco:SIM_NG' AND state = 'ACTIVE'
  LIMIT 1
), closed AS (
  UPDATE config_versions c
  SET state = 'SUPERSEDED', effective_to = now()
  FROM cur WHERE c.config_version_id = cur.config_version_id
  RETURNING cur.version_no, cur.content
)
INSERT INTO config_versions
  (config_version_id, domain, scope, version_no, state, content, content_hash,
   effective_from, created_by, approved_by, reason)
SELECT
  'cfg_seed_adapter_cooldown_v' || (version_no + 1),
  'telco.adapter', 'telco:SIM_NG', version_no + 1, 'ACTIVE',
  content || '{"circuit_cooldown_seconds":30}'::jsonb,
  encode(sha256((content || '{"circuit_cooldown_seconds":30}'::jsonb)::text::bytea), 'hex'),
  now(), 'seed:builder', 'seed:reviewer',
  'R-P0-8b: adapter circuit breaker armed — cooldown 30s (open->half-open) added to the existing 50%/20-req policy. retry_budget stays 0 (INV-009).'
FROM closed;
