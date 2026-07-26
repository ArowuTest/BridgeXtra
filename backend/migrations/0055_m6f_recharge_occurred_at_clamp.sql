-- 0055_m6f_recharge_occurred_at_clamp.sql — Phase 1 S3-C: add the occurred_at
-- clamp knobs to telco.recharge_feed so a webhook event's business time cannot be
-- back-dated beyond the RECOVERY re-sweep window (nor implausibly future-dated).
-- validateRechargeFeed now REQUIRES both fields, so the existing GLOBAL seed
-- (0051, which lacks them) must be superseded to add them — otherwise a future
-- re-activation of the global default would fail validation, and the webhook's
-- feedCfg would read 0/0 and reject every event (backdate 0 = must equal now).
--
-- Data-driven supersede (no hardcoded version_no): close the current ACTIVE row
-- and insert version_no+1 with the two knobs merged in. Seeded generous:
--   recovery_max_backdate_seconds   = 1209600 (14d, matching the recon re-sweep lookback)
--   recovery_max_future_skew_seconds = 60

WITH cur AS (
  SELECT config_version_id, version_no, content
  FROM config_versions
  WHERE domain='telco.recharge_feed' AND scope='global' AND state='ACTIVE'
),
closed AS (
  UPDATE config_versions c
  SET state='SUPERSEDED', effective_to=now()
  FROM cur WHERE c.config_version_id = cur.config_version_id
  RETURNING cur.version_no AS vno, cur.content AS content
)
INSERT INTO config_versions
  (config_version_id, domain, scope, version_no, state, content, content_hash,
   effective_from, created_by, approved_by, reason)
SELECT 'cfg_seed_recharge_feed_global_v'||(closed.vno+1),
       'telco.recharge_feed', 'global', closed.vno+1, 'ACTIVE',
       (closed.content || '{"recovery_max_backdate_seconds":1209600,"recovery_max_future_skew_seconds":60}'::jsonb),
       encode(sha256((closed.content || '{"recovery_max_backdate_seconds":1209600,"recovery_max_future_skew_seconds":60}'::jsonb)::text::bytea), 'hex'),
       now(), 'seed:builder', 'seed:reviewer',
       'S3-C: add occurred_at clamp (14d backdate ceiling matching the recon re-sweep lookback, 60s future skew) to the global telco.recharge_feed default.'
FROM closed;
