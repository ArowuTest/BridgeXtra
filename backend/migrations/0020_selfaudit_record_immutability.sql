-- 0020_selfaudit_record_immutability.sql — self-audit finding (EXT-3 class).
--
-- Two records that are contractual / audit evidence carried table-WIDE UPDATE
-- grants to tcp_app, so any column could be rewritten in place:
--
--   * settlement_statements — a FINAL statement is the partner's contractual
--     record; its content_hash is the very thing the verify tool checks. Its
--     only legitimate mutation is DRAFT -> FINAL (state/content_hash/
--     finalised_at). A FINAL row must be FROZEN.
--   * guardrail_trips — the recorded breach evidence (measured/limit). Its
--     only legitimate mutation is the two-person re-arm lifecycle.
--
-- Fix, matching the tcp_worker column-scope discipline (and the config
-- immutability of 0019):
--   1. Least-privilege column-scoped UPDATE grants.
--   2. For settlement, a trigger backstop: a FINAL statement is immutable even
--      to the table owner, and identity/period/terms never change.

-- settlement_statements: column-scoped UPDATE + FINAL-frozen trigger.
REVOKE UPDATE ON settlement_statements FROM tcp_app;
GRANT UPDATE (state, content_hash, finalised_at) ON settlement_statements TO tcp_app;

CREATE OR REPLACE FUNCTION settlement_statement_immutable()
RETURNS trigger AS $$
BEGIN
  -- A FINAL statement is a frozen contractual record — no field changes, and
  -- it can never be re-opened or re-hashed.
  IF OLD.state = 'FINAL' THEN
    RAISE EXCEPTION 'settlement_statements: a FINAL statement is immutable (self-audit)';
  END IF;
  -- Identity, period, terms and currency are set at creation, never updated.
  IF NEW.telco_id         IS DISTINCT FROM OLD.telco_id
  OR NEW.programme_id     IS DISTINCT FROM OLD.programme_id
  OR NEW.period_start     IS DISTINCT FROM OLD.period_start
  OR NEW.period_end       IS DISTINCT FROM OLD.period_end
  OR NEW.terms_version_id IS DISTINCT FROM OLD.terms_version_id
  OR NEW.currency         IS DISTINCT FROM OLD.currency THEN
    RAISE EXCEPTION 'settlement_statements: identity/period/terms/currency are immutable (self-audit)';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER settlement_statement_immutable
  BEFORE UPDATE ON settlement_statements
  FOR EACH ROW EXECUTE FUNCTION settlement_statement_immutable();

-- guardrail_trips: only the re-arm lifecycle columns may change; the breach
-- evidence (measured_minor/limit_minor/currency/guardrail) is now unwritable
-- via the app role.
REVOKE UPDATE ON guardrail_trips FROM tcp_app;
GRANT UPDATE (state, rearm_requested_by, rearm_approved_by, rearmed_at)
  ON guardrail_trips TO tcp_app;
