-- 0080_recharge_reserved_event.sql — BX-HIGH-001 crash-reservation hardening.
--
-- The atomic (telco, UTC-day) reservation counter (0078) prevents concurrent webhooks from
-- exceeding the daily ceiling — the money-safety defect is closed. But a crash between reserve
-- and Recovery.Ingest, or a cross-MAC retry of the same event, would reserve AGAIN: the counter
-- over-counts (fail-safe: the ceiling only gets stricter, never looser), but capacity is
-- consumed twice for one event.
--
-- Make reservation idempotent per source_event_id: a per-event dedup row claimed once. A retry
-- of the same source_event_id finds the existing claim and does NOT increment the counter a
-- second time; the counter increment happens exactly once per event. The counter (0078) still
-- provides the atomic ceiling; this table provides the exactly-once guarantee.
CREATE TABLE recharge_reserved_event (
  telco_id        TEXT   NOT NULL REFERENCES telcos(telco_id),
  source_event_id TEXT   NOT NULL,
  business_day    DATE   NOT NULL,
  amount_minor    BIGINT NOT NULL,
  reserved_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (telco_id, source_event_id)
);

-- Tenant-scoped like the counter: the handler reserves inside a WithTenantTx (app.telco_id set).
ALTER TABLE recharge_reserved_event ENABLE ROW LEVEL SECURITY;
CREATE POLICY recharge_reserved_event_tenant ON recharge_reserved_event
  USING (telco_id = current_setting('app.telco_id', true))
  WITH CHECK (telco_id = current_setting('app.telco_id', true));

-- The app claims (INSERT), reads back the day on a retry (SELECT), and undoes the claim on an
-- over-ceiling reject or a failed booking (DELETE) so a genuine later retry can re-reserve.
GRANT SELECT, INSERT, DELETE ON recharge_reserved_event TO tcp_app;
