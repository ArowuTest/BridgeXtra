-- 0004_m1_credit_core.sql — M1 walking skeleton credit core (BUILD_PLAN §3).
-- Money columns are BIGINT minor units + CHAR(3) currency; the repo layer is
-- the ONLY place they map to/from entity.Money (ADR-0002).

-- ---------------------------------------------------------------------------
-- Subscriber accounts (V2-SUB-001, BUILD_PLAN §3): an MSISDN↔telco
-- relationship for an effective identity period — never an eternal identity.
-- One LIVE identity period per (telco, token); porting/recycling close the
-- period (effective_to) and open a new account (EDG-016/017).
-- ---------------------------------------------------------------------------
CREATE TABLE subscriber_accounts (
  subscriber_account_id TEXT PRIMARY KEY,
  telco_id       TEXT NOT NULL REFERENCES telcos(telco_id),
  msisdn_token   TEXT NOT NULL,
  status         TEXT NOT NULL DEFAULT 'ACTIVE'
                 CHECK (status IN ('ACTIVE','BARRED','SELF_EXCLUDED','CLOSED')),
  effective_from TIMESTAMPTZ NOT NULL DEFAULT now(),
  effective_to   TIMESTAMPTZ,
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX subscriber_live_identity_uq
  ON subscriber_accounts (telco_id, msisdn_token) WHERE effective_to IS NULL;
CREATE INDEX subscriber_status_ix ON subscriber_accounts (telco_id, status);

ALTER TABLE subscriber_accounts ENABLE ROW LEVEL SECURITY;
CREATE POLICY t_subscribers ON subscriber_accounts
  USING (telco_id = current_setting('app.telco_id', true))
  WITH CHECK (telco_id = current_setting('app.telco_id', true));
GRANT SELECT, INSERT, UPDATE ON subscriber_accounts TO tcp_app;
GRANT SELECT ON subscriber_accounts TO tcp_worker;

-- ---------------------------------------------------------------------------
-- Funding pools (V2-TRE-001/002): the CHECK makes over-allocation impossible
-- even under application bugs; reservation is a single conditional UPDATE.
-- ---------------------------------------------------------------------------
CREATE TABLE funding_pools (
  pool_id         TEXT PRIMARY KEY,
  programme_id    TEXT NOT NULL REFERENCES programmes(programme_id),
  telco_id        TEXT NOT NULL,
  currency        CHAR(3) NOT NULL,
  committed_minor BIGINT NOT NULL CHECK (committed_minor >= 0),
  reserved_minor  BIGINT NOT NULL DEFAULT 0 CHECK (reserved_minor >= 0),
  utilised_minor  BIGINT NOT NULL DEFAULT 0 CHECK (utilised_minor >= 0),
  status          TEXT NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE','SUSPENDED')),
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT funding_no_overallocation CHECK (reserved_minor + utilised_minor <= committed_minor)
);
CREATE INDEX funding_pools_programme_ix ON funding_pools (programme_id, status);

-- ---------------------------------------------------------------------------
-- Decision snapshots (M1: seeded static decision; the M2 scoring engine
-- becomes the writer of this same table — real table, not a stub).
-- ---------------------------------------------------------------------------
CREATE TABLE decision_snapshots (
  decision_snapshot_id TEXT PRIMARY KEY,
  telco_id             TEXT NOT NULL,
  subscriber_account_id TEXT NOT NULL REFERENCES subscriber_accounts(subscriber_account_id),
  max_face_value_minor BIGINT NOT NULL CHECK (max_face_value_minor > 0),
  currency             CHAR(3) NOT NULL,
  is_current           BOOLEAN NOT NULL DEFAULT true,
  config_version_id    TEXT NOT NULL,   -- pinned decision inputs (V1-CFG-007)
  created_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- Hot-path point read (V2-TAR-004): one current decision per subscriber.
CREATE UNIQUE INDEX decision_current_uq
  ON decision_snapshots (subscriber_account_id) WHERE is_current;

-- ---------------------------------------------------------------------------
-- Offers (V2-OFR-001/002): separate entity from advance; immutable snapshot.
-- ---------------------------------------------------------------------------
CREATE TABLE offers (
  offer_id            TEXT PRIMARY KEY,
  telco_id            TEXT NOT NULL,
  programme_id        TEXT NOT NULL REFERENCES programmes(programme_id),
  subscriber_account_id TEXT NOT NULL REFERENCES subscriber_accounts(subscriber_account_id),
  decision_snapshot_id TEXT NOT NULL REFERENCES decision_snapshots(decision_snapshot_id),
  face_value_minor    BIGINT NOT NULL CHECK (face_value_minor > 0),
  fee_minor           BIGINT NOT NULL CHECK (fee_minor >= 0),
  disbursed_minor     BIGINT NOT NULL CHECK (disbursed_minor > 0),
  repayment_minor     BIGINT NOT NULL CHECK (repayment_minor > 0),
  currency            CHAR(3) NOT NULL,
  fee_model           TEXT NOT NULL CHECK (fee_model IN ('DEDUCTED_UPFRONT','ADDED_TO_REPAYMENT')),
  product_config_version_id TEXT NOT NULL, -- pinned terms (V2-OFR-002)
  state               TEXT NOT NULL DEFAULT 'GENERATED'
                      CHECK (state IN ('GENERATED','ACCEPTED','EXPIRED','WITHDRAWN','SUPERSEDED')),
  expires_at          TIMESTAMPTZ NOT NULL,
  created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
  -- Money algebra pinned at snapshot time; never recomputed after acceptance.
  CONSTRAINT offer_money_identity CHECK (face_value_minor = disbursed_minor + fee_minor OR fee_model <> 'DEDUCTED_UPFRONT')
);
CREATE INDEX offers_active_ix ON offers (subscriber_account_id, state, expires_at);

-- ---------------------------------------------------------------------------
-- Advances (BUILD_PLAN §3): DB-arbitered idempotency + one-active backstop.
-- ---------------------------------------------------------------------------
CREATE TABLE advances (
  advance_id          TEXT PRIMARY KEY,
  telco_id            TEXT NOT NULL,
  programme_id        TEXT NOT NULL REFERENCES programmes(programme_id),
  subscriber_account_id TEXT NOT NULL REFERENCES subscriber_accounts(subscriber_account_id),
  offer_id            TEXT NOT NULL REFERENCES offers(offer_id),
  funding_pool_id     TEXT NOT NULL REFERENCES funding_pools(pool_id),
  idempotency_key     TEXT NOT NULL,
  correlation_id      TEXT NOT NULL,      -- BC-6: customer tap -> journal lineage
  state               TEXT NOT NULL
                      CHECK (state IN ('REQUESTED','VALIDATED','EXPOSURE_RESERVED','PENDING_FULFILMENT',
                                       'FULFILMENT_UNKNOWN','ACTIVE','PARTIALLY_RECOVERED','CLOSED',
                                       'FULFILMENT_FAILED','DECLINED')),
  version             INT NOT NULL DEFAULT 1,  -- optimistic FSM lock (V2-ADV-007)
  face_value_minor    BIGINT NOT NULL CHECK (face_value_minor > 0),
  fee_minor           BIGINT NOT NULL CHECK (fee_minor >= 0),
  disbursed_minor     BIGINT NOT NULL CHECK (disbursed_minor > 0),
  outstanding_minor   BIGINT NOT NULL CHECK (outstanding_minor >= 0), -- INV-006 floor
  currency            CHAR(3) NOT NULL,
  accepted_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  activated_at        TIMESTAMPTZ,
  closed_at           TIMESTAMPTZ,
  updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (telco_id, idempotency_key)                    -- V2-ADV-004
);
-- THE one-active-advance schema backstop (V1-PRD-005, EDG-002). Its NAME is
-- load-bearing: the SF-2 config validator introspects pg_indexes for it.
CREATE UNIQUE INDEX advances_one_active_uq ON advances (subscriber_account_id)
  WHERE state IN ('REQUESTED','VALIDATED','EXPOSURE_RESERVED','PENDING_FULFILMENT',
                  'FULFILMENT_UNKNOWN','ACTIVE','PARTIALLY_RECOVERED');
CREATE INDEX advances_state_ix ON advances (telco_id, state, updated_at);   -- V3-AFO-001 queues
CREATE INDEX advances_subscriber_ix ON advances (subscriber_account_id, accepted_at DESC);

-- ---------------------------------------------------------------------------
-- Fulfilment attempts (V2-TEL-002 evidence; V2-ADV-005 reference chain).
-- ---------------------------------------------------------------------------
CREATE TABLE fulfilment_attempts (
  attempt_id          TEXT PRIMARY KEY,
  advance_id          TEXT NOT NULL REFERENCES advances(advance_id),
  attempt_no          INT NOT NULL,
  telco_idempotency_key TEXT NOT NULL,
  state               TEXT NOT NULL CHECK (state IN ('SENT','CONFIRMED','FAILED','UNKNOWN')),
  telco_reference     TEXT,
  request_evidence    JSONB NOT NULL,
  response_evidence   JSONB,
  submitted_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  next_enquiry_at     TIMESTAMPTZ,
  enquiry_count       INT NOT NULL DEFAULT 0,
  resolved_at         TIMESTAMPTZ,
  UNIQUE (advance_id, attempt_no),
  UNIQUE (telco_idempotency_key)
);
-- The FULFILMENT_UNKNOWN worker scan is index-only (BUILD_PLAN §3).
CREATE INDEX fulfilment_unknown_ix ON fulfilment_attempts (next_enquiry_at)
  WHERE state = 'UNKNOWN';

-- ---------------------------------------------------------------------------
-- Recovery events + allocations (V2-COL-001..006; EDG-018/019/020).
-- ---------------------------------------------------------------------------
CREATE TABLE recovery_events (
  recovery_event_id   TEXT PRIMARY KEY,
  telco_id            TEXT NOT NULL,
  source_event_id     TEXT NOT NULL,
  subscriber_account_id TEXT REFERENCES subscriber_accounts(subscriber_account_id),
  amount_minor        BIGINT NOT NULL CHECK (amount_minor > 0),
  currency            CHAR(3) NOT NULL,
  state               TEXT NOT NULL DEFAULT 'PENDING'
                      CHECK (state IN ('PENDING','ALLOCATED','QUARANTINED','UNMATCHED')),
  occurred_at         TIMESTAMPTZ NOT NULL,
  received_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (telco_id, source_event_id)                    -- EDG-018 dedup at DB
);
CREATE INDEX recovery_pending_ix ON recovery_events (received_at) WHERE state = 'PENDING';

CREATE TABLE recovery_allocations (
  allocation_id     TEXT PRIMARY KEY,
  recovery_event_id TEXT NOT NULL REFERENCES recovery_events(recovery_event_id),
  advance_id        TEXT NOT NULL REFERENCES advances(advance_id),
  component         TEXT NOT NULL CHECK (component IN ('FEE','PRINCIPAL')),
  amount_minor      BIGINT NOT NULL CHECK (amount_minor > 0),
  currency          CHAR(3) NOT NULL,
  created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX recovery_alloc_advance_ix ON recovery_allocations (advance_id);
CREATE INDEX recovery_alloc_event_ix ON recovery_allocations (recovery_event_id);

-- Over-recovery quarantine (EDG-020: never silently retained).
CREATE TABLE suspense_items (
  suspense_id       TEXT PRIMARY KEY,
  telco_id          TEXT NOT NULL,
  recovery_event_id TEXT NOT NULL REFERENCES recovery_events(recovery_event_id),
  amount_minor      BIGINT NOT NULL CHECK (amount_minor > 0),
  currency          CHAR(3) NOT NULL,
  reason            TEXT NOT NULL,
  state             TEXT NOT NULL DEFAULT 'OPEN' CHECK (state IN ('OPEN','RESOLVED')),
  created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX suspense_open_ix ON suspense_items (created_at) WHERE state = 'OPEN';

-- ---------------------------------------------------------------------------
-- Ledger (V2-LED-001..015): append-only double entry. Grants below give the
-- app role INSERT+SELECT ONLY — no UPDATE/DELETE exists for any runtime role.
-- Sole-writer discipline: internal/ledger is the only package that posts
-- (V2-SRV-002); a dedicated ledger DB role arrives when the ledger becomes a
-- separate service (post-M3) — same-transaction atomicity with the saga
-- requires one role today (V2-COL-005), recorded in ASSUMPTIONS A-15.
-- ---------------------------------------------------------------------------
CREATE TABLE journals (
  journal_id        TEXT PRIMARY KEY,
  business_event_key TEXT NOT NULL,
  event_type        TEXT NOT NULL,
  telco_id          TEXT NOT NULL,
  programme_id      TEXT NOT NULL,
  advance_id        TEXT,
  correlation_id    TEXT NOT NULL,          -- BC-6
  accounting_date   DATE NOT NULL DEFAULT CURRENT_DATE,
  posted_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (business_event_key, event_type)   -- INV-003 posting idempotency
);
CREATE INDEX journals_advance_ix ON journals (advance_id);
CREATE INDEX journals_period_ix ON journals (accounting_date, posted_at);

CREATE TABLE journal_entries (
  entry_id     TEXT PRIMARY KEY,
  journal_id   TEXT NOT NULL REFERENCES journals(journal_id),
  account_code TEXT NOT NULL,
  debit_minor  BIGINT NOT NULL DEFAULT 0 CHECK (debit_minor >= 0),
  credit_minor BIGINT NOT NULL DEFAULT 0 CHECK (credit_minor >= 0),
  currency     CHAR(3) NOT NULL,
  CONSTRAINT entry_single_side CHECK ((debit_minor = 0) <> (credit_minor = 0))
);
CREATE INDEX journal_entries_account_ix ON journal_entries (account_code, journal_id);

-- Reconciliation items (M1: platform vs simulator/telco records).
CREATE TABLE recon_items (
  recon_item_id  TEXT PRIMARY KEY,
  run_id         TEXT NOT NULL,
  telco_id       TEXT NOT NULL,
  item_type      TEXT NOT NULL CHECK (item_type IN ('FULFILMENT','RECOVERY')),
  platform_ref   TEXT,
  telco_ref      TEXT,
  status         TEXT NOT NULL CHECK (status IN ('MATCHED','BREAK_MISSING_PLATFORM','BREAK_MISSING_TELCO','BREAK_AMOUNT_MISMATCH')),
  detail         JSONB NOT NULL DEFAULT '{}',
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX recon_breaks_ix ON recon_items (run_id, status);

-- ---------------------------------------------------------------------------
-- RLS on tenant tables (fail-closed, as 0001).
-- ---------------------------------------------------------------------------
ALTER TABLE funding_pools       ENABLE ROW LEVEL SECURITY;
ALTER TABLE decision_snapshots  ENABLE ROW LEVEL SECURITY;
ALTER TABLE offers              ENABLE ROW LEVEL SECURITY;
ALTER TABLE advances            ENABLE ROW LEVEL SECURITY;
ALTER TABLE fulfilment_attempts ENABLE ROW LEVEL SECURITY;
ALTER TABLE recovery_events     ENABLE ROW LEVEL SECURITY;
ALTER TABLE recovery_allocations ENABLE ROW LEVEL SECURITY;
ALTER TABLE suspense_items      ENABLE ROW LEVEL SECURITY;
ALTER TABLE journals            ENABLE ROW LEVEL SECURITY;
ALTER TABLE journal_entries     ENABLE ROW LEVEL SECURITY;
ALTER TABLE recon_items         ENABLE ROW LEVEL SECURITY;

CREATE POLICY t_funding ON funding_pools      USING (telco_id = current_setting('app.telco_id', true)) WITH CHECK (telco_id = current_setting('app.telco_id', true));
CREATE POLICY t_decisions ON decision_snapshots USING (telco_id = current_setting('app.telco_id', true)) WITH CHECK (telco_id = current_setting('app.telco_id', true));
CREATE POLICY t_offers ON offers              USING (telco_id = current_setting('app.telco_id', true)) WITH CHECK (telco_id = current_setting('app.telco_id', true));
CREATE POLICY t_advances ON advances          USING (telco_id = current_setting('app.telco_id', true)) WITH CHECK (telco_id = current_setting('app.telco_id', true));
CREATE POLICY t_attempts ON fulfilment_attempts
  USING (EXISTS (SELECT 1 FROM advances a WHERE a.advance_id = fulfilment_attempts.advance_id))
  WITH CHECK (EXISTS (SELECT 1 FROM advances a WHERE a.advance_id = fulfilment_attempts.advance_id));
CREATE POLICY t_recovery ON recovery_events   USING (telco_id = current_setting('app.telco_id', true)) WITH CHECK (telco_id = current_setting('app.telco_id', true));
CREATE POLICY t_alloc ON recovery_allocations
  USING (EXISTS (SELECT 1 FROM advances a WHERE a.advance_id = recovery_allocations.advance_id))
  WITH CHECK (EXISTS (SELECT 1 FROM advances a WHERE a.advance_id = recovery_allocations.advance_id));
CREATE POLICY t_suspense ON suspense_items    USING (telco_id = current_setting('app.telco_id', true)) WITH CHECK (telco_id = current_setting('app.telco_id', true));
CREATE POLICY t_journals ON journals          USING (telco_id = current_setting('app.telco_id', true)) WITH CHECK (telco_id = current_setting('app.telco_id', true));
CREATE POLICY t_entries ON journal_entries
  USING (EXISTS (SELECT 1 FROM journals j WHERE j.journal_id = journal_entries.journal_id))
  WITH CHECK (EXISTS (SELECT 1 FROM journals j WHERE j.journal_id = journal_entries.journal_id));
CREATE POLICY t_recon ON recon_items          USING (telco_id = current_setting('app.telco_id', true)) WITH CHECK (telco_id = current_setting('app.telco_id', true));

-- Grants: append-only money trail — journals/entries/allocations get NO
-- UPDATE/DELETE for any runtime role (V2-LED-003/DAT-003).
GRANT SELECT, INSERT, UPDATE ON funding_pools, advances, fulfilment_attempts, recovery_events, offers TO tcp_app;
GRANT SELECT, INSERT ON decision_snapshots, recovery_allocations, suspense_items, journals, journal_entries, recon_items TO tcp_app;
GRANT UPDATE (state) ON suspense_items TO tcp_app;
GRANT UPDATE (is_current) ON decision_snapshots TO tcp_app;
GRANT SELECT ON funding_pools, decision_snapshots, offers, advances, fulfilment_attempts,
               recovery_events, recovery_allocations, suspense_items, journals, journal_entries, recon_items TO tcp_worker;
GRANT INSERT ON recon_items TO tcp_worker;
GRANT UPDATE (state, version, outstanding_minor, activated_at, closed_at, updated_at) ON advances TO tcp_worker;
GRANT UPDATE (state, telco_reference, response_evidence, next_enquiry_at, enquiry_count, resolved_at) ON fulfilment_attempts TO tcp_worker;
GRANT UPDATE (reserved_minor, utilised_minor) ON funding_pools TO tcp_worker;
GRANT INSERT ON journals, journal_entries TO tcp_worker;

-- ---------------------------------------------------------------------------
-- Chart of accounts config + walking-skeleton seeds.
-- ---------------------------------------------------------------------------
INSERT INTO config_versions
  (config_version_id, domain, scope, version_no, state, content, content_hash,
   effective_from, created_by, approved_by, reason)
VALUES
  ('cfg_seed_ledger_accounts_v1', 'ledger.accounts', 'global', 1, 'ACTIVE',
   '{"accounts":[{"code":"SUBSCRIBER_RECEIVABLE","kind":"ASSET"},{"code":"FEE_INCOME","kind":"INCOME"},{"code":"AIRTIME_FUNDING_CLEARING","kind":"LIABILITY"},{"code":"TELCO_SETTLEMENT_RECEIVABLE","kind":"ASSET"},{"code":"RECOVERY_SUSPENSE","kind":"LIABILITY"}]}',
   encode(sha256('{"accounts":[{"code":"SUBSCRIBER_RECEIVABLE","kind":"ASSET"},{"code":"FEE_INCOME","kind":"INCOME"},{"code":"AIRTIME_FUNDING_CLEARING","kind":"LIABILITY"},{"code":"TELCO_SETTLEMENT_RECEIVABLE","kind":"ASSET"},{"code":"RECOVERY_SUSPENSE","kind":"LIABILITY"}]}'::bytea),'hex'),
   now(), 'seed:builder', 'seed:reviewer',
   'Seeded M1 chart of accounts (DD-18 accounting point: recognition at confirmed fulfilment; full config posting-template engine lands M3)');

-- Walking-skeleton fixtures: subscriber, decision, funding pool. These are
-- REAL rows the E2E path uses; M2''s scorer becomes the writer of decisions.
INSERT INTO subscriber_accounts (subscriber_account_id, telco_id, msisdn_token, status)
VALUES ('sub_sim_0001', 'SIM_NG', 'tok_sim_0001', 'ACTIVE');

INSERT INTO decision_snapshots
  (decision_snapshot_id, telco_id, subscriber_account_id, max_face_value_minor, currency, config_version_id)
VALUES ('dec_sim_0001', 'SIM_NG', 'sub_sim_0001', 50000, 'NGN', 'cfg_seed_product_airtime_v1');

INSERT INTO funding_pools (pool_id, programme_id, telco_id, currency, committed_minor)
VALUES ('pool_sim_01', 'prg_sim_airtime01', 'SIM_NG', 'NGN', 100000000);
