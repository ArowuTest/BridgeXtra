-- 0046_operator_demo_runs_grant.sql — Gate B #1 Slice 2b. demo_runs is the 14th
-- portal-read table (the M4e-3 fault-demo run list/detail) and was missed by the
-- Slice-1 grant set. It already has the standard telco RLS policy, so the
-- read-only operator role needs SELECT + the op_all disjunct like the other 13.
-- Separate migration (0044 is applied; the runner keys on version int).
GRANT SELECT ON demo_runs TO tcp_operator;
CREATE POLICY op_all_demo_runs ON demo_runs
  FOR SELECT TO tcp_operator USING (current_setting('app.op_all', true) = 'true');
