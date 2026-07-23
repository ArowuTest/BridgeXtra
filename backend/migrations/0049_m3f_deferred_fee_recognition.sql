-- 0049_m3f_deferred_fee_recognition.sql — deferred fee recognition (money core).
--
-- Recognize fee income as it is RECOVERED, not at issuance. Config-driven policy
-- fee_recognition in {UPFRONT, DEFERRED} (seeded global default DEFERRED), PINNED
-- per-advance at origination and replayed downstream — the pin (not a fresh
-- config read at each posting site) is what makes recovery/reversal/write-off
-- symmetric even if the policy is re-activated mid-life.
--
-- Mechanism (one governed template per event, self-cancelling same-symbol DR/CR
-- pairs; a fail-closed policy bool binds each recognition symbol to a real amount
-- under DEFERRED or to ZERO under UPFRONT — no variant-template key that could
-- silently fall back to UPFRONT):
--   * ADVANCE_ISSUED (DEFERRED): CR FEE_INCOME=FEE then DR FEE_INCOME / CR
--     UNEARNED_FEE = FEE_DEFER_ADJ(=fee) — FEE_INCOME nets 0, fee lands in the
--     UNEARNED_FEE liability. OUTSTANDING = DISBURSED + FEE unchanged.
--   * RECOVERY_APPLIED (DEFERRED): DR UNEARNED_FEE / CR FEE_INCOME = the
--     waterfall-allocated fee-portion (FEE_RECOGNIZED).
--   * RECOVERY_REVERSED (DEFERRED): the exact transpose (de-recognise).
--   * WRITE_OFF (DEFERRED): DR UNEARNED_FEE / CR WRITE_OFF_EXPENSE = the remaining
--     unearned fee (recomputed fresh at approval) — never recognized as income.
-- UPFRONT binds every recognition symbol to zero, so those legs omit and the
-- rendered journal is byte-identical to the pre-migration behaviour.

-- --- Chart of accounts v3: add UNEARNED_FEE (deferred-fee liability) ----------
-- Must activate BEFORE templates v2 (the template validator cross-checks accounts
-- against the ACTIVE chart). Supersede pattern — chart is config, never edited.
UPDATE config_versions
SET state = 'SUPERSEDED', effective_to = now(), updated_at = now()
WHERE domain = 'ledger.accounts' AND scope = 'global' AND state = 'ACTIVE';

WITH t AS (
  SELECT '{"accounts":[{"code":"SUBSCRIBER_RECEIVABLE","kind":"ASSET"},{"code":"FEE_INCOME","kind":"INCOME"},{"code":"AIRTIME_FUNDING_CLEARING","kind":"LIABILITY"},{"code":"TELCO_SETTLEMENT_RECEIVABLE","kind":"ASSET"},{"code":"RECOVERY_SUSPENSE","kind":"LIABILITY"},{"code":"WRITEOFF_RECOVERY_INCOME","kind":"INCOME"},{"code":"WRITE_OFF_EXPENSE","kind":"EXPENSE"},{"code":"UNEARNED_FEE","kind":"LIABILITY"}]}'::text AS c
)
INSERT INTO config_versions
  (config_version_id, domain, scope, version_no, state, content, content_hash,
   effective_from, created_by, approved_by, reason)
SELECT 'cfg_seed_ledger_accounts_v3', 'ledger.accounts', 'global', 3, 'ACTIVE',
       t.c::jsonb, encode(sha256(t.c::bytea), 'hex'),
       now(), 'seed:builder', 'seed:reviewer',
       'Chart v3: UNEARNED_FEE (deferred-fee liability / contra-revenue) for fee_recognition=DEFERRED'
FROM t;

-- --- Templates v2: augment the four fee-touching events with recognition legs -
UPDATE config_versions
SET state = 'SUPERSEDED', effective_to = now(), updated_at = now()
WHERE domain = 'ledger.templates' AND scope = 'global' AND state = 'ACTIVE';

WITH t AS (
  SELECT '{"templates":{"ADVANCE_ISSUED":{"lines":[{"account":"SUBSCRIBER_RECEIVABLE","side":"DEBIT","amount":"OUTSTANDING"},{"account":"AIRTIME_FUNDING_CLEARING","side":"CREDIT","amount":"DISBURSED"},{"account":"FEE_INCOME","side":"CREDIT","amount":"FEE","omit_when_zero":true},{"account":"FEE_INCOME","side":"DEBIT","amount":"FEE_DEFER_ADJ","omit_when_zero":true},{"account":"UNEARNED_FEE","side":"CREDIT","amount":"FEE_DEFER_ADJ","omit_when_zero":true}]},"RECOVERY_APPLIED":{"lines":[{"account":"TELCO_SETTLEMENT_RECEIVABLE","side":"DEBIT","amount":"AMOUNT"},{"account":"SUBSCRIBER_RECEIVABLE","side":"CREDIT","amount":"AMOUNT"},{"account":"UNEARNED_FEE","side":"DEBIT","amount":"FEE_RECOGNIZED","omit_when_zero":true},{"account":"FEE_INCOME","side":"CREDIT","amount":"FEE_RECOGNIZED","omit_when_zero":true}]},"RECOVERY_SUSPENSE":{"lines":[{"account":"TELCO_SETTLEMENT_RECEIVABLE","side":"DEBIT","amount":"AMOUNT"},{"account":"RECOVERY_SUSPENSE","side":"CREDIT","amount":"AMOUNT"}]},"RECOVERY_REVERSED":{"lines":[{"account":"SUBSCRIBER_RECEIVABLE","side":"DEBIT","amount":"AMOUNT"},{"account":"TELCO_SETTLEMENT_RECEIVABLE","side":"CREDIT","amount":"AMOUNT"},{"account":"FEE_INCOME","side":"DEBIT","amount":"FEE_RECOGNIZED","omit_when_zero":true},{"account":"UNEARNED_FEE","side":"CREDIT","amount":"FEE_RECOGNIZED","omit_when_zero":true}]},"RECOVERY_QUARANTINED":{"lines":[{"account":"TELCO_SETTLEMENT_RECEIVABLE","side":"DEBIT","amount":"AMOUNT"},{"account":"RECOVERY_SUSPENSE","side":"CREDIT","amount":"AMOUNT"}]},"WRITEOFF_RECOVERY_INC":{"lines":[{"account":"TELCO_SETTLEMENT_RECEIVABLE","side":"DEBIT","amount":"AMOUNT"},{"account":"WRITEOFF_RECOVERY_INCOME","side":"CREDIT","amount":"AMOUNT"}]},"WRITE_OFF":{"lines":[{"account":"WRITE_OFF_EXPENSE","side":"DEBIT","amount":"AMOUNT"},{"account":"SUBSCRIBER_RECEIVABLE","side":"CREDIT","amount":"AMOUNT"},{"account":"UNEARNED_FEE","side":"DEBIT","amount":"FEE_UNEARNED_REVERSED","omit_when_zero":true},{"account":"WRITE_OFF_EXPENSE","side":"CREDIT","amount":"FEE_UNEARNED_REVERSED","omit_when_zero":true}]}}}'::text AS c
)
INSERT INTO config_versions
  (config_version_id, domain, scope, version_no, state, content, content_hash,
   effective_from, created_by, approved_by, reason)
SELECT 'cfg_seed_ledger_templates_v2', 'ledger.templates', 'global', 2, 'ACTIVE',
       t.c::jsonb, encode(sha256(t.c::bytea), 'hex'),
       now(), 'seed:builder', 'seed:reviewer',
       'Templates v2 (deferred fee recognition): ADVANCE_ISSUED/RECOVERY_APPLIED/RECOVERY_REVERSED/WRITE_OFF gain self-cancelling UNEARNED_FEE recognition legs (bound to zero under UPFRONT so the journal is byte-identical); unchanged events copied verbatim from v1'
FROM t;

-- --- fee_recognition policy (new domain), seeded global default DEFERRED -------
WITH t AS (
  SELECT '{"policy":"DEFERRED"}'::text AS c
)
INSERT INTO config_versions
  (config_version_id, domain, scope, version_no, state, content, content_hash,
   effective_from, created_by, approved_by, reason)
SELECT 'cfg_seed_fee_recognition_v1', 'fee_recognition', 'global', 1, 'ACTIVE',
       t.c::jsonb, encode(sha256(t.c::bytea), 'hex'),
       now(), 'seed:builder', 'seed:reviewer',
       'fee_recognition default DEFERRED (recognise fee as recovered). Read fail-closed at origination and PINNED on the advance; programme/telco overrides may be authored via the portal'
FROM t;

-- --- Pin column on advances (set once at origination, never mutated) ----------
-- Legacy NULL is treated as UPFRONT by the code: an advance issued before this
-- feature already recognised its fee at origination, so replaying UPFRONT for its
-- recoveries/write-off is the only correct accounting (no retroactive change).
ALTER TABLE advances ADD COLUMN fee_recognition TEXT;
ALTER TABLE advances ADD CONSTRAINT advances_fee_recognition_chk
  CHECK (fee_recognition IS NULL OR fee_recognition IN ('UPFRONT', 'DEFERRED'));
