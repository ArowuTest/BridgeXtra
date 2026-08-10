-- 0078_recharge_daily_reservation.sql — BX-HIGH-001.
--
-- The per-telco daily recharge ceiling had a check-then-book race: the recharge webhook
-- read the day's booked SUM (HeldRecharge.DailyIngestedMinor) in one tx, committed, then
-- booked the recovery in a SEPARATE Recovery.Ingest tx. Two concurrent webhook deliveries
-- both observed the same stale SUM, both passed the ceiling check, and both booked —
-- exceeding the ceiling. A SUM read is not a concurrency arbiter.
--
-- Replace the SUM check with an atomic per-(telco, UTC business-day) reservation counter.
-- Reserving is a single conditional UPSERT that row-locks the counter, so concurrent bookers
-- serialize and the ceiling is enforced at commit — never guessed from a stale read. Booking
-- failures and idempotent replays RELEASE the reservation (the handler compensates). Every
-- failure mode is fail-SAFE: an orphaned reservation (e.g. a crash between reserve and book)
-- over-counts, making the ceiling STRICTER, never looser. A blast-radius control must never
-- fail open. The day is the UTC calendar day, matching DailyIngestedMinor's window.
CREATE TABLE recharge_daily_reservation (
  telco_id       TEXT   NOT NULL REFERENCES telcos(telco_id),
  business_day   DATE   NOT NULL,
  reserved_minor BIGINT NOT NULL DEFAULT 0 CHECK (reserved_minor >= 0),
  PRIMARY KEY (telco_id, business_day)
);

-- Tenant-scoped like every other per-telco table: the handler reserves inside a
-- WithTenantTx (app.telco_id set), so RLS both enforces the boundary and lets the row
-- resolve. current_setting(..., true) unset -> NULL -> matches nothing (fail closed).
ALTER TABLE recharge_daily_reservation ENABLE ROW LEVEL SECURITY;
CREATE POLICY recharge_daily_reservation_tenant ON recharge_daily_reservation
  USING (telco_id = current_setting('app.telco_id', true))
  WITH CHECK (telco_id = current_setting('app.telco_id', true));

-- The app role reserves (INSERT/UPDATE) and releases (UPDATE) within the tenant tx.
GRANT SELECT, INSERT, UPDATE ON recharge_daily_reservation TO tcp_app;
