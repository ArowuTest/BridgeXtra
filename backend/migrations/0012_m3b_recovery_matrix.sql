-- 0012_m3b_recovery_matrix.sql — M3b: full recovery matrix supports.
--
-- DD-19 (carried from the M1 deferred register): quarantined recovery money
-- now RECEIVES a ledger attribution. A quarantined event has no programme by
-- nature (unmatched subscriber / no open advance), so the liability is booked
-- at TELCO level: journals.programme_id becomes nullable, permitted ONLY for
-- the quarantine event type — every other posting still requires a programme.
ALTER TABLE journals ALTER COLUMN programme_id DROP NOT NULL;
ALTER TABLE journals ADD CONSTRAINT journals_programme_scope_ck
  CHECK (programme_id IS NOT NULL OR event_type = 'RECOVERY_QUARANTINED');

-- Reversals net against prior allocations (EDG-019): allocation rows may now
-- be negative (a reversal entry), never zero. Component sums stay net, so
-- the waterfall's recovered-so-far arithmetic is naturally reversal-aware.
ALTER TABLE recovery_allocations DROP CONSTRAINT recovery_allocations_amount_minor_check;
ALTER TABLE recovery_allocations ADD CONSTRAINT recovery_allocations_amount_nonzero_ck
  CHECK (amount_minor <> 0);

-- Post-write-off recoveries are INCOME, not receivable repayment (EDG-021):
-- new allocation component + reversal linkage on events.
ALTER TABLE recovery_allocations DROP CONSTRAINT recovery_allocations_component_check;
ALTER TABLE recovery_allocations ADD CONSTRAINT recovery_allocations_component_check
  CHECK (component IN ('FEE','PRINCIPAL','WRITEOFF_INCOME'));

-- Recovery events gain a REVERSED terminal marker: a fully-reversed event is
-- visible as such (partial reversals leave the event ALLOCATED with net
-- allocation rows telling the story).
ALTER TABLE recovery_events DROP CONSTRAINT recovery_events_state_check;
ALTER TABLE recovery_events ADD CONSTRAINT recovery_events_state_check
  CHECK (state IN ('PENDING','ALLOCATED','QUARANTINED','UNMATCHED','REVERSED'));

-- Chart of accounts v2 (global): write-off movement + post-write-off income
-- accounts. Supersede pattern — the chart is config, never edited in place.
UPDATE config_versions
SET state = 'SUPERSEDED', effective_to = now(), updated_at = now()
WHERE domain = 'ledger.accounts' AND scope = 'global' AND state = 'ACTIVE';

INSERT INTO config_versions
  (config_version_id, domain, scope, version_no, state, content, content_hash,
   effective_from, created_by, approved_by, reason)
VALUES
  ('cfg_seed_ledger_accounts_v2', 'ledger.accounts', 'global', 2, 'ACTIVE',
   '{"accounts":[{"code":"SUBSCRIBER_RECEIVABLE","kind":"ASSET"},{"code":"FEE_INCOME","kind":"INCOME"},{"code":"AIRTIME_FUNDING_CLEARING","kind":"LIABILITY"},{"code":"TELCO_SETTLEMENT_RECEIVABLE","kind":"ASSET"},{"code":"RECOVERY_SUSPENSE","kind":"LIABILITY"},{"code":"WRITEOFF_RECOVERY_INCOME","kind":"INCOME"},{"code":"WRITE_OFF_EXPENSE","kind":"EXPENSE"}]}',
   encode(sha256('{"accounts":[{"code":"SUBSCRIBER_RECEIVABLE","kind":"ASSET"},{"code":"FEE_INCOME","kind":"INCOME"},{"code":"AIRTIME_FUNDING_CLEARING","kind":"LIABILITY"},{"code":"TELCO_SETTLEMENT_RECEIVABLE","kind":"ASSET"},{"code":"RECOVERY_SUSPENSE","kind":"LIABILITY"},{"code":"WRITEOFF_RECOVERY_INCOME","kind":"INCOME"},{"code":"WRITE_OFF_EXPENSE","kind":"EXPENSE"}]}'::bytea),'hex'),
   now(), 'seed:builder', 'seed:reviewer',
   'Chart v2 (M3b/M3c): WRITEOFF_RECOVERY_INCOME (EDG-021 post-write-off recoveries are income, the loss stays crystallised) + WRITE_OFF_EXPENSE (loss crystallisation movement)');
