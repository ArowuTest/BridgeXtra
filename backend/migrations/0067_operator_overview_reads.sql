-- 0067 — MI dashboard (Overview): operator read grants for the health tiles.
-- Additive, read-only, mirrors 0061/0063 exactly. Two new doors onto the RLS-enforced,
-- SELECT-only tcp_operator read role:
--   1. funding_pools — the pool-exposure-headroom tile (A10) reads committed/reserved/
--      utilised. Not previously granted to the operator role.
--   2. op_all policies for programmes + telcos — these tables were granted SELECT to
--      tcp_operator (0044) but have NO op_all policy, so the '*' platform-admin estate
--      view falls through to EMPTY. Add them so the per-programme health tiles (state,
--      daily-cap headroom, exposure) resolve for the admin AND telco-scoped operators
--      uniformly. guardrail_trips already has SELECT + op_all (0044).
-- Telco-scoped operators keep their existing tenant RLS; the op_all policies only fire
-- when app.op_all='true' (set by the OperatorReader for the '*' admin). No write grants.

GRANT SELECT ON funding_pools TO tcp_operator;

CREATE POLICY op_all_funding_pools ON funding_pools
  FOR SELECT TO tcp_operator USING (current_setting('app.op_all', true) = 'true');

CREATE POLICY op_all_programmes ON programmes
  FOR SELECT TO tcp_operator USING (current_setting('app.op_all', true) = 'true');

CREATE POLICY op_all_telcos ON telcos
  FOR SELECT TO tcp_operator USING (current_setting('app.op_all', true) = 'true');
