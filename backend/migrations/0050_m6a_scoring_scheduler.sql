-- 0050_m6a_scoring_scheduler.sql — Phase 0: arm the scoring pipeline.
--
-- A durable, config-driven scheduler (cmd/worker) runs featureingest -> scoringrun
-- per active telco/programme so fresh decisions are always on file for offers.
-- This migration lays the DORMANT foundation: the per-(programme,cycle) claim
-- table and the two config domains. No behaviour changes until the scheduler
-- service and its worker loop land (later slices).
--
-- Correctness design (hardened by an adversarial design pass — see the scheduler
-- service for the algorithm):
--   * The claim is a LEASE claim-or-reclaim, not a fire-once lock: a crash between
--     claiming a cycle and finishing it must not remove that cycle from the whole
--     fleet. A FAILED cycle, or a CLAIMED cycle whose lease has expired, is
--     re-claimable by any healthy instance; a SUCCEEDED / STALE_NO_REFRESH cycle
--     never is. UNIQUE(telco_id, programme_id, cycle_key) is the arbiter.
--   * cycle_key is computed from the DATABASE clock on the effective-cadence grid
--     (see the service), so instances with skewed wall clocks still agree on the
--     bucket — no double-run near a boundary.
--   * feature_file_id is bound to the cycle so a reclaim REUSES the same file and
--     scoringrun's per-(file,policy,programme) idempotency replays instead of
--     minting a second run.
--   * The one-current-decision-per-subscriber invariant is already enforced
--     structurally by decision_current_uq (0004) — it is the fail-closed backstop
--     against a second concurrent run, so no new index is needed here.

-- ---------------------------------------------------------------------------
-- Per-(programme, cycle) claim ledger (tenant-scoped, RLS).
-- ---------------------------------------------------------------------------
CREATE TABLE scoring_schedule_cycles (
  cycle_id                TEXT PRIMARY KEY,
  telco_id                TEXT NOT NULL REFERENCES telcos(telco_id),
  programme_id            TEXT NOT NULL,
  cycle_key               TIMESTAMPTZ NOT NULL,            -- effective-cadence bucket start (DB clock)
  status                  TEXT NOT NULL DEFAULT 'CLAIMED'
                            CHECK (status IN ('CLAIMED','SUCCEEDED','FAILED','STALE_NO_REFRESH')),
  claimed_by              TEXT NOT NULL,                    -- worker instance id (host+pid+uuid)
  claimed_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
  attempts                INT NOT NULL DEFAULT 1 CHECK (attempts >= 1),
  effective_cadence_hours INT NOT NULL CHECK (effective_cadence_hours >= 1),
  feature_file_id         TEXT,                             -- bound after ingest, reused on reclaim
  scoring_run_id          TEXT,
  subjects_scored         INT,
  finished_at             TIMESTAMPTZ,
  error                   TEXT,
  UNIQUE (telco_id, programme_id, cycle_key)
);
-- Newest-cycle-first browse per programme (ops visibility / freshness gauge).
CREATE INDEX scoring_schedule_cycles_prog_ix
  ON scoring_schedule_cycles (telco_id, programme_id, cycle_key DESC);

ALTER TABLE scoring_schedule_cycles ENABLE ROW LEVEL SECURITY;
CREATE POLICY t_scoring_schedule_cycles ON scoring_schedule_cycles
  USING (telco_id = current_setting('app.telco_id', true))
  WITH CHECK (telco_id = current_setting('app.telco_id', true));
-- The scheduler claims/updates cycles as tcp_app under WithTenant (RLS-enforced).
GRANT SELECT, INSERT, UPDATE ON scoring_schedule_cycles TO tcp_app;
-- The worker/owner reads cycle status cross-tenant for monitoring.
GRANT SELECT ON scoring_schedule_cycles TO tcp_worker;

-- ---------------------------------------------------------------------------
-- Config domain scoring.schedule (new). Every knob is a validated admin-config
-- with a seeded default; the seed is DISABLED so arming is an explicit act
-- (zero-config floor is OFF — nothing lends until an operator flips enabled).
--   cadence_hours    : nominal re-score period.
--   headroom_cycles  : spare cadences of freshness margin; effective cadence is
--                      clamped to <= decision_valid_hours/(headroom_cycles+1) so a
--                      missed/crashed cycle can never open a NO_OFFER gap.
--   lease_seconds    : a CLAIMED cycle is re-claimable only after this; MUST
--                      exceed the longest scoring run so a slow live run is never
--                      reclaimed under it.
--   max_attempts     : reclaim ceiling before a cycle is parked FAILED + alerted.
-- ---------------------------------------------------------------------------
WITH t AS (
  SELECT '{"enabled":false,"cadence_hours":24,"headroom_cycles":1,"lease_seconds":900,"max_attempts":6}'::text AS c
)
INSERT INTO config_versions
  (config_version_id, domain, scope, version_no, state, content, content_hash,
   effective_from, created_by, approved_by, reason)
SELECT 'cfg_seed_scoring_schedule_global_v1', 'scoring.schedule', 'global', 1, 'ACTIVE',
       t.c::jsonb, encode(sha256(t.c::bytea), 'hex'),
       now(), 'seed:builder', 'seed:reviewer',
       'Seeded scoring.schedule global default DISABLED (zero-config floor OFF): 24h cadence, 1 cadence of freshness headroom, 900s lease, 6 reclaim attempts. Arming requires an explicit enabled:true per programme/telco.'
FROM t;

-- ---------------------------------------------------------------------------
-- scoring.policy: add a GLOBAL default so scope fallback (programme -> global)
-- always resolves. Without it, a NEW programme with no override would fail the
-- fail-closed policy read every cycle and silently never score. Content mirrors
-- the active programme:prg_sim_airtime01 seed (v2), incl. decision_valid_hours=168.
-- ---------------------------------------------------------------------------
WITH t AS (
  SELECT '{"gates":{"min_tenure_days":90,"blocked_statuses":["BARRED","SELF_EXCLUDED","CLOSED"],"require_activity_days":30},"staleness":{"accept_hours":48,"degrade_hours":168,"degrade_tier_cap":"TIER_01"},"missing_policy":"REJECT","anti_gaming":{"window_days":90,"winsor_upper_bps":9200,"spike_ratio_max_bps":30000,"min_active_days":10,"spike_action":"FLAG_ONLY"},"tiers":[{"code":"TIER_01","max_face_minor":5000,"min_recharge_90d_minor":30000},{"code":"TIER_02","max_face_minor":10000,"min_recharge_90d_minor":90000},{"code":"TIER_03","max_face_minor":20000,"min_recharge_90d_minor":200000},{"code":"TIER_04","max_face_minor":50000,"min_recharge_90d_minor":500000}],"starter_tier":"TIER_01","one_tier_up_max":1,"decision_valid_hours":168}'::text AS c
)
INSERT INTO config_versions
  (config_version_id, domain, scope, version_no, state, content, content_hash,
   effective_from, created_by, approved_by, reason)
SELECT 'cfg_seed_scoring_policy_global_v1', 'scoring.policy', 'global', 1, 'ACTIVE',
       t.c::jsonb, encode(sha256(t.c::bytea), 'hex'),
       now(), 'seed:builder', 'seed:reviewer',
       'Seeded scoring.policy GLOBAL default so programme->global scope fallback resolves for programmes without an override (mirrors the prg_sim_airtime01 v2 policy; decision_valid_hours=168).'
FROM t;
