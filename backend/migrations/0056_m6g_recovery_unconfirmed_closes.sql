-- 0056_m6g_recovery_unconfirmed_closes.sql — Phase 1 S3-C2: the re-origination
-- HOLD (build/PHASE1_S3_DESIGN.md §S3-C, Conflict B). A phantom/forged webhook
-- recovery that CLOSES a real debt would otherwise free the advances_one_active_uq
-- slot, letting the borrower take a fresh advance inside the recon detection
-- window — and the later recovery.Reverse reopen would then DEADLOCK on that
-- unique index. This eligibility ledger blocks RE-ORIGINATION for a subscriber
-- whose advance was CLOSED by a not-yet-EOD-confirmed wh: recovery, so no advance B
-- is ever originated while A is unconfirmed.
--
-- This is NOT a new money state: recovery still books finally at ingest (no
-- journal / allocation / recovery_events change). This is a SEPARATE table read by
-- origination as a precondition and cleared by the RECOVERY recon when the feed
-- confirms the subscriber's recovery (the token MATCHED on its business day).
--
-- No business_date column: the Lagos business day is derived at clear time from
-- the governed recon config tz (via the recovery_event's occurred_at), so no tz is
-- hardcoded here.

CREATE TABLE recovery_unconfirmed_closes (
  id                    TEXT PRIMARY KEY,
  telco_id              TEXT NOT NULL REFERENCES telcos(telco_id),
  subscriber_account_id TEXT NOT NULL,
  advance_id            TEXT NOT NULL,
  recovery_event_id     TEXT NOT NULL REFERENCES recovery_events(recovery_event_id),
  created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
  cleared_at            TIMESTAMPTZ,
  UNIQUE (recovery_event_id)  -- one hold per closing recovery (idempotent on retry)
);

ALTER TABLE recovery_unconfirmed_closes ENABLE ROW LEVEL SECURITY;
CREATE POLICY t_recovery_unconfirmed_closes ON recovery_unconfirmed_closes
  USING (telco_id = current_setting('app.telco_id', true))
  WITH CHECK (telco_id = current_setting('app.telco_id', true));

-- The origination precondition query: open holds for a subscriber.
CREATE INDEX recovery_unconfirmed_closes_open_ix
  ON recovery_unconfirmed_closes (telco_id, subscriber_account_id) WHERE cleared_at IS NULL;

-- Recovery ingest INSERTs (on a wh: close), origination SELECTs (precondition),
-- the RECOVERY recon UPDATEs cleared_at on confirmation. All run as tcp_app inside
-- a tenant tx (RLS). UPDATE is column-scoped to cleared_at — the hold's identity
-- (who / which advance / which event) is immutable once written.
GRANT SELECT, INSERT ON recovery_unconfirmed_closes TO tcp_app;
GRANT UPDATE (cleared_at) ON recovery_unconfirmed_closes TO tcp_app;
