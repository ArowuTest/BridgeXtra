-- 0011_m3_money_core.sql — M3a: money-core schema + governed domains.
-- Spine: V2 §14 recovery matrix (EDG-019/020/021), §15 delinquency/write-off
-- (overlay, never an FSM state — SRS D-2; write-off IS a state change),
-- §16 treasury guardrails, §17 settlement, V2-REC-008..012 breaks workflow,
-- V1-CUS complaints, V1-REG bureau staging (DORMANT — no transmitter exists).

-- ---------------------------------------------------------------------------
-- Advance FSM gains WRITTEN_OFF (controlled reversal workflows arrive at M3,
-- exactly as 0004 promised). One-active index is NOT widened: a written-off
-- advance no longer blocks new lending — the loss is crystallised.
-- ---------------------------------------------------------------------------
ALTER TABLE advances DROP CONSTRAINT advances_state_check;
ALTER TABLE advances ADD CONSTRAINT advances_state_check
  CHECK (state IN ('REQUESTED','VALIDATED','EXPOSURE_RESERVED','PENDING_FULFILMENT',
                   'FULFILMENT_UNKNOWN','ACTIVE','PARTIALLY_RECOVERED','CLOSED',
                   'FULFILMENT_FAILED','DECLINED','WRITTEN_OFF'));

-- Delinquency OVERLAY (never a state): bucket + as-of stamped by the daily
-- classification job from the governed aging ladder.
ALTER TABLE advances
  ADD COLUMN delinquency_bucket TEXT,
  ADD COLUMN bucket_as_of       TIMESTAMPTZ;

-- ---------------------------------------------------------------------------
-- EDG-019: reversal-before-original parking. A reversal referencing a source
-- event we have not seen yet is PARKED, never dropped, never applied blind.
-- ---------------------------------------------------------------------------
CREATE TABLE pending_reversals (
  pending_reversal_id      TEXT PRIMARY KEY,
  telco_id                 TEXT NOT NULL,
  original_source_event_id TEXT NOT NULL,   -- the event this reverses (unseen)
  reversal_source_event_id TEXT NOT NULL,
  amount_minor             BIGINT NOT NULL CHECK (amount_minor > 0),
  currency                 CHAR(3) NOT NULL,
  state                    TEXT NOT NULL DEFAULT 'PARKED'
                           CHECK (state IN ('PARKED','APPLIED','EXPIRED')),
  received_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
  applied_at               TIMESTAMPTZ,
  UNIQUE (telco_id, original_source_event_id),
  UNIQUE (telco_id, reversal_source_event_id)
);
CREATE INDEX pending_reversals_aging_ix ON pending_reversals (received_at)
  WHERE state = 'PARKED';

ALTER TABLE pending_reversals ENABLE ROW LEVEL SECURITY;
CREATE POLICY t_pending_reversals ON pending_reversals
  USING (telco_id = current_setting('app.telco_id', true))
  WITH CHECK (telco_id = current_setting('app.telco_id', true));
GRANT SELECT, INSERT, UPDATE ON pending_reversals TO tcp_app;
GRANT SELECT ON pending_reversals TO tcp_worker;

-- ---------------------------------------------------------------------------
-- Write-offs (V2 §15): maker-checker enforced AT THE SCHEMA (distinct
-- approver, like config_versions), one write-off per advance, full money
-- split snapshot for the ledger movement.
-- ---------------------------------------------------------------------------
CREATE TABLE write_offs (
  write_off_id    TEXT PRIMARY KEY,
  telco_id        TEXT NOT NULL,
  advance_id      TEXT NOT NULL REFERENCES advances(advance_id),
  principal_minor BIGINT NOT NULL CHECK (principal_minor >= 0),
  fee_minor       BIGINT NOT NULL CHECK (fee_minor >= 0),
  currency        CHAR(3) NOT NULL,
  reason          TEXT NOT NULL,
  requested_by    TEXT NOT NULL,
  approved_by     TEXT,
  state           TEXT NOT NULL DEFAULT 'REQUESTED'
                  CHECK (state IN ('REQUESTED','APPROVED','POSTED','REJECTED')),
  requested_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  decided_at      TIMESTAMPTZ,
  posted_at       TIMESTAMPTZ,
  UNIQUE (advance_id),
  CHECK (principal_minor + fee_minor > 0),
  -- maker-checker at the schema: approval requires a DISTINCT actor
  CHECK (state NOT IN ('APPROVED','POSTED') OR (approved_by IS NOT NULL AND approved_by <> requested_by))
);
CREATE INDEX write_offs_state_ix ON write_offs (telco_id, state, requested_at);

ALTER TABLE write_offs ENABLE ROW LEVEL SECURITY;
CREATE POLICY t_write_offs ON write_offs
  USING (telco_id = current_setting('app.telco_id', true))
  WITH CHECK (telco_id = current_setting('app.telco_id', true));
GRANT SELECT, INSERT, UPDATE ON write_offs TO tcp_app;
GRANT SELECT ON write_offs TO tcp_worker;

-- ---------------------------------------------------------------------------
-- Settlement (V2 §17): statements are DERIVED FROM THE LEDGER and stored
-- with a content hash so regeneration is provably bit-identical (EDG-027
-- class). Lines are append-only once the statement is FINAL.
-- ---------------------------------------------------------------------------
CREATE TABLE settlement_statements (
  statement_id   TEXT PRIMARY KEY,
  telco_id       TEXT NOT NULL,
  programme_id   TEXT NOT NULL REFERENCES programmes(programme_id),
  period_start   TIMESTAMPTZ NOT NULL,
  period_end     TIMESTAMPTZ NOT NULL,
  state          TEXT NOT NULL DEFAULT 'DRAFT' CHECK (state IN ('DRAFT','FINAL')),
  currency       CHAR(3) NOT NULL,
  content_hash   TEXT,                       -- set at FINAL; regeneration must match
  terms_version_id TEXT NOT NULL,            -- pinned settlement.terms config
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  finalised_at   TIMESTAMPTZ,
  CHECK (period_end > period_start),
  UNIQUE (telco_id, programme_id, period_start, period_end)
);

CREATE TABLE settlement_lines (
  line_id       TEXT PRIMARY KEY,
  statement_id  TEXT NOT NULL REFERENCES settlement_statements(statement_id),
  telco_id      TEXT NOT NULL,
  line_code     TEXT NOT NULL,               -- e.g. PRINCIPAL_DISBURSED, FEE_INCOME, TELCO_SHARE, TAX_VAT
  amount_minor  BIGINT NOT NULL,
  currency      CHAR(3) NOT NULL,
  detail        JSONB NOT NULL DEFAULT '{}',
  UNIQUE (statement_id, line_code)
);

ALTER TABLE settlement_statements ENABLE ROW LEVEL SECURITY;
CREATE POLICY t_settlement_statements ON settlement_statements
  USING (telco_id = current_setting('app.telco_id', true))
  WITH CHECK (telco_id = current_setting('app.telco_id', true));
ALTER TABLE settlement_lines ENABLE ROW LEVEL SECURITY;
CREATE POLICY t_settlement_lines ON settlement_lines
  USING (telco_id = current_setting('app.telco_id', true))
  WITH CHECK (telco_id = current_setting('app.telco_id', true));
GRANT SELECT, INSERT, UPDATE ON settlement_statements TO tcp_app;
GRANT SELECT, INSERT ON settlement_lines TO tcp_app;    -- lines never updated
GRANT SELECT ON settlement_statements, settlement_lines TO tcp_worker;

-- ---------------------------------------------------------------------------
-- Breaks workflow (V2-REC-008..012): recon_items gain a lifecycle; every
-- action is an append-only log row (who, what, why) — breaks are resolved
-- with reasons, never edited away.
-- ---------------------------------------------------------------------------
ALTER TABLE recon_items
  ADD COLUMN assigned_to TEXT,
  ADD COLUMN resolved_at TIMESTAMPTZ,
  ADD COLUMN resolution  TEXT;

CREATE TABLE recon_break_actions (
  action_id     TEXT PRIMARY KEY,
  telco_id      TEXT NOT NULL,
  recon_item_id TEXT NOT NULL REFERENCES recon_items(recon_item_id),
  action        TEXT NOT NULL CHECK (action IN ('ASSIGN','RESOLVE','ESCALATE','NOTE')),
  actor         TEXT NOT NULL,
  reason        TEXT NOT NULL,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX recon_break_actions_item_ix ON recon_break_actions (recon_item_id, created_at);

ALTER TABLE recon_break_actions ENABLE ROW LEVEL SECURITY;
CREATE POLICY t_recon_break_actions ON recon_break_actions
  USING (telco_id = current_setting('app.telco_id', true))
  WITH CHECK (telco_id = current_setting('app.telco_id', true));
GRANT SELECT, INSERT ON recon_break_actions TO tcp_app;  -- append-only
GRANT SELECT ON recon_break_actions TO tcp_worker;

-- ---------------------------------------------------------------------------
-- Complaints register (V1-CUS): PII-lean (tokenised subscriber reference,
-- category + narrative), full lifecycle, resolution always recorded.
-- ---------------------------------------------------------------------------
CREATE TABLE complaints (
  complaint_id  TEXT PRIMARY KEY,
  telco_id      TEXT NOT NULL,
  subscriber_account_id TEXT REFERENCES subscriber_accounts(subscriber_account_id),
  advance_id    TEXT REFERENCES advances(advance_id),
  channel       TEXT NOT NULL,
  category      TEXT NOT NULL CHECK (category IN
                ('DISPUTED_ADVANCE','DISPUTED_RECOVERY','DISCLOSURE','SERVICE','OTHER')),
  narrative     TEXT NOT NULL,
  state         TEXT NOT NULL DEFAULT 'OPEN'
                CHECK (state IN ('OPEN','IN_REVIEW','RESOLVED','REJECTED')),
  resolution    TEXT,
  opened_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  resolved_at   TIMESTAMPTZ,
  CHECK (state NOT IN ('RESOLVED','REJECTED') OR resolution IS NOT NULL)
);
CREATE INDEX complaints_state_ix ON complaints (telco_id, state, opened_at);

ALTER TABLE complaints ENABLE ROW LEVEL SECURITY;
CREATE POLICY t_complaints ON complaints
  USING (telco_id = current_setting('app.telco_id', true))
  WITH CHECK (telco_id = current_setting('app.telco_id', true));
GRANT SELECT, INSERT, UPDATE ON complaints TO tcp_app;
GRANT SELECT ON complaints TO tcp_worker;

-- ---------------------------------------------------------------------------
-- Bureau export staging (V1-REG): the pipeline PRODUCES batches to staging;
-- nothing transmits (DORMANT until licensing arms it — no stub, a real
-- producer with the sender deliberately absent).
-- ---------------------------------------------------------------------------
CREATE TABLE bureau_export_batches (
  batch_id     TEXT PRIMARY KEY,
  telco_id     TEXT NOT NULL,
  period_start TIMESTAMPTZ NOT NULL,
  period_end   TIMESTAMPTZ NOT NULL,
  row_count    INT NOT NULL DEFAULT 0 CHECK (row_count >= 0),
  file_hash    TEXT,
  state        TEXT NOT NULL DEFAULT 'STAGED' CHECK (state IN ('STAGED')),  -- SENT arrives with licensing
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (telco_id, period_start, period_end)
);

ALTER TABLE bureau_export_batches ENABLE ROW LEVEL SECURITY;
CREATE POLICY t_bureau_batches ON bureau_export_batches
  USING (telco_id = current_setting('app.telco_id', true))
  WITH CHECK (telco_id = current_setting('app.telco_id', true));
GRANT SELECT, INSERT, UPDATE ON bureau_export_batches TO tcp_app;
GRANT SELECT ON bureau_export_batches TO tcp_worker;

-- ---------------------------------------------------------------------------
-- Seeded M3 governed domains (V1 no-hardcoding; conservative defaults).
-- Validators in configsvc/validators_m3.go — every value the code cannot
-- honor is rejected at approval; safety controls carry zero-config floors.
-- ---------------------------------------------------------------------------
INSERT INTO config_versions
  (config_version_id, domain, scope, version_no, state, content, content_hash,
   effective_from, created_by, approved_by, reason)
VALUES
  ('cfg_seed_delinq_buckets_v1', 'delinquency.buckets', 'programme:prg_sim_airtime01', 1, 'ACTIVE',
   '{"buckets":[{"code":"CURRENT","min_days_past_due":0},{"code":"DPD_1_7","min_days_past_due":1},{"code":"DPD_8_30","min_days_past_due":8},{"code":"DPD_31_60","min_days_past_due":31},{"code":"DPD_61_90","min_days_past_due":61},{"code":"DPD_90_PLUS","min_days_past_due":91}],"grace_days":0}',
   encode(sha256('{"buckets":[{"code":"CURRENT","min_days_past_due":0},{"code":"DPD_1_7","min_days_past_due":1},{"code":"DPD_8_30","min_days_past_due":8},{"code":"DPD_31_60","min_days_past_due":31},{"code":"DPD_61_90","min_days_past_due":61},{"code":"DPD_90_PLUS","min_days_past_due":91}],"grace_days":0}'::bytea),'hex'),
   now(), 'seed:builder', 'seed:reviewer',
   'Seeded delinquency aging ladder (V2 §15): CURRENT + five DPD buckets, zero grace — overlay classification, never an FSM state (SRS D-2)'),

  ('cfg_seed_writeoff_policy_v1', 'writeoff.policy', 'programme:prg_sim_airtime01', 1, 'ACTIVE',
   '{"min_bucket":"DPD_90_PLUS","require_distinct_approver":true,"post_writeoff_recovery":"RECOVERY_INCOME"}',
   encode(sha256('{"min_bucket":"DPD_90_PLUS","require_distinct_approver":true,"post_writeoff_recovery":"RECOVERY_INCOME"}'::bytea),'hex'),
   now(), 'seed:builder', 'seed:reviewer',
   'Seeded write-off policy: eligible from DPD_90_PLUS, maker-checker structurally required (validator rejects false), post-write-off recoveries to recovery income (EDG-021)'),

  ('cfg_seed_treasury_guardrails_v1', 'treasury.guardrails', 'programme:prg_sim_airtime01', 1, 'ACTIVE',
   '{"max_daily_disbursed_minor":50000000,"max_open_exposure_bps_of_committed":8000,"trip_action":"SUSPEND_PROGRAMME","rearm":"MAKER_CHECKER"}',
   encode(sha256('{"max_daily_disbursed_minor":50000000,"max_open_exposure_bps_of_committed":8000,"trip_action":"SUSPEND_PROGRAMME","rearm":"MAKER_CHECKER"}'::bytea),'hex'),
   now(), 'seed:builder', 'seed:reviewer',
   'Seeded treasury guardrails (V1-TRE): NGN 500k/day disbursement cap, 80% open-exposure-of-committed cap; trip = programme auto-suspend (fail closed); re-arm = maker-checker only (not configurable off)'),

  ('cfg_seed_settlement_terms_v1', 'settlement.terms', 'programme:prg_sim_airtime01', 1, 'ACTIVE',
   '{"cycle":"MONTHLY","telco_share_bps":2500,"platform_share_bps":7500,"taxes":[{"code":"VAT","bps":750}],"tolerance_minor":0}',
   encode(sha256('{"cycle":"MONTHLY","telco_share_bps":2500,"platform_share_bps":7500,"taxes":[{"code":"VAT","bps":750}],"tolerance_minor":0}'::bytea),'hex'),
   now(), 'seed:builder', 'seed:reviewer',
   'Seeded settlement terms (V2 §17): monthly cycle, 25/75 fee-income share (Optasia-model economics inverted in our favour is a COMMERCIAL decision — this is a demo seed), VAT 7.5%, zero tolerance');
