-- 0029_rp02_idempotency_response.sql — R-P0-2. The recovery ingest stores its
-- exact outcome on the idempotency record (in the same tx that books the
-- money) so a valid replay reproduces Applied/Excess/AdvanceClosed byte-for-
-- byte rather than re-deriving them. That needs UPDATE on the response
-- columns, which the 0001 column-scoped grant (terminal only) withholds.
--
-- Extend the scope to the response columns AND freeze them once written: the
-- response is stored exactly once (a duplicate takes the replay path and never
-- re-writes), so write-once is structural, not merely conventional — the same
-- immutability posture as write_offs / settlement_statements.

REVOKE UPDATE ON idempotency_records FROM tcp_app;
GRANT UPDATE (terminal, response_status, response_body) ON idempotency_records TO tcp_app;

CREATE OR REPLACE FUNCTION idempotency_response_write_once()
RETURNS trigger AS $$
BEGIN
  -- request_hash and identity are already UPDATE-denied by grant; this guards
  -- the response: once a real outcome is recorded (status <> 0), the stored
  -- response can never be rewritten — a replay must reproduce the original.
  IF OLD.response_status <> 0 AND
     (NEW.response_status IS DISTINCT FROM OLD.response_status
      OR NEW.response_body IS DISTINCT FROM OLD.response_body) THEN
    RAISE EXCEPTION 'idempotency_records: a recorded response is immutable (R-P0-2)';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER idempotency_response_write_once
  BEFORE UPDATE ON idempotency_records
  FOR EACH ROW EXECUTE FUNCTION idempotency_response_write_once();
