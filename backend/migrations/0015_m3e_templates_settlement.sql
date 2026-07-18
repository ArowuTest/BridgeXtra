-- 0015_m3e_templates_settlement.sql — M3e: CFG-012 posting templates.
--
-- Every journal the platform posts is now RENDERED FROM A GOVERNED TEMPLATE
-- (ledger.templates, global scope). The validator refuses activation of any
-- template that could post unbalanced under ANY permitted branch — the
-- symbolic proof runs at approval, not at posting. Journals pin the template
-- version they were rendered from (nullable: pre-template rows keep their
-- history honest).

ALTER TABLE journals ADD COLUMN template_version TEXT;

INSERT INTO config_versions
  (config_version_id, domain, scope, version_no, state, content, content_hash,
   effective_from, created_by, approved_by, reason)
VALUES
  ('cfg_seed_ledger_templates_v1', 'ledger.templates', 'global', 1, 'ACTIVE',
   '{"templates":{"ADVANCE_ISSUED":{"lines":[{"account":"SUBSCRIBER_RECEIVABLE","side":"DEBIT","amount":"OUTSTANDING"},{"account":"AIRTIME_FUNDING_CLEARING","side":"CREDIT","amount":"DISBURSED"},{"account":"FEE_INCOME","side":"CREDIT","amount":"FEE","omit_when_zero":true}]},"RECOVERY_APPLIED":{"lines":[{"account":"TELCO_SETTLEMENT_RECEIVABLE","side":"DEBIT","amount":"AMOUNT"},{"account":"SUBSCRIBER_RECEIVABLE","side":"CREDIT","amount":"AMOUNT"}]},"RECOVERY_SUSPENSE":{"lines":[{"account":"TELCO_SETTLEMENT_RECEIVABLE","side":"DEBIT","amount":"AMOUNT"},{"account":"RECOVERY_SUSPENSE","side":"CREDIT","amount":"AMOUNT"}]},"RECOVERY_REVERSED":{"lines":[{"account":"SUBSCRIBER_RECEIVABLE","side":"DEBIT","amount":"AMOUNT"},{"account":"TELCO_SETTLEMENT_RECEIVABLE","side":"CREDIT","amount":"AMOUNT"}]},"RECOVERY_QUARANTINED":{"lines":[{"account":"TELCO_SETTLEMENT_RECEIVABLE","side":"DEBIT","amount":"AMOUNT"},{"account":"RECOVERY_SUSPENSE","side":"CREDIT","amount":"AMOUNT"}]},"WRITEOFF_RECOVERY_INC":{"lines":[{"account":"TELCO_SETTLEMENT_RECEIVABLE","side":"DEBIT","amount":"AMOUNT"},{"account":"WRITEOFF_RECOVERY_INCOME","side":"CREDIT","amount":"AMOUNT"}]},"WRITE_OFF":{"lines":[{"account":"WRITE_OFF_EXPENSE","side":"DEBIT","amount":"AMOUNT"},{"account":"SUBSCRIBER_RECEIVABLE","side":"CREDIT","amount":"AMOUNT"}]}}}',
   encode(sha256('{"templates":{"ADVANCE_ISSUED":{"lines":[{"account":"SUBSCRIBER_RECEIVABLE","side":"DEBIT","amount":"OUTSTANDING"},{"account":"AIRTIME_FUNDING_CLEARING","side":"CREDIT","amount":"DISBURSED"},{"account":"FEE_INCOME","side":"CREDIT","amount":"FEE","omit_when_zero":true}]},"RECOVERY_APPLIED":{"lines":[{"account":"TELCO_SETTLEMENT_RECEIVABLE","side":"DEBIT","amount":"AMOUNT"},{"account":"SUBSCRIBER_RECEIVABLE","side":"CREDIT","amount":"AMOUNT"}]},"RECOVERY_SUSPENSE":{"lines":[{"account":"TELCO_SETTLEMENT_RECEIVABLE","side":"DEBIT","amount":"AMOUNT"},{"account":"RECOVERY_SUSPENSE","side":"CREDIT","amount":"AMOUNT"}]},"RECOVERY_REVERSED":{"lines":[{"account":"SUBSCRIBER_RECEIVABLE","side":"DEBIT","amount":"AMOUNT"},{"account":"TELCO_SETTLEMENT_RECEIVABLE","side":"CREDIT","amount":"AMOUNT"}]},"RECOVERY_QUARANTINED":{"lines":[{"account":"TELCO_SETTLEMENT_RECEIVABLE","side":"DEBIT","amount":"AMOUNT"},{"account":"RECOVERY_SUSPENSE","side":"CREDIT","amount":"AMOUNT"}]},"WRITEOFF_RECOVERY_INC":{"lines":[{"account":"TELCO_SETTLEMENT_RECEIVABLE","side":"DEBIT","amount":"AMOUNT"},{"account":"WRITEOFF_RECOVERY_INCOME","side":"CREDIT","amount":"AMOUNT"}]},"WRITE_OFF":{"lines":[{"account":"WRITE_OFF_EXPENSE","side":"DEBIT","amount":"AMOUNT"},{"account":"SUBSCRIBER_RECEIVABLE","side":"CREDIT","amount":"AMOUNT"}]}}}'::bytea),'hex'),
   now(), 'seed:builder', 'seed:reviewer',
   'CFG-012 (carried from the M1 deferred register, now closed): every posting renders from this governed template set; the validator proves per-symbol balance under all branches (OUTSTANDING=DISBURSED+FEE identity; omit_when_zero lines vanish only when their symbol is zero) before any version can activate');
