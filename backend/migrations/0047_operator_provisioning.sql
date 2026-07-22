-- 0047_operator_provisioning.sql — governed operator provisioning (v1).
-- Turns the out-of-band `seed-operators` stopgap into a real, audited runtime
-- surface. v1 scope (reviewer-decided): CREATE (four-eyes / maker-checker) and
-- REVOKE (single-actor) only — NO in-place role/scope change and NO escalation
-- classifier (those, with an operators identity model, are post-pen-test v2).
--
-- STRUCTURAL write-once (the core security property): the app role is granted
-- exactly INSERT + UPDATE(status) on admin_credentials and NOTHING on role/scope,
-- so a privilege change is physically impossible without revoke-and-recreate.
-- `actor` stays UNIQUE/stable (an operator identity is permanent; a revoked actor
-- is retired, never reused). The M4A-F1 kill-switch (Resolve re-checks status
-- ='ACTIVE' every request) means revoke ends live sessions immediately.

-- The maker-checker record for CREATE. Platform-scope (operators are not telco
-- data), so no RLS telco policy — access is gated by the ADMIN-only API and the
-- column grants below. Same two-actor discipline as subscriber_status_actions.
CREATE TABLE operator_create_requests (
  request_id    TEXT PRIMARY KEY,
  actor         TEXT NOT NULL,   -- the proposed new operator's stable identity
  role          TEXT NOT NULL CHECK (role IN ('ADMIN','RISK','FINANCE','OPS','SUPPORT')),
  scope         TEXT NOT NULL
                CHECK (scope = '*' OR scope ~ '^(global|programme:[A-Za-z0-9_]+|telco:[A-Za-z0-9_]+)$'),
  reason        TEXT NOT NULL,
  requested_by  TEXT NOT NULL,
  approved_by   TEXT,
  state         TEXT NOT NULL DEFAULT 'REQUESTED'
                CHECK (state IN ('REQUESTED','REJECTED','APPLIED')),
  requested_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  decided_at    TIMESTAMPTZ,
  applied_at    TIMESTAMPTZ,
  -- maker-checker at the schema: a decision needs a DISTINCT approver.
  CHECK (state IN ('REQUESTED') OR (approved_by IS NOT NULL AND approved_by <> requested_by))
);

-- One open create-request per proposed actor — two admins cannot race two
-- proposals for the same identity to approval (guardrail_trips convergence).
CREATE UNIQUE INDEX operator_create_one_open_uq
  ON operator_create_requests (actor) WHERE state = 'REQUESTED';
CREATE INDEX operator_create_open_ix
  ON operator_create_requests (requested_at) WHERE state = 'REQUESTED';

-- Column-scoped from birth (EXT-3 discipline): the code's only mutation is
-- Decide (state, approved_by, decided_at, applied_at). Identity, role, scope,
-- reason and requester are write-once.
GRANT SELECT, INSERT ON operator_create_requests TO tcp_app;
GRANT UPDATE (state, approved_by, decided_at, applied_at)
  ON operator_create_requests TO tcp_app;
GRANT SELECT ON operator_create_requests TO tcp_worker;

-- Trigger backstop (subscriber_status_actions pattern): terminal rows frozen,
-- identity/role/scope/requester immutable, approver fixed once — even against
-- the table owner.
CREATE OR REPLACE FUNCTION operator_create_request_immutable()
RETURNS trigger AS $$
BEGIN
  IF OLD.state IN ('REJECTED','APPLIED') THEN
    RAISE EXCEPTION 'operator_create_requests: a %-state request is immutable', OLD.state;
  END IF;
  IF NEW.request_id   IS DISTINCT FROM OLD.request_id
  OR NEW.actor        IS DISTINCT FROM OLD.actor
  OR NEW.role         IS DISTINCT FROM OLD.role
  OR NEW.scope        IS DISTINCT FROM OLD.scope
  OR NEW.reason       IS DISTINCT FROM OLD.reason
  OR NEW.requested_by IS DISTINCT FROM OLD.requested_by THEN
    RAISE EXCEPTION 'operator_create_requests: identity/role/scope/requester are immutable';
  END IF;
  IF OLD.approved_by IS NOT NULL AND NEW.approved_by IS DISTINCT FROM OLD.approved_by THEN
    RAISE EXCEPTION 'operator_create_requests: approved_by is immutable once set';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER operator_create_request_immutable
  BEFORE UPDATE ON operator_create_requests
  FOR EACH ROW EXECUTE FUNCTION operator_create_request_immutable();

-- The designed grant lands WITH the writer (0025/0027 discipline): the app role
-- gains exactly INSERT (create on approval) and UPDATE(status) (revoke). It is
-- deliberately NOT granted UPDATE on role or scope — write-once is enforced by
-- the DATABASE, not by convention. A role/scope change is therefore impossible
-- except by revoke-and-recreate, which fires the kill-switch (proven in tests).
GRANT INSERT ON admin_credentials TO tcp_app;
GRANT UPDATE (status) ON admin_credentials TO tcp_app;
