-- 0043_selfexclusion_operator_toggle.sql — reviewer follow-up to self-exclusion.
-- Reinstatement is subscriber self-service after the cool-off (the correct
-- default: the cool-off is the protection, and a customer has the right to
-- re-engage once the reflection period passes). But whether an OPERATOR must
-- approve lifting an exclusion is a jurisdictional policy question — so make it a
-- governed toggle (no hardcoding), not a code assumption. Default false =
-- self-service. The reinstatement path reads it and, while true, refuses
-- self-service (fail-closed) — the operator maker-checker path is a future build
-- gated by this flag, to be built if counsel/CBN require it (a config flip, not a
-- rebuild).
--
-- Data-driven supersede of the currently-active origination.self_exclusion
-- (0037/0039 recon pattern) — no hardcoded version number, prior fields merged.
WITH cur AS (
  SELECT config_version_id, version_no, content
  FROM config_versions
  WHERE domain = 'origination.self_exclusion' AND scope = 'programme:prg_sim_airtime01' AND state = 'ACTIVE'
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
  'cfg_seed_self_exclusion_optgate_v' || (version_no + 1),
  'origination.self_exclusion', 'programme:prg_sim_airtime01', version_no + 1, 'ACTIVE',
  content || '{"require_operator_reinstatement":false}'::jsonb,
  encode(sha256((content || '{"require_operator_reinstatement":false}'::jsonb)::text::bytea), 'hex'),
  now(), 'seed:builder', 'seed:reviewer',
  'Self-exclusion: require_operator_reinstatement=false (self-service after the cool-off is the default; operator sign-off is a governed flip if a jurisdiction requires it).'
FROM closed;
