-- 0052_m6c_recharge_webhook_data.sql — Phase 1 S2.2a: the data the recharge
-- webhook handler needs. DORMANT until the handler (S2.2b) is wired.
--
-- Two structures the adversarial design pass required:
--   1. recon_layer_arming — the STRUCTURAL "no webhook money without
--      reconciliation" gate. S2 builds the gate that READS it; S3 POPULATES it
--      when it arms a telco's RECOVERY recon layer. Until then the table is
--      empty, so the handler refuses to ingest — reconciliation is a precondition
--      for ingestion, not merely a seeded-DISABLED default.
--   2. held_recharge_events — over-limit recharges (per-event / per-telco-daily
--      blast-radius clamps) are HELD in this durable, reviewable queue for a
--      maker-checker release (S2.3), never silently ingested or dropped.

-- ---------------------------------------------------------------------------
-- Recon-layer arming marker (control registry; read by telco_id before tenant
-- context in the kill-switch, like the credential lookup — so NOT RLS-scoped).
-- ---------------------------------------------------------------------------
CREATE TABLE recon_layer_arming (
  telco_id  TEXT NOT NULL REFERENCES telcos(telco_id),
  layer     TEXT NOT NULL,            -- e.g. 'RECOVERY', 'SETTLEMENT'
  live_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (telco_id, layer)
);
GRANT SELECT ON recon_layer_arming TO tcp_app;                 -- handler reads the gate
GRANT SELECT, INSERT, DELETE ON recon_layer_arming TO tcp_worker; -- S3 / ops arm-disarm

-- ---------------------------------------------------------------------------
-- HELD recharge events (tenant-scoped, RLS): an over-limit webhook recharge is
-- parked here for governed (maker-checker) release, never auto-ingested.
-- ---------------------------------------------------------------------------
CREATE TABLE held_recharge_events (
  held_id         TEXT PRIMARY KEY,
  telco_id        TEXT NOT NULL REFERENCES telcos(telco_id),
  source_event_id TEXT NOT NULL,                    -- namespaced "wh:"<event_id>
  msisdn_token    TEXT NOT NULL,
  amount_minor    BIGINT NOT NULL CHECK (amount_minor > 0),
  currency        CHAR(3) NOT NULL,
  occurred_at     TIMESTAMPTZ NOT NULL,
  reason          TEXT NOT NULL CHECK (reason IN ('PER_EVENT_CLAMP','DAILY_CEILING')),
  status          TEXT NOT NULL DEFAULT 'HELD' CHECK (status IN ('HELD','RELEASED','REJECTED')),
  held_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  -- maker-checker release (S2.3): a maker requests, a DISTINCT checker approves.
  requested_by    TEXT,
  approved_by     TEXT,
  resolved_at     TIMESTAMPTZ,
  CHECK (approved_by IS NULL OR requested_by IS NULL OR approved_by <> requested_by),
  UNIQUE (telco_id, source_event_id)                -- one hold per event
);
CREATE INDEX held_recharge_events_open_ix
  ON held_recharge_events (telco_id, held_at DESC) WHERE status = 'HELD';

ALTER TABLE held_recharge_events ENABLE ROW LEVEL SECURITY;
CREATE POLICY t_held_recharge_events ON held_recharge_events
  USING (telco_id = current_setting('app.telco_id', true))
  WITH CHECK (telco_id = current_setting('app.telco_id', true));
GRANT SELECT, INSERT, UPDATE ON held_recharge_events TO tcp_app;
GRANT SELECT ON held_recharge_events TO tcp_worker;
GRANT SELECT ON held_recharge_events TO tcp_operator;  -- the reviewable ops queue
