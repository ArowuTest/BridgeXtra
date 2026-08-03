-- 0067 — MI dashboard (Overview): operator read grants for the health tiles.
-- Additive, read-only, mirrors 0061/0063 exactly. Two new doors onto the RLS-enforced,
-- SELECT-only tcp_operator read role:
--   1. funding_pools — the pool-exposure-headroom tile (A10) reads committed/reserved/
--      utilised. Not previously granted to the operator role. funding_pools has RLS enabled
--      with a role-agnostic telco policy (t_funding, 0004), so a telco-scoped operator is
--      bounded to its own pools; op_all_funding_pools below is the ONLY cross-telco path and
--      fires only for the '*' admin (app.op_all='true').
--   2. op_all policy for programmes — programmes was granted SELECT to tcp_operator (0044)
--      and has RLS enabled with a role-agnostic telco policy (programmes_tenant, 0001), but
--      NO op_all policy, so the '*' platform-admin estate view fell through to EMPTY. Add it
--      so the per-programme health tiles (state, daily-cap headroom, exposure) resolve for
--      the admin AND telco-scoped operators uniformly. guardrail_trips already has SELECT +
--      op_all (0044).
-- Telco-scoped operators keep their existing tenant RLS; the op_all policies only fire when
-- app.op_all='true' (set by the OperatorReader for the '*' admin), which fails closed when
-- unset (NULL <> 'true'). No write grants.
--
-- NOTE on telcos: the overview never reads the `telcos` table — it takes telco_id from the
-- RLS-scoped `programmes`/`advances` rows, never from telcos. And `telcos` has NO row-level
-- security enabled (just a plain SELECT grant since 0001/0044), so an op_all policy on it
-- would be inert (a policy does nothing until RLS is enabled). It is therefore deliberately
-- left untouched here. Enabling RLS on telcos so a telco-scoped operator cannot enumerate the
-- registry is a separate, broader hardening tracked outside this slice — it touches a
-- widely-shared reference table that tcp_app/tcp_worker also read.

GRANT SELECT ON funding_pools TO tcp_operator;

CREATE POLICY op_all_funding_pools ON funding_pools
  FOR SELECT TO tcp_operator USING (current_setting('app.op_all', true) = 'true');

CREATE POLICY op_all_programmes ON programmes
  FOR SELECT TO tcp_operator USING (current_setting('app.op_all', true) = 'true');
