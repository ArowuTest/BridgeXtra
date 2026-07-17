-- 0007_m2_decision_core.sql — M2 credit core: feature store, scoring runs,
-- decision extensions, overlay flags, consent evidence, notification evidence.
-- Requirements: V2-SCR-001..018 (§11), §11.2 canonical decision result,
-- V2-REG-001 (consent evidence), EDG-013/014, V1-CRD-010 (bit-exact replay).
-- Controls ship WITH features (V3-DLV-004): quality flags, dedup, hashes and
-- validators land in the same migration as the tables they guard.

-- ---------------------------------------------------------------------------
-- Feature files: one row per ingested batch file. Dedup is file-level by
-- content hash — re-ingesting the same file is a recorded no-op, never a
-- double-write (V2-SCR-001 pipeline idempotency).
-- ---------------------------------------------------------------------------
CREATE TABLE feature_files (
  feature_file_id TEXT PRIMARY KEY,
  telco_id        TEXT NOT NULL REFERENCES telcos(telco_id),
  source          TEXT NOT NULL,            -- e.g. 'sim:/sim/feature-file'
  as_of           TIMESTAMPTZ NOT NULL,     -- the telco's data cut time
  content_hash    TEXT NOT NULL,
  row_count       INT  NOT NULL CHECK (row_count >= 0),
  quarantined_rows INT NOT NULL DEFAULT 0 CHECK (quarantined_rows >= 0),
  status          TEXT NOT NULL DEFAULT 'INGESTED'
                  CHECK (status IN ('INGESTED','QUARANTINED')),
  received_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (telco_id, content_hash)
);
CREATE INDEX feature_files_asof_ix ON feature_files (telco_id, as_of DESC);

ALTER TABLE feature_files ENABLE ROW LEVEL SECURITY;
CREATE POLICY t_feature_files ON feature_files
  USING (telco_id = current_setting('app.telco_id', true))
  WITH CHECK (telco_id = current_setting('app.telco_id', true));
GRANT SELECT, INSERT, UPDATE ON feature_files TO tcp_app;
GRANT SELECT ON feature_files TO tcp_worker;

-- ---------------------------------------------------------------------------
-- Feature snapshots: per subscriber per as-of cut. Every snapshot records its
-- coverage/missingness/quality flags (V2-SCR-002) — the scoring engine reads
-- flags, it never guesses. features carries integer minor units / counts /
-- bps only: floats are banned from the money-and-scoring perimeter (BC-1).
-- ---------------------------------------------------------------------------
CREATE TABLE feature_snapshots (
  feature_snapshot_id  TEXT PRIMARY KEY,
  telco_id             TEXT NOT NULL,
  subscriber_account_id TEXT NOT NULL REFERENCES subscriber_accounts(subscriber_account_id),
  feature_file_id      TEXT NOT NULL REFERENCES feature_files(feature_file_id),
  as_of                TIMESTAMPTZ NOT NULL,
  features             JSONB NOT NULL,      -- canonical feature map (integers)
  quality              JSONB NOT NULL,      -- {coverage_days, missing:[...], flags:[...]}
  content_hash         TEXT  NOT NULL,      -- replay input pin (BC-4)
  created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (subscriber_account_id, as_of)
);
-- Scoring reads the latest snapshot per subscriber: covered by the unique
-- (subscriber_account_id, as_of) scanned backwards.

ALTER TABLE feature_snapshots ENABLE ROW LEVEL SECURITY;
CREATE POLICY t_feature_snapshots ON feature_snapshots
  USING (telco_id = current_setting('app.telco_id', true))
  WITH CHECK (telco_id = current_setting('app.telco_id', true));
GRANT SELECT, INSERT ON feature_snapshots TO tcp_app;
GRANT SELECT ON feature_snapshots TO tcp_worker;

-- ---------------------------------------------------------------------------
-- Scoring runs: a run is the unit of batch decisioning — pinned to the exact
-- scoring.policy config version, with control totals so a partial run is
-- visible, never silent (V2-SCR-018 monitoring hooks read these).
-- ---------------------------------------------------------------------------
CREATE TABLE scoring_runs (
  scoring_run_id   TEXT PRIMARY KEY,
  telco_id         TEXT NOT NULL,
  programme_id     TEXT NOT NULL REFERENCES programmes(programme_id),
  feature_file_id  TEXT NOT NULL REFERENCES feature_files(feature_file_id),
  policy_version_id TEXT NOT NULL,          -- pinned scoring.policy config
  status           TEXT NOT NULL DEFAULT 'RUNNING'
                   CHECK (status IN ('RUNNING','COMPLETED','FAILED')),
  subjects_total   INT NOT NULL DEFAULT 0 CHECK (subjects_total >= 0),
  subjects_scored  INT NOT NULL DEFAULT 0 CHECK (subjects_scored >= 0),
  subjects_skipped INT NOT NULL DEFAULT 0 CHECK (subjects_skipped >= 0),
  started_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  completed_at     TIMESTAMPTZ,
  -- one completed run per (file, policy version, programme): re-running the
  -- same inputs is a replay, not a new run
  UNIQUE (feature_file_id, policy_version_id, programme_id)
);

ALTER TABLE scoring_runs ENABLE ROW LEVEL SECURITY;
CREATE POLICY t_scoring_runs ON scoring_runs
  USING (telco_id = current_setting('app.telco_id', true))
  WITH CHECK (telco_id = current_setting('app.telco_id', true));
GRANT SELECT, INSERT, UPDATE ON scoring_runs TO tcp_app;
GRANT SELECT ON scoring_runs TO tcp_worker;

-- ---------------------------------------------------------------------------
-- Decision snapshot extensions (§11.2 canonical result + BC-4 replay pin).
-- Existing rows (M1 seeds) get explicit SEED markers, not fake provenance.
-- ---------------------------------------------------------------------------
ALTER TABLE decision_snapshots
  ADD COLUMN tier_code           TEXT NOT NULL DEFAULT 'SEED',
  ADD COLUMN reason_codes        JSONB NOT NULL DEFAULT '["SEEDED_DECISION"]',
  ADD COLUMN permitted_denominations JSONB,   -- minor-unit array; NULL = product ladder
  ADD COLUMN feature_snapshot_id TEXT REFERENCES feature_snapshots(feature_snapshot_id),
  ADD COLUMN scoring_run_id      TEXT REFERENCES scoring_runs(scoring_run_id),
  ADD COLUMN valid_until         TIMESTAMPTZ,  -- NULL = no expiry (seed only)
  ADD COLUMN decision_hash       TEXT;         -- bit-exact replay target (BC-4)

-- Scored decisions must carry full provenance; only seeds may omit it.
ALTER TABLE decision_snapshots ADD CONSTRAINT decision_provenance_ck CHECK (
  tier_code = 'SEED'
  OR (feature_snapshot_id IS NOT NULL AND scoring_run_id IS NOT NULL
      AND valid_until IS NOT NULL AND decision_hash IS NOT NULL)
);

-- ---------------------------------------------------------------------------
-- Subscriber overlay flags (V2-SCR-008/015): real-time risk states with
-- effective ranges. A flag row is evidence — it is closed (effective_to),
-- never deleted.
-- ---------------------------------------------------------------------------
CREATE TABLE subscriber_flags (
  flag_id        TEXT PRIMARY KEY,
  telco_id       TEXT NOT NULL,
  subscriber_account_id TEXT NOT NULL REFERENCES subscriber_accounts(subscriber_account_id),
  flag           TEXT NOT NULL CHECK (flag IN
                 ('SIM_SWAP','BARRED','SELF_EXCLUDED','FRAUD_SUSPECT','DECEASED')),
  source         TEXT NOT NULL,             -- event/system that raised it
  effective_from TIMESTAMPTZ NOT NULL DEFAULT now(),
  effective_to   TIMESTAMPTZ,
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- One OPEN row per (subscriber, flag): raise is idempotent at the DB.
CREATE UNIQUE INDEX subscriber_flags_open_uq
  ON subscriber_flags (subscriber_account_id, flag) WHERE effective_to IS NULL;
CREATE INDEX subscriber_flags_sub_ix ON subscriber_flags (subscriber_account_id)
  WHERE effective_to IS NULL;

ALTER TABLE subscriber_flags ENABLE ROW LEVEL SECURITY;
CREATE POLICY t_subscriber_flags ON subscriber_flags
  USING (telco_id = current_setting('app.telco_id', true))
  WITH CHECK (telco_id = current_setting('app.telco_id', true));
GRANT SELECT, INSERT, UPDATE ON subscriber_flags TO tcp_app;
GRANT SELECT ON subscriber_flags TO tcp_worker;

-- ---------------------------------------------------------------------------
-- Consent / disclosure evidence (V2-REG-001): written IN the confirm
-- transaction; UNIQUE(advance_id) — an advance cannot exist twice-consented,
-- and M2e makes writing this record a structural part of confirm.
-- Append-only by grants, like the ledger: evidence is never edited.
-- ---------------------------------------------------------------------------
CREATE TABLE consents (
  consent_id     TEXT PRIMARY KEY,
  telco_id       TEXT NOT NULL,
  advance_id     TEXT NOT NULL REFERENCES advances(advance_id),
  subscriber_account_id TEXT NOT NULL REFERENCES subscriber_accounts(subscriber_account_id),
  disclosed_terms JSONB NOT NULL,           -- exact terms shown (fees, repayment, model)
  content_hash   TEXT NOT NULL,             -- hash of disclosed_terms
  channel        TEXT NOT NULL,             -- e.g. 'USSD'
  captured_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (advance_id)
);

ALTER TABLE consents ENABLE ROW LEVEL SECURITY;
CREATE POLICY t_consents ON consents
  USING (telco_id = current_setting('app.telco_id', true))
  WITH CHECK (telco_id = current_setting('app.telco_id', true));
GRANT SELECT, INSERT ON consents TO tcp_app;   -- no UPDATE/DELETE: append-only
GRANT SELECT ON consents TO tcp_worker;

-- ---------------------------------------------------------------------------
-- Notification evidence (V2 §10.2 notifications row): what was sent, from
-- which template version, with the rendered-content hash. Delivery is
-- best-effort; EVIDENCE of the attempt is not.
-- ---------------------------------------------------------------------------
CREATE TABLE notifications (
  notification_id TEXT PRIMARY KEY,
  telco_id        TEXT NOT NULL,
  subscriber_account_id TEXT NOT NULL REFERENCES subscriber_accounts(subscriber_account_id),
  advance_id      TEXT REFERENCES advances(advance_id),
  kind            TEXT NOT NULL,            -- e.g. 'ADVANCE_CONFIRMED','RECOVERY_APPLIED'
  template_version TEXT NOT NULL,           -- pinned notify.templates config version
  rendered_hash   TEXT NOT NULL,
  state           TEXT NOT NULL DEFAULT 'PENDING'
                  CHECK (state IN ('PENDING','SENT','FAILED')),
  provider_ref    TEXT,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  sent_at         TIMESTAMPTZ,
  -- idempotency: one notification per (advance, kind) — replays return it
  UNIQUE (advance_id, kind)
);

ALTER TABLE notifications ENABLE ROW LEVEL SECURITY;
CREATE POLICY t_notifications ON notifications
  USING (telco_id = current_setting('app.telco_id', true))
  WITH CHECK (telco_id = current_setting('app.telco_id', true));
GRANT SELECT, INSERT, UPDATE ON notifications TO tcp_app;
GRANT SELECT, UPDATE (state, provider_ref, sent_at) ON notifications TO tcp_worker;

-- ---------------------------------------------------------------------------
-- Seeded M2 config domains (V1 no-hardcoding; conservative defaults).
-- Validators in configsvc (validators_m2.go) guard every domain — a domain
-- without a validator is a review finding.
-- ---------------------------------------------------------------------------
INSERT INTO config_versions
  (config_version_id, domain, scope, version_no, state, content, content_hash,
   effective_from, created_by, approved_by, reason)
VALUES
  ('cfg_seed_scoring_policy_v1', 'scoring.policy', 'programme:prg_sim_airtime01', 1, 'ACTIVE',
   '{"gates":{"min_tenure_days":90,"blocked_statuses":["BARRED","SELF_EXCLUDED","CLOSED"],"require_activity_days":30},"staleness":{"accept_hours":48,"degrade_hours":168,"degrade_tier_cap":"TIER_01"},"missing_policy":"REJECT","anti_gaming":{"window_days":90,"winsor_upper_bps":9500,"spike_ratio_max_bps":30000,"min_active_days":10},"tiers":[{"code":"TIER_01","max_face_minor":5000,"min_recharge_90d_minor":30000},{"code":"TIER_02","max_face_minor":10000,"min_recharge_90d_minor":90000},{"code":"TIER_03","max_face_minor":20000,"min_recharge_90d_minor":200000},{"code":"TIER_04","max_face_minor":50000,"min_recharge_90d_minor":500000}],"starter_tier":"TIER_01","one_tier_up_max":1,"decision_valid_hours":168}',
   encode(sha256('{"gates":{"min_tenure_days":90,"blocked_statuses":["BARRED","SELF_EXCLUDED","CLOSED"],"require_activity_days":30},"staleness":{"accept_hours":48,"degrade_hours":168,"degrade_tier_cap":"TIER_01"},"missing_policy":"REJECT","anti_gaming":{"window_days":90,"winsor_upper_bps":9500,"spike_ratio_max_bps":30000,"min_active_days":10},"tiers":[{"code":"TIER_01","max_face_minor":5000,"min_recharge_90d_minor":30000},{"code":"TIER_02","max_face_minor":10000,"min_recharge_90d_minor":90000},{"code":"TIER_03","max_face_minor":20000,"min_recharge_90d_minor":200000},{"code":"TIER_04","max_face_minor":50000,"min_recharge_90d_minor":500000}],"starter_tier":"TIER_01","one_tier_up_max":1,"decision_valid_hours":168}'::bytea),'hex'),
   now(), 'seed:builder', 'seed:reviewer',
   'Seeded scoring policy (V2-SCR-003/004/007/009/016/017): winsorised 90d recharge, spike-capped, one-tier-up, starter TIER_01, stale accept 48h/degrade 7d, missing features REJECT'),

  ('cfg_seed_overlays_v1', 'overlays.policy', 'telco:SIM_NG', 1, 'ACTIVE',
   '{"blocking_flags":["SIM_SWAP","BARRED","SELF_EXCLUDED","FRAUD_SUSPECT","DECEASED"],"sim_swap_cooloff_hours":72,"check_at":["OFFER","CONFIRM"]}',
   encode(sha256('{"blocking_flags":["SIM_SWAP","BARRED","SELF_EXCLUDED","FRAUD_SUSPECT","DECEASED"],"sim_swap_cooloff_hours":72,"check_at":["OFFER","CONFIRM"]}'::bytea),'hex'),
   now(), 'seed:builder', 'seed:reviewer',
   'Seeded overlays (V2-SCR-008/015): every risk flag blocks at both offer and confirm; SIM-swap cool-off 72h. Fail-closed: unknown flag names are rejected by the validator'),

  ('cfg_seed_notify_templates_v1', 'notify.templates', 'telco:SIM_NG', 1, 'ACTIVE',
   '{"sender_id":"BridgeXtra","quiet_hours":{"start":"21:00","end":"07:00"},"templates":{"ADVANCE_CONFIRMED":{"version":"v1","body":"Your advance of {{face}} is active. Repayment {{repayment}} will be recovered from recharges. Fee {{fee}}."},"RECOVERY_APPLIED":{"version":"v1","body":"Recovery of {{amount}} applied. Outstanding {{outstanding}}."}}}',
   encode(sha256('{"sender_id":"BridgeXtra","quiet_hours":{"start":"21:00","end":"07:00"},"templates":{"ADVANCE_CONFIRMED":{"version":"v1","body":"Your advance of {{face}} is active. Repayment {{repayment}} will be recovered from recharges. Fee {{fee}}."},"RECOVERY_APPLIED":{"version":"v1","body":"Recovery of {{amount}} applied. Outstanding {{outstanding}}."}}}'::bytea),'hex'),
   now(), 'seed:builder', 'seed:reviewer',
   'Seeded notification templates (V2 §10.2): sender + quiet hours + advance/recovery templates; consent/DND controls prevail over delivery');
