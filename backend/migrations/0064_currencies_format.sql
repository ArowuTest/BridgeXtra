-- Money-format governance. The portal rendered every amount as raw minor units with a
-- "(minor)" suffix (e.g. "NGN 5,000 (minor)") because toMoneyView had no scale to divide
-- by. This adds the governed source: currency decimals + symbol as a seeded reference
-- table, read ONCE at boot and applied in toMoneyView, so the kobo->naira divisor is a
-- value keyed by ISO code — never a hardcoded /100 — and stays correct if a zero-decimal
-- currency is ever added. Global reference data (not tenant-scoped), same posture as
-- telcos / config_versions: SELECT granted to the app/worker roles, no RLS.

CREATE TABLE currencies (
  code       CHAR(3) PRIMARY KEY,
  decimals   SMALLINT NOT NULL CHECK (decimals >= 0 AND decimals <= 4),
  symbol     TEXT     NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- NGN is the Release 1 operating currency (ASSUMPTIONS A-13): 2 decimals (kobo), ₦.
INSERT INTO currencies (code, decimals, symbol) VALUES ('NGN', 2, '₦');

GRANT SELECT ON currencies TO tcp_app, tcp_worker;
