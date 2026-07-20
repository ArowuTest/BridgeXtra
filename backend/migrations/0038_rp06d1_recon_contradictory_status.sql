-- 0038_rp06d1_recon_contradictory_status.sql — R-P0-6 Slice D1 (EDG-006).
-- A telco feed that carries BOTH a FAILED and a SUCCESS record for the same
-- fulfilment key is internally contradictory: the walking-skeleton dropped the
-- FAILED and reconciled the SUCCESS as if clean. That hides a data-quality
-- problem. Classify such a key as BREAK_CONTRADICTORY_TELCO_STATUS — a break
-- for ops, never a silent MATCHED.
ALTER TABLE recon_items DROP CONSTRAINT recon_items_status_check;
ALTER TABLE recon_items ADD CONSTRAINT recon_items_status_check
  CHECK (status IN ('MATCHED','BREAK_MISSING_PLATFORM','BREAK_MISSING_TELCO',
                    'BREAK_AMOUNT_MISMATCH','BREAK_CURRENCY_MISMATCH',
                    'BREAK_MALFORMED_TELCO_RECORD','BREAK_DUPLICATE_TELCO_RECORD',
                    'BREAK_CONTRADICTORY_TELCO_STATUS'));
