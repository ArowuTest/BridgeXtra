-- 0069 — Feed-Health monitor (Wave B.4): add the recharge silence-alarm threshold to the
-- GLOBAL telco.recharge_feed default. The recharge webhook governs per-EVENT freshness
-- (replay_window_seconds) but has no ABSENCE threshold — nothing says "no recharge in X
-- seconds = alarm". silence_alarm_seconds is that governed knob (validated 300..86400 in
-- validators_recharge.go, OPTIONAL so configs predating it stay valid and the monitor's
-- zero-config floor degrades to a raw timestamp rather than a false "never silent").
--
-- Data-driven supersede (no hardcoded version_no; mirrors 0055): close the current ACTIVE
-- global row and insert version_no+1 with the key merged. Seeded 3600 (1h) — a recharge feed
-- silent for an hour means recovery is stalling; the admin can retune per telco.

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
       (closed.content || '{"silence_alarm_seconds":3600}'::jsonb),
       encode(sha256((closed.content || '{"silence_alarm_seconds":3600}'::jsonb)::text::bytea), 'hex'),
       now(), 'seed:builder', 'seed:reviewer',
       'Feed-Health B4: add recharge silence_alarm_seconds=3600 to the global telco.recharge_feed default (the feed-silence alarm threshold; per-event freshness already governed by replay_window_seconds).'
FROM closed;
