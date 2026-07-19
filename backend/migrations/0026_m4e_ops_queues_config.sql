-- 0026_m4e_ops_queues_config.sql — M4e-1 ambiguity-queue thresholds.
-- Config-first (TCP no-hardcoding rule): the SENT-staleness threshold and the
-- queue page bound are governed records with a validator
-- (validators_m4e.go), seeded active so the queues work out of the box. The
-- consumer refuses on absent/invalid config (C3 zero-config floor) — this
-- seed is the governed default, not a fallback in code.

INSERT INTO config_versions
  (config_version_id, domain, scope, version_no, state, content, content_hash,
   effective_from, created_by, approved_by, reason)
VALUES
  ('cfg_seed_ops_queues_v1', 'ops.queues', 'global', 1, 'ACTIVE',
   '{"stale_sent_after_seconds":600,"max_page_size":200}',
   encode(sha256('{"stale_sent_after_seconds":600,"max_page_size":200}'::bytea),'hex'),
   now(), 'seed:builder', 'seed:reviewer',
   'Seeded M4e default: a SENT attempt with no telco response after 10 minutes surfaces in the ops ambiguity queue (resolver enquiry cadence is advance.fulfilment); queue pages cap at 200');
