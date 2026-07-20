-- 0039_rp06d2_recon_rereconcile_lookback.sql — R-P0-6 Slice D2 (VR-50-F1 /
-- REC-006). The incremental RunFulfilment advances the watermark past a window
-- based on the telco records present AT RUN TIME. A telco credit that lands
-- AFTER its window was reconciled is never picked up by future incremental runs
-- (those start at the advanced watermark), so it is stranded forever as a
-- BREAK_MISSING_TELCO — a real completeness gap. Slice D2 adds a scheduled
-- re-reconcile of recent settled windows; this governs how far back that sweep
-- reaches. A late file arriving within rereconcile_lookback_seconds of its
-- window's end is recovered; a value of 0 would leave late arrivals stranded
-- (armed-but-dead), so the validator requires it positive.
--
-- Data-driven supersede of the currently-active recon.tolerance (0035/0037
-- pattern) so no version number is hardcoded and every prior field is preserved.
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
  'cfg_seed_recon_tol_rerecon_v' || (version_no + 1),
  'recon.tolerance', 'programme:prg_sim_airtime01', version_no + 1, 'ACTIVE',
  content || '{"rereconcile_lookback_seconds":604800}'::jsonb,
  encode(sha256((content || '{"rereconcile_lookback_seconds":604800}'::jsonb)::text::bytea), 'hex'),
  now(), 'seed:builder', 'seed:reviewer',
  'R-P0-6 Slice D2: re-reconcile lookback 604800s (7d) — the scheduled re-reconcile re-sweeps settled windows that ended within this horizon so a late-arriving telco credit is recovered instead of stranded as a missing-telco break (VR-50-F1).'
FROM closed;
