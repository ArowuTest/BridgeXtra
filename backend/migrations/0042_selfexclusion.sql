-- 0042_selfexclusion.sql — Self-exclusion (R1-MUST, pre-pilot). A responsible-
-- lending control: a subscriber can opt OUT of being offered/receiving airtime
-- credit. subscriber_accounts.status already carries 'SELF_EXCLUDED' and the
-- origination gate already refuses any non-ACTIVE subscriber, so the enforcement
-- chokepoint exists. What is missing — and what makes self-exclusion a real
-- control rather than a toggle a distressed borrower flips back instantly — is a
-- register with a governed COOL-OFF: the exclusion cannot be reinstated before a
-- minimum period. This register is the authoritative lifecycle; the subscriber
-- status is a mirror kept in sync in the same tx.

CREATE TABLE self_exclusions (
  exclusion_id          TEXT PRIMARY KEY,
  telco_id              TEXT NOT NULL,
  subscriber_account_id TEXT NOT NULL REFERENCES subscriber_accounts(subscriber_account_id),
  state                 TEXT NOT NULL DEFAULT 'ACTIVE' CHECK (state IN ('ACTIVE','REINSTATED')),
  channel               TEXT NOT NULL,
  reason                TEXT,
  requested_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
  -- Cool-off: the earliest a reinstatement may take effect. Set from the governed
  -- origination.self_exclusion.min_exclusion_days at request time.
  min_until             TIMESTAMPTZ NOT NULL,
  reinstated_at         TIMESTAMPTZ,
  reinstated_channel    TEXT,
  -- A reinstated exclusion must record when; min_until never precedes the request.
  CHECK (state <> 'REINSTATED' OR reinstated_at IS NOT NULL),
  CHECK (min_until >= requested_at)
);

-- At most ONE active self-exclusion per subscriber (fail-closed: a second request
-- while excluded converges on the existing one, never a duplicate).
CREATE UNIQUE INDEX self_exclusions_active_uq
  ON self_exclusions (subscriber_account_id) WHERE state = 'ACTIVE';
CREATE INDEX self_exclusions_lookup_ix
  ON self_exclusions (telco_id, subscriber_account_id, state);

ALTER TABLE self_exclusions ENABLE ROW LEVEL SECURITY;
CREATE POLICY t_self_exclusions ON self_exclusions
  USING (telco_id = current_setting('app.telco_id', true))
  WITH CHECK (telco_id = current_setting('app.telco_id', true));
-- Append-only register; the only mutation is the reinstatement flip. Column-scoped
-- UPDATE (FSM-grant-lockdown discipline).
GRANT SELECT, INSERT ON self_exclusions TO tcp_app;
GRANT UPDATE (state, reinstated_at, reinstated_channel) ON self_exclusions TO tcp_app;
GRANT SELECT ON self_exclusions TO tcp_worker;

-- Governed terms (no hardcoding): the cool-off minimum and the channels through
-- which a subscriber may self-exclude. Seeded with a defensible responsible-
-- lending default (30-day cool-off); admin-configurable via the maker-checker
-- config path. programme-scoped like the other lending-policy domains.
INSERT INTO config_versions
  (config_version_id, domain, scope, version_no, state, content, content_hash,
   effective_from, created_by, approved_by, reason)
VALUES
  ('cfg_seed_self_exclusion_v1', 'origination.self_exclusion', 'programme:prg_sim_airtime01', 1, 'ACTIVE',
   '{"min_exclusion_days":30,"allowed_channels":["USSD","APP","SMS"]}',
   encode(sha256('{"min_exclusion_days":30,"allowed_channels":["USSD","APP","SMS"]}'::bytea),'hex'),
   now(), 'seed:builder', 'seed:reviewer',
   'Self-exclusion terms: 30-day cool-off before reinstatement (responsible-lending default), self-exclude via USSD/APP/SMS.');
