-- 0044_operator_read_role.sql — Gate B #1 (DB-level tenant scope + role
-- separation). Portal operator READS ran on the worker pool (BYPASSRLS locally /
-- DB owner on Render), so RLS was bypassed and the only tenant boundary was the
-- app-layer WHERE telco_id=$scope. This adds a dedicated READ-ONLY operator role
-- that does NOT bypass RLS and never owns a table, so the EXISTING per-table
-- telco policies (telco_id = current_setting('app.telco_id', true), and the
-- parent-cascade policies on fulfilment_attempts/journal_entries) enforce the
-- tenant boundary in the DATABASE. A dropped or forged WHERE clause can no longer
-- leak cross-telco.
--
-- Failure mode is fail-closed: a read that forgets to SET LOCAL app.telco_id runs
-- with an empty setting, so telco_id = '' matches nothing and returns empty
-- (never a leak). This is why GUC-scoping beats security-barrier views here.
--
-- Scope handling:
--   * telco-scoped operator  -> app.telco_id set  -> DB-ENFORCED by the telco policy.
--   * '*' platform admin      -> reads across telcos: an added op_all policy path,
--     armed ONLY by a single server-side chokepoint that sets app.op_all='true'
--     for a '*' session. A telco/programme operator never sets it, so their reads
--     fall through to the telco policy. This global path is app-gated (the DB
--     can't tell a legit '*' admin from an app bug setting op_all) — bounded to
--     the rare global case, audited at the chokepoint. Accepted, documented.
--   * programme-scoped operator -> telco boundary DB-enforced when its telco is
--     pinned; programme is an intra-tenant filter (app-level). Cross-programme-
--     same-telco is a lower-severity residual (telco is the tenant boundary).

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'tcp_operator') THEN
    -- Read-only: LOGIN, but NEVER BYPASSRLS, NOSUPERUSER, and it owns no tables,
    -- so RLS fully applies. Actions/writes stay on tcp_app.
    CREATE ROLE tcp_operator LOGIN PASSWORD 'devlocal_operator' NOSUPERUSER NOCREATEDB NOCREATEROLE;
  END IF;
END $$;

GRANT USAGE ON SCHEMA public TO tcp_operator;
-- SELECT only, and only on the tables the portal operator reads. No INSERT/UPDATE/
-- DELETE anywhere — a compromised operator credential can read (within scope) but
-- never mutate money.
GRANT SELECT ON
  advances, complaints, fulfilment_attempts, guardrail_trips,
  journal_entries, journals, notifications, pending_reversals, recon_items,
  settlement_lines, settlement_statements, subscriber_accounts, subscriber_status_actions,
  telcos, programmes
  TO tcp_operator;

-- The op_all path for the '*' platform admin. Permissive policies are OR-ed, so a
-- tcp_operator sees (existing telco policy) OR (op_all). With app.op_all unset the
-- op_all disjunct is false and only the telco policy applies (fail-closed to the
-- tenant). Applied to every read table so a '*' admin can read the full estate.
CREATE POLICY op_all_advances                  ON advances                  FOR SELECT TO tcp_operator USING (current_setting('app.op_all', true) = 'true');
CREATE POLICY op_all_complaints                ON complaints                FOR SELECT TO tcp_operator USING (current_setting('app.op_all', true) = 'true');
CREATE POLICY op_all_fulfilment_attempts       ON fulfilment_attempts       FOR SELECT TO tcp_operator USING (current_setting('app.op_all', true) = 'true');
CREATE POLICY op_all_guardrail_trips           ON guardrail_trips           FOR SELECT TO tcp_operator USING (current_setting('app.op_all', true) = 'true');
CREATE POLICY op_all_journal_entries           ON journal_entries           FOR SELECT TO tcp_operator USING (current_setting('app.op_all', true) = 'true');
CREATE POLICY op_all_journals                  ON journals                  FOR SELECT TO tcp_operator USING (current_setting('app.op_all', true) = 'true');
CREATE POLICY op_all_notifications             ON notifications             FOR SELECT TO tcp_operator USING (current_setting('app.op_all', true) = 'true');
CREATE POLICY op_all_pending_reversals         ON pending_reversals         FOR SELECT TO tcp_operator USING (current_setting('app.op_all', true) = 'true');
CREATE POLICY op_all_recon_items               ON recon_items               FOR SELECT TO tcp_operator USING (current_setting('app.op_all', true) = 'true');
CREATE POLICY op_all_settlement_lines          ON settlement_lines          FOR SELECT TO tcp_operator USING (current_setting('app.op_all', true) = 'true');
CREATE POLICY op_all_settlement_statements     ON settlement_statements     FOR SELECT TO tcp_operator USING (current_setting('app.op_all', true) = 'true');
CREATE POLICY op_all_subscriber_accounts       ON subscriber_accounts       FOR SELECT TO tcp_operator USING (current_setting('app.op_all', true) = 'true');
CREATE POLICY op_all_subscriber_status_actions ON subscriber_status_actions FOR SELECT TO tcp_operator USING (current_setting('app.op_all', true) = 'true');
