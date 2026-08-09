-- 0074_webhook_cred_held_recharge_immutability.sql — external-audit BX-HIGH-013/014.
--
-- Two tables in the recharge-webhook money path carried table-wide tcp_app UPDATE
-- grants (0051/0052) — the same class the self-audit fixed for write_offs (0021),
-- settlement_statements/guardrail_trips (0020) and config_versions (0019):
--
--   HIGH-013 telco_webhook_credentials — the PUBLIC key_id -> telco -> secret_env
--     root-of-trust map. A table-wide UPDATE let a live credential be re-bound to
--     another telco or secret in place (its sibling telco_api_credentials is
--     SELECT-only). Lock it: only status may change (ACTIVE -> REVOKED), the trust
--     binding is write-once, and a REVOKED credential is terminal.
--
--   HIGH-014 held_recharge_events — the HELD maker-checker release queue. A
--     table-wide UPDATE let ONE statement set requested_by + approved_by + RELEASED,
--     bypassing four-eyes at the DB (the approved_by<>requested_by CHECK stops equal
--     values, not setting BOTH in one statement). Lock it: identity/amount/event
--     evidence write-once, terminal states frozen, and — the four-eyes backstop — an
--     approval requires a PRE-EXISTING request, so no single UPDATE can self-approve.
--
-- Both layers, per the in-tree pattern: a column-scoped grant AND a trigger. The
-- trigger is load-bearing where the worker/app DSN owns the table (managed Postgres),
-- because column grants do not bind the owner — the trigger does.

-- --- HIGH-013: telco_webhook_credentials -----------------------------------
REVOKE UPDATE ON telco_webhook_credentials FROM tcp_app;
GRANT UPDATE (status) ON telco_webhook_credentials TO tcp_app;

CREATE OR REPLACE FUNCTION telco_webhook_credential_immutable()
RETURNS trigger AS $$
BEGIN
  IF OLD.status = 'REVOKED' THEN
    RAISE EXCEPTION 'telco_webhook_credentials: a REVOKED credential is immutable (BX-HIGH-013)';
  END IF;
  IF NEW.key_id     IS DISTINCT FROM OLD.key_id
  OR NEW.telco_id   IS DISTINCT FROM OLD.telco_id
  OR NEW.secret_env IS DISTINCT FROM OLD.secret_env
  OR NEW.label      IS DISTINCT FROM OLD.label
  OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION 'telco_webhook_credentials: the credential identity/secret binding is immutable — only status may change (BX-HIGH-013)';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER telco_webhook_credential_immutable
  BEFORE UPDATE ON telco_webhook_credentials
  FOR EACH ROW EXECUTE FUNCTION telco_webhook_credential_immutable();

-- --- HIGH-014: held_recharge_events ----------------------------------------
REVOKE UPDATE ON held_recharge_events FROM tcp_app;
GRANT UPDATE (status, requested_by, approved_by, resolved_at) ON held_recharge_events TO tcp_app;

CREATE OR REPLACE FUNCTION held_recharge_immutable()
RETURNS trigger AS $$
BEGIN
  IF OLD.status IN ('RELEASED','REJECTED') THEN
    RAISE EXCEPTION 'held_recharge_events: a %-state hold is immutable (BX-HIGH-014)', OLD.status;
  END IF;
  IF NEW.held_id         IS DISTINCT FROM OLD.held_id
  OR NEW.telco_id        IS DISTINCT FROM OLD.telco_id
  OR NEW.source_event_id IS DISTINCT FROM OLD.source_event_id
  OR NEW.msisdn_token    IS DISTINCT FROM OLD.msisdn_token
  OR NEW.amount_minor    IS DISTINCT FROM OLD.amount_minor
  OR NEW.currency        IS DISTINCT FROM OLD.currency
  OR NEW.occurred_at     IS DISTINCT FROM OLD.occurred_at
  OR NEW.reason          IS DISTINCT FROM OLD.reason
  OR NEW.held_at         IS DISTINCT FROM OLD.held_at THEN
    RAISE EXCEPTION 'held_recharge_events: identity/amount/event evidence are immutable (BX-HIGH-014)';
  END IF;
  IF OLD.requested_by IS NOT NULL AND NEW.requested_by IS DISTINCT FROM OLD.requested_by THEN
    RAISE EXCEPTION 'held_recharge_events: requested_by is immutable once set (BX-HIGH-014)';
  END IF;
  -- Four-eyes AT THE DB: a release approval requires a request that already exists in a
  -- prior committed state, so requester and approver can never be set in one UPDATE.
  IF NEW.approved_by IS NOT NULL AND OLD.requested_by IS NULL THEN
    RAISE EXCEPTION 'held_recharge_events: a release approval requires a prior distinct request — four-eyes (BX-HIGH-014)';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER held_recharge_immutable
  BEFORE UPDATE ON held_recharge_events
  FOR EACH ROW EXECUTE FUNCTION held_recharge_immutable();
