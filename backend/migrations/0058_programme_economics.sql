-- 0058_programme_economics.sql — Build 1: the programme economic/legal go-live gate.
-- A programme may be ACTIVE but cannot originate a loan until its economics
-- (funding model, lender of record, loss bearer, settlement method, tax treatment)
-- are configured. Origination reads programme.economics fail-closed (absent or
-- malformed → refuse), mirroring the recharge-feed "telco-scope row required → deny"
-- pattern. The validator (validators_economics.go, registered in the I14 fence)
-- gates maker-checker; the origination read re-validates (floor lives in code too).
--
-- No schema change — programme.economics is a configsvc domain, effective-dated +
-- maker-checker like every other. This migration seeds ONE valid ACTIVE version for
-- the synthetic programme prg_sim_airtime01 so the existing SIM_NG flows (tests,
-- simulator, walking skeleton) keep originating. Real programmes set theirs through
-- the governed maker-checker path; a programme with no economics simply cannot lend.
--
-- Scope is programme:<id> with NO global default — a global economics row would
-- authorise EVERY programme to lend, so origination additionally requires the
-- resolved config's scope to equal programme:<the programme> (no global-fallback
-- leak). Hence we seed ONLY the per-programme row, never a global one.

WITH t AS (
  SELECT '{"funding_model":"PLATFORM_CAPITAL","lender_of_record":"BridgeXtra Financial Services Ltd","loss_bearer":"BridgeXtra Financial Services Ltd","settlement_method":"NET_OFF_AGAINST_AIRTIME","tax_treatment":"VAT_INCLUSIVE_FEE"}'::text AS c
)
INSERT INTO config_versions
  (config_version_id, domain, scope, version_no, state, content, content_hash,
   effective_from, created_by, approved_by, reason)
SELECT 'cfg_seed_prog_economics_sim_v1', 'programme.economics', 'programme:prg_sim_airtime01', 1, 'ACTIVE',
       t.c::jsonb, encode(sha256(t.c::bytea), 'hex'),
       now(), 'seed:builder', 'seed:reviewer',
       'Seeded programme.economics for prg_sim_airtime01 (Build 1): PLATFORM_CAPITAL, lender+loss-bearer=BridgeXtra Financial Services Ltd, NET_OFF_AGAINST_AIRTIME, VAT_INCLUSIVE_FEE. Synthetic programme only — real programmes configure their own economics before they can originate.'
FROM t;
