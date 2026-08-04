-- 0071 — Collections write-off inbox (Wave B.3 Phase B): operator read grant on write_offs so
-- the checker can see the pending-approvals queue. Additive, read-only, mirrors 0061/0067.
-- write_offs ALREADY has RLS enabled with a role-agnostic telco policy (t_write_offs, 0011), so
-- a telco-scoped operator is bounded to its own pending write-offs; op_all_write_offs below is
-- the ONLY cross-telco path and fires only for the '*' admin (app.op_all='true', fails closed
-- when unset). No write grant — the write path stays on tcp_app via the collections usecase.

GRANT SELECT ON write_offs TO tcp_operator;
CREATE POLICY op_all_write_offs ON write_offs
  FOR SELECT TO tcp_operator USING (current_setting('app.op_all', true) = 'true');
