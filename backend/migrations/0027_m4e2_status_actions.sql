-- 0027_m4e2_status_actions.sql — M4e-2: the privileged subscriber-status
-- action (VR-35-F1 closure). Gives the "out-of-band privileged operation" a
-- real, audited, maker-checker existence — and re-grants the column-scoped
-- UPDATE(status) on subscriber_accounts that 0025 revoked, now that a
-- production writer exists. This is the 0025 discipline working as designed:
-- the grant lands WITH the code that writes the column and the tests that
-- prove it (VR-36 gate).

CREATE TABLE subscriber_status_actions (
  action_id             TEXT PRIMARY KEY,
  telco_id              TEXT NOT NULL,
  subscriber_account_id TEXT NOT NULL REFERENCES subscriber_accounts(subscriber_account_id),
  from_status           TEXT NOT NULL,
  to_status             TEXT NOT NULL,
  reason                TEXT NOT NULL,
  requested_by          TEXT NOT NULL,
  approved_by           TEXT,
  state                 TEXT NOT NULL DEFAULT 'REQUESTED'
                        CHECK (state IN ('REQUESTED','REJECTED','APPLIED')),
  requested_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
  decided_at            TIMESTAMPTZ,
  applied_at            TIMESTAMPTZ,
  -- The conduct floor is STRUCTURAL, not just config (C1): SELF_EXCLUDED can
  -- never be produced or overridden by an operator action — it belongs to the
  -- customer's own channel (EDG-030; task #45). The config validator refuses
  -- it too; this CHECK holds even against a bad config write.
  CHECK (from_status IN ('ACTIVE','BARRED','CLOSED')),
  CHECK (to_status   IN ('ACTIVE','BARRED','CLOSED')),
  CHECK (from_status <> to_status),
  -- maker-checker at the schema: decision requires a DISTINCT actor
  CHECK (state IN ('REQUESTED') OR (approved_by IS NOT NULL AND approved_by <> requested_by))
);

-- C5: one open action per subscriber — two concurrent requests cannot race to
-- approval (the guardrail_trips convergence pattern).
CREATE UNIQUE INDEX status_action_one_open_uq
  ON subscriber_status_actions (subscriber_account_id) WHERE state = 'REQUESTED';
CREATE INDEX status_actions_open_ix
  ON subscriber_status_actions (telco_id, requested_at) WHERE state = 'REQUESTED';

ALTER TABLE subscriber_status_actions ENABLE ROW LEVEL SECURITY;
CREATE POLICY t_subscriber_status_actions ON subscriber_status_actions
  USING (telco_id = current_setting('app.telco_id', true))
  WITH CHECK (telco_id = current_setting('app.telco_id', true));

-- Born column-scoped (EXT-3 discipline — never a broad UPDATE grant): the
-- code's only mutations are Decide (state, approved_by, decided_at) and the
-- apply stamp (applied_at). Identity, transition, reason, requester are
-- write-once.
GRANT SELECT, INSERT ON subscriber_status_actions TO tcp_app;
GRANT UPDATE (state, approved_by, decided_at, applied_at)
  ON subscriber_status_actions TO tcp_app;
GRANT SELECT ON subscriber_status_actions TO tcp_worker;

-- Trigger backstop (write_offs pattern): terminal rows frozen, identity
-- immutable, approver fixed once — even against the table owner.
CREATE OR REPLACE FUNCTION subscriber_status_action_immutable()
RETURNS trigger AS $$
BEGIN
  IF OLD.state IN ('REJECTED','APPLIED') THEN
    RAISE EXCEPTION 'subscriber_status_actions: a %-state action is immutable (M4e-2)', OLD.state;
  END IF;
  IF NEW.action_id             IS DISTINCT FROM OLD.action_id
  OR NEW.telco_id              IS DISTINCT FROM OLD.telco_id
  OR NEW.subscriber_account_id IS DISTINCT FROM OLD.subscriber_account_id
  OR NEW.from_status           IS DISTINCT FROM OLD.from_status
  OR NEW.to_status             IS DISTINCT FROM OLD.to_status
  OR NEW.reason                IS DISTINCT FROM OLD.reason
  OR NEW.requested_by          IS DISTINCT FROM OLD.requested_by THEN
    RAISE EXCEPTION 'subscriber_status_actions: identity/transition/requester are immutable (M4e-2)';
  END IF;
  IF OLD.approved_by IS NOT NULL AND NEW.approved_by IS DISTINCT FROM OLD.approved_by THEN
    RAISE EXCEPTION 'subscriber_status_actions: approved_by is immutable once set (M4e-2)';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER subscriber_status_action_immutable
  BEFORE UPDATE ON subscriber_status_actions
  FOR EACH ROW EXECUTE FUNCTION subscriber_status_action_immutable();

-- The designed re-grant (0025 close-out comment, VR-36): subscriber_accounts
-- gains exactly the column the new writer writes. Everything else stays
-- locked (msisdn_token, effective_from, telco linkage remain UPDATE-denied).
GRANT UPDATE (status) ON subscriber_accounts TO tcp_app;

-- Governed transition set (C3: consumer refuses absent/invalid config; C1:
-- the validator refuses SELF_EXCLUDED structurally — see validators_m4e.go).
INSERT INTO config_versions
  (config_version_id, domain, scope, version_no, state, content, content_hash,
   effective_from, created_by, approved_by, reason)
VALUES
  ('cfg_seed_ops_status_actions_v1', 'ops.status_actions', 'global', 1, 'ACTIVE',
   '{"allowed_transitions":[{"from":"ACTIVE","to":"BARRED"},{"from":"BARRED","to":"ACTIVE"},{"from":"ACTIVE","to":"CLOSED"},{"from":"BARRED","to":"CLOSED"}]}',
   encode(sha256('{"allowed_transitions":[{"from":"ACTIVE","to":"BARRED"},{"from":"BARRED","to":"ACTIVE"},{"from":"ACTIVE","to":"CLOSED"},{"from":"BARRED","to":"CLOSED"}]}'::bytea),'hex'),
   now(), 'seed:builder', 'seed:reviewer',
   'Seeded M4e-2 default: operators may bar/unbar and close (CLOSED is terminal). SELF_EXCLUDED is structurally excluded — customer-channel only (EDG-030, task #45)');
