-- 0068 — Feed-Health monitor (Wave B.4): operator read grants for the four grant-backed
-- tiles. Additive, read-only, mirrors 0063/0067 exactly. Four new doors onto the
-- RLS-enforced, SELECT-only tcp_operator read role. Each table ALREADY has RLS enabled with
-- a role-agnostic telco policy (verified), so a telco-scoped operator stays bounded to its
-- own rows and the op_all_* policy below is the ONLY cross-telco path — it fires only for the
-- '*' admin (app.op_all='true') and fails closed when unset (NULL <> 'true'). No write grants.
--   recon_runs (0035, t_recon_runs)                        — B4 recon-run health + REJECTED count
--   suspense_items (0004, t_suspense)                      — B5 over-recovery suspense (money)
--   recovery_unconfirmed_closes (0056, t_..._closes)       — C2 stuck unconfirmed closes
--   audit_events (0001, audit_tenant)                      — B6 feed denials. NOTE: denials are
--       written telco_id NULL (platform scope, InsertPlatform), so a telco operator's tenant
--       policy matches none — B6 is a '*'-admin ESTATE view only (the handler labels it so).
--
-- Deliberately NOT granted:
--   recon_layer_arming — not RLS-enabled, so an op_all policy would be inert AND a naive
--     estate query would leak every telco's row. A2 reuses ReconArming.IsLayerLive per-telco
--     on the tcp_app control pool instead.
--   recovery_eod_feed  — the simulator seeder's store, not the real feed (the HTTPS adapter
--     fetches, reconciles, and discards without persisting). Reading it for freshness would be
--     blank for every real telco; real freshness is recon_runs.created_at + IsLayerLive.

GRANT SELECT ON recon_runs TO tcp_operator;
CREATE POLICY op_all_recon_runs ON recon_runs
  FOR SELECT TO tcp_operator USING (current_setting('app.op_all', true) = 'true');

GRANT SELECT ON suspense_items TO tcp_operator;
CREATE POLICY op_all_suspense_items ON suspense_items
  FOR SELECT TO tcp_operator USING (current_setting('app.op_all', true) = 'true');

GRANT SELECT ON recovery_unconfirmed_closes TO tcp_operator;
CREATE POLICY op_all_recovery_unconfirmed_closes ON recovery_unconfirmed_closes
  FOR SELECT TO tcp_operator USING (current_setting('app.op_all', true) = 'true');

GRANT SELECT ON audit_events TO tcp_operator;
CREATE POLICY op_all_audit_events ON audit_events
  FOR SELECT TO tcp_operator USING (current_setting('app.op_all', true) = 'true');
