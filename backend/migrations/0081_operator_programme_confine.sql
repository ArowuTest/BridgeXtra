-- 0081_operator_programme_confine.sql — BX-MED-010 programme-scoped operator predicate.
--
-- A programme-scoped portal operator (scope 'programme:<id>') is only meant to see its OWN
-- programme's data. The OperatorReader chokepoint pins the TELCO for such a session (resolving the
-- programme's owning telco into app.telco_id), and the existing PUBLIC telco policies (t_advances,
-- t_journals, t_guardrail_trips, t_settlement_statements) confine reads to that telco. But nothing
-- confined them to the PROGRAMME: the programme predicate was left to each repo query's caller
-- filter, applied inconsistently (portalops applies it; ListAdvances / LoanBookSummary did not). So
-- a programme:A operator could read programme B's advances / loan book / journals / settlements /
-- guardrail trips within the same telco — an intra-tenant scope bypass on money and risk data.
--
-- Fix: make the programme bound DB-ENFORCED and impossible to forget, exactly as the telco bound
-- is. The OperatorReader now also sets app.programme_id for a programme-scoped session; these
-- RESTRICTIVE policies AND a programme match onto the permissive telco/op_all result for the
-- operator role. They are scoped TO tcp_operator, so origination (tcp_app) and the worker
-- (tcp_worker, BYPASSRLS) are untouched; and they are permissive when app.programme_id is unset, so
-- a telco-scoped or '*' admin operator (which never sets it) reads exactly as before. Only the four
-- tables that actually carry a programme_id — the programme-grained money/risk surface — need
-- confining; telco-grained resources (recon, subscribers) already fail closed for a programme scope
-- via the app-layer TelcoLevelBound guard.
--
-- current_setting('app.programme_id', true) is NULL when unset (telco/admin operator) -> permissive;
-- it equals the session's programme when set -> confines. Reversible: DROP POLICY.

CREATE POLICY op_programme_confine_advances ON advances AS RESTRICTIVE FOR SELECT TO tcp_operator
  USING (current_setting('app.programme_id', true) IS NULL
         OR current_setting('app.programme_id', true) = ''
         OR programme_id = current_setting('app.programme_id', true));

CREATE POLICY op_programme_confine_journals ON journals AS RESTRICTIVE FOR SELECT TO tcp_operator
  USING (current_setting('app.programme_id', true) IS NULL
         OR current_setting('app.programme_id', true) = ''
         OR programme_id = current_setting('app.programme_id', true));

CREATE POLICY op_programme_confine_guardrail_trips ON guardrail_trips AS RESTRICTIVE FOR SELECT TO tcp_operator
  USING (current_setting('app.programme_id', true) IS NULL
         OR current_setting('app.programme_id', true) = ''
         OR programme_id = current_setting('app.programme_id', true));

CREATE POLICY op_programme_confine_settlement_statements ON settlement_statements AS RESTRICTIVE FOR SELECT TO tcp_operator
  USING (current_setting('app.programme_id', true) IS NULL
         OR current_setting('app.programme_id', true) = ''
         OR programme_id = current_setting('app.programme_id', true));
