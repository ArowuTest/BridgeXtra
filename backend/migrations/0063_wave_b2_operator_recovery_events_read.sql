-- Wave B.2a (Subscriber-360): the operator subscriber read-model reads recovery_events
-- — for the directory's "last recharge" and the 360's recharges-in (money in, and how
-- much of each was applied). tcp_operator (the RLS-enforced, SELECT-only operator READ
-- role) was granted SELECT on advances / subscriber_accounts / journals / journal_entries
-- (0044) and recovery_allocations (0061), but NOT recovery_events, so those reads would
-- fail closed with "permission denied for table recovery_events".
--
-- Add it, read-only + additive, exactly as 0061 did for recovery_allocations:
--   1. SELECT grant.
--   2. The op_all policy for the '*' platform admin (mirrors op_all_journal_entries,
--      0044) — an OR-ed permissive policy that fires only when app.op_all='true'.
-- The telco-scoped operator path is already covered by the existing role-agnostic
-- t_recovery policy (0004:265), which scopes visibility to the operator's telco via
-- app.telco_id. No INSERT/UPDATE/DELETE is granted — the recovery trail stays
-- append-only and operator reads stay read-only.

GRANT SELECT ON recovery_events TO tcp_operator;

CREATE POLICY op_all_recovery_events ON recovery_events
  FOR SELECT TO tcp_operator USING (current_setting('app.op_all', true) = 'true');

-- The 360 also surfaces the credit limit + tier HISTORY from decision_snapshots
-- (condition #5). Same additive, read-only grant: SELECT + the '*'-admin op_all policy;
-- the telco-scoped path is covered by the role-agnostic t_decisions policy (0004:259).
GRANT SELECT ON decision_snapshots TO tcp_operator;

CREATE POLICY op_all_decision_snapshots ON decision_snapshots
  FOR SELECT TO tcp_operator USING (current_setting('app.op_all', true) = 'true');
