-- Wave B.1 (loan book): the operator loan-book read-model joins recovery_allocations
-- for recovered-to-date (net of reversals). tcp_operator (the RLS-enforced, SELECT-only
-- operator READ role) was granted SELECT on advances / subscriber_accounts / journals /
-- journal_entries (0044) but NOT recovery_allocations, so the join would fail closed.
--
-- Add it, read-only + additive:
--   1. SELECT grant.
--   2. The op_all policy for the '*' platform admin (mirrors op_all_journal_entries,
--      0044) — an OR-ed permissive policy that fires only when app.op_all='true'.
-- The telco-scoped operator path is already covered by the existing role-agnostic
-- t_alloc policy (0004:266), which delegates visibility to the advance's own RLS
-- (EXISTS over advances, itself telco-scoped), so a telco operator sees allocations
-- only for advances in their scope. No INSERT/UPDATE/DELETE is granted — the money
-- trail stays append-only and operator reads stay read-only.

GRANT SELECT ON recovery_allocations TO tcp_operator;

CREATE POLICY op_all_recovery_allocations ON recovery_allocations
  FOR SELECT TO tcp_operator USING (current_setting('app.op_all', true) = 'true');
