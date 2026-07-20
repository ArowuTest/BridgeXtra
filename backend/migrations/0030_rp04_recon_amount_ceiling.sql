-- 0030_rp04_recon_amount_ceiling.sql — R-P0-3/4. Reconciliation now compares
-- currency before amount and range-validates external telco amounts before any
-- subtraction (overflow-safe). The ceiling is a governed config value
-- (no-hardcoding): add max_amount_minor to recon.tolerance.
--
-- config_versions are immutable and ACTIVE periods may not overlap, so this
-- SUPERSEDES the v1 seed with a v2 that carries the new field. now() is the
-- transaction timestamp (constant within this migration's tx), so v1's
-- effective_to and v2's effective_from are IDENTICAL — the [from,to) ranges
-- touch without overlapping and the exclusion constraint is satisfied.

-- New break classes (R-P0-3 currency, R-P0-4 malformed) need the status CHECK
-- widened. Breaks are still recorded, never auto-resolved.
ALTER TABLE recon_items DROP CONSTRAINT recon_items_status_check;
ALTER TABLE recon_items ADD CONSTRAINT recon_items_status_check
  CHECK (status IN ('MATCHED','BREAK_MISSING_PLATFORM','BREAK_MISSING_TELCO',
                    'BREAK_AMOUNT_MISMATCH','BREAK_CURRENCY_MISMATCH','BREAK_MALFORMED_TELCO_RECORD'));

UPDATE config_versions
   SET state = 'SUPERSEDED', effective_to = now()
 WHERE config_version_id = 'cfg_seed_recon_v1';

INSERT INTO config_versions
  (config_version_id, domain, scope, version_no, state, content, content_hash,
   effective_from, created_by, approved_by, reason)
VALUES
  ('cfg_seed_recon_v2', 'recon.tolerance', 'programme:prg_sim_airtime01', 2, 'ACTIVE',
   '{"amount_tolerance_minor":0,"auto_resolve":false,"break_aging_alert_hours":24,"max_amount_minor":1000000000000}',
   encode(sha256('{"amount_tolerance_minor":0,"auto_resolve":false,"break_aging_alert_hours":24,"max_amount_minor":1000000000000}'::bytea),'hex'),
   now(), 'seed:builder', 'seed:reviewer',
   'R-P0-4: adds max_amount_minor (1e12 kobo = ₦10bn) — the credible-amount / overflow-guard ceiling for reconciliation. Retains ZERO tolerance and no auto-resolve.');
