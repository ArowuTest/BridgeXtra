-- 0021_writeoff_immutability.sql — reviewer VR-32 / self-audit continuation.
--
-- A write-off is a maker-checker crystallised-loss record. Its amounts, the
-- advance it discharges, and the requester/approver identities are the audit
-- trail of a real financial write-down. It carried a table-wide tcp_app UPDATE
-- grant, so a posted loss could be rewritten in place (same class as the
-- settlement_statements fix in 0020, which the self-audit noted but deferred).
--
-- Legitimate mutations (repo/writeoffs.go): Decide REQUESTED->APPROVED|REJECTED
-- (state, approved_by, decided_at) and MarkPosted APPROVED->POSTED (state,
-- posted_at). Everything else is write-once.

-- Column-scoped UPDATE grant.
REVOKE UPDATE ON write_offs FROM tcp_app;
GRANT UPDATE (state, approved_by, decided_at, posted_at) ON write_offs TO tcp_app;

-- Trigger backstop: terminal states frozen, identity/amounts/requester
-- immutable, and the approver fixed once recorded (maker-checker integrity) —
-- enforced even against the table owner.
CREATE OR REPLACE FUNCTION write_off_immutable()
RETURNS trigger AS $$
BEGIN
  IF OLD.state IN ('POSTED','REJECTED') THEN
    RAISE EXCEPTION 'write_offs: a %-state write-off is immutable (self-audit)', OLD.state;
  END IF;
  IF NEW.write_off_id    IS DISTINCT FROM OLD.write_off_id
  OR NEW.telco_id        IS DISTINCT FROM OLD.telco_id
  OR NEW.advance_id      IS DISTINCT FROM OLD.advance_id
  OR NEW.principal_minor IS DISTINCT FROM OLD.principal_minor
  OR NEW.fee_minor       IS DISTINCT FROM OLD.fee_minor
  OR NEW.currency        IS DISTINCT FROM OLD.currency
  OR NEW.reason          IS DISTINCT FROM OLD.reason
  OR NEW.requested_by    IS DISTINCT FROM OLD.requested_by THEN
    RAISE EXCEPTION 'write_offs: identity/amounts/requester are immutable (self-audit)';
  END IF;
  IF OLD.approved_by IS NOT NULL AND NEW.approved_by IS DISTINCT FROM OLD.approved_by THEN
    RAISE EXCEPTION 'write_offs: approved_by is immutable once set (self-audit)';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER write_off_immutable
  BEFORE UPDATE ON write_offs
  FOR EACH ROW EXECUTE FUNCTION write_off_immutable();
