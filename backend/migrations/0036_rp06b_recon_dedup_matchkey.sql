-- 0036_rp06b_recon_dedup_matchkey.sql — R-P0-6 Slice B (AUD-P2-010 / R-P2-5).
-- The walking-skeleton recon processed every telco record independently, so a
-- telco success record reported TWICE for one fulfilment produced two items
-- (double-counted) with no duplicate classification. Slice B gives each item a
-- deterministic match_key (the logical thing being reconciled), classifies a
-- repeat of a key as BREAK_DUPLICATE_TELCO_RECORD, and enforces exactly one
-- CANONICAL item per (run, match_key) at the DB.

-- match_key: nullable so legacy pre-framework items (no key) are grandfathered;
-- every NEW item written by the framework carries one.
ALTER TABLE recon_items ADD COLUMN IF NOT EXISTS match_key TEXT;

-- New break class for a duplicated source record.
ALTER TABLE recon_items DROP CONSTRAINT recon_items_status_check;
ALTER TABLE recon_items ADD CONSTRAINT recon_items_status_check
  CHECK (status IN ('MATCHED','BREAK_MISSING_PLATFORM','BREAK_MISSING_TELCO',
                    'BREAK_AMOUNT_MISMATCH','BREAK_CURRENCY_MISMATCH',
                    'BREAK_MALFORMED_TELCO_RECORD','BREAK_DUPLICATE_TELCO_RECORD'));

-- Exactly ONE canonical (non-duplicate) item per (run, match_key): the primary
-- classification of a key appears once; extra source records for the same key
-- are BREAK_DUPLICATE_TELCO_RECORD and excluded here, so many duplicates are
-- allowed but a key can never have two canonical outcomes. Partial + WHERE
-- match_key IS NOT NULL so legacy null-key rows (and NULLs, which are distinct
-- in a unique index anyway) never conflict.
CREATE UNIQUE INDEX recon_items_canonical_uq
  ON recon_items (run_id, match_key)
  WHERE match_key IS NOT NULL AND status <> 'BREAK_DUPLICATE_TELCO_RECORD';
