-- 0037_rp06c_recon_period_watermark.sql — R-P0-6 Slice C (period/watermark).
-- The recon skeleton rescanned ALL history every run. Slice C bounds a run to a
-- window [watermark, now - lag): the watermark is the max period_end of the
-- prior ACTIVE runs for the scope, and a governed recon_lag_seconds keeps
-- still-settling records out of the current window. Distinct periods must
-- coexist as separate ACTIVE runs (each period keeps its own statement), so the
-- one-active-run uniqueness moves from (telco, programme, layer) to include
-- period_start — a re-reconcile of ONE period supersedes only that period.

-- One ACTIVE run per (telco, programme, layer, PERIOD_START). Widening the
-- prior Slice-A index (which allowed one ACTIVE per scope) is strictly more
-- permissive on existing single-period data, so it cannot fail on current rows.
DROP INDEX recon_runs_active_uq;
CREATE UNIQUE INDEX recon_runs_active_uq
  ON recon_runs (telco_id, programme_id, layer, period_start) WHERE state = 'ACTIVE';

-- Governed settling lag: records credited/activated within the last
-- recon_lag_seconds are still in flight and are excluded from the window (so a
-- run is a statement of SETTLED activity, not a race with in-progress money).
-- Data-driven supersede of the currently-active recon.tolerance (0010/0033/0035
-- pattern) so no version number is hardcoded.
WITH cur AS (
  SELECT config_version_id, version_no, content
  FROM config_versions
  WHERE domain = 'recon.tolerance' AND scope = 'programme:prg_sim_airtime01' AND state = 'ACTIVE'
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
  'cfg_seed_recon_tol_lag_v' || (version_no + 1),
  'recon.tolerance', 'programme:prg_sim_airtime01', version_no + 1, 'ACTIVE',
  content || '{"recon_lag_seconds":300}'::jsonb,
  encode(sha256((content || '{"recon_lag_seconds":300}'::jsonb)::text::bytea), 'hex'),
  now(), 'seed:builder', 'seed:reviewer',
  'R-P0-6 Slice C: settling lag 300s — the reconciliation window ends at now-lag so still-in-flight records are not reconciled prematurely.'
FROM closed;
