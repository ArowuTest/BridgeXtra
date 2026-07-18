-- 0014_m3d_guardrails.sql — M3d treasury guardrails (V1-TRE, EDG-024/025).
--
-- A trip is EVIDENCE with a lifecycle: TRIPPED (programme auto-suspended,
-- fail closed) -> REARM_REQUESTED (maker) -> REARMED (checker, distinct —
-- schema-enforced like every other two-person decision in this system).
-- One un-rearmed trip per (programme, guardrail): concurrent detection of
-- the same breach converges on a single record.
-- The seeded demo programme was left in CERTIFICATION — latent inconsistency
-- the new fail-closed status gate surfaced: worker jobs that filter on
-- ACTIVE (recon, delinquency) were already silently skipping it, and now
-- offers/confirms would refuse too. A walking-skeleton demo programme is a
-- LIVE programme; certification workflows for real telcos arrive at M5.
UPDATE programmes SET status = 'ACTIVE'
WHERE programme_id = 'prg_sim_airtime01' AND status IN ('DRAFT','CERTIFICATION');

CREATE TABLE guardrail_trips (
  trip_id            TEXT PRIMARY KEY,
  telco_id           TEXT NOT NULL,
  programme_id       TEXT NOT NULL REFERENCES programmes(programme_id),
  guardrail          TEXT NOT NULL CHECK (guardrail IN ('DAILY_DISBURSED','OPEN_EXPOSURE')),
  measured_minor     BIGINT NOT NULL,
  limit_minor        BIGINT NOT NULL,
  currency           CHAR(3) NOT NULL,
  state              TEXT NOT NULL DEFAULT 'TRIPPED'
                     CHECK (state IN ('TRIPPED','REARM_REQUESTED','REARMED')),
  tripped_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  rearm_requested_by TEXT,
  rearm_approved_by  TEXT,
  rearmed_at         TIMESTAMPTZ,
  -- two-person re-arm, structurally: approval requires a DISTINCT actor
  CHECK (state <> 'REARMED' OR (rearm_requested_by IS NOT NULL
         AND rearm_approved_by IS NOT NULL AND rearm_approved_by <> rearm_requested_by))
);
CREATE UNIQUE INDEX guardrail_open_trip_uq
  ON guardrail_trips (programme_id, guardrail) WHERE state <> 'REARMED';
CREATE INDEX guardrail_trips_state_ix ON guardrail_trips (telco_id, state, tripped_at);

ALTER TABLE guardrail_trips ENABLE ROW LEVEL SECURITY;
CREATE POLICY t_guardrail_trips ON guardrail_trips
  USING (telco_id = current_setting('app.telco_id', true))
  WITH CHECK (telco_id = current_setting('app.telco_id', true));
GRANT SELECT, INSERT, UPDATE ON guardrail_trips TO tcp_app;
GRANT SELECT ON guardrail_trips TO tcp_worker;
