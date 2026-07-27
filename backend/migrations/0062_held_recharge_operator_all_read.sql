-- Slice 0 (admin-UX foundation): the held-recharge review queue was unusable for a
-- '*' platform admin. The list read is RLS-scoped by the transaction; the only
-- policy on held_recharge_events is t_held_recharge_events (telco_id = app.telco_id,
-- 0052:53), so a '*' admin (op_all='true', app.telco_id='') matched nothing and the
-- page hung on an empty queue. The per-telco path already works via that t_ policy.
--
-- Add the op_all read policy for tcp_operator (mirrors op_all_recovery_allocations,
-- 0061, and op_all_journal_entries, 0044): an OR-ed permissive SELECT policy that
-- fires only when app.op_all='true', so a platform admin reading through the
-- operator chokepoint sees every telco's held queue. The SELECT grant to
-- tcp_operator already exists (0052:58). Read-only + additive — no INSERT/UPDATE/
-- DELETE, and the telco-scoped operator path is unchanged (still the t_ policy).

CREATE POLICY op_all_held_recharge_events ON held_recharge_events
  FOR SELECT TO tcp_operator USING (current_setting('app.op_all', true) = 'true');
