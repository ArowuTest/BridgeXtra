-- 0082_worker_reads_idempotency_for_inv020.sql — BX-MED-002 (reopened).
--
-- INV-020 is the standing, class-level control behind the reopened MED-002 finding: an advance with
-- a COMMITTED economic outcome must always have its exact confirm response recorded (origination
-- now commits both in ONE transaction). Expressing that as an invariant means the checker has to
-- join advances against idempotency_records.
--
-- The invariant checker runs on tcp_worker (BYPASSRLS, SELECT-only paths — see
-- backend/internal/invariants and cmd/worker), and tcp_worker had NO grant on idempotency_records
-- at all: 0001_core.sql granted it to tcp_app only. So the sweep failed with "permission denied"
-- rather than reporting violations — an armed-but-dead control.
--
-- Grant SELECT only. The worker must never write idempotency records: they are claimed and filled
-- in by the application role inside the business transaction (PutIfAbsent / SetResponseIfAbsent),
-- and migration 0029's write-once trigger keeps the first recorded response immutable. Read-only
-- here preserves that boundary while making the control actually able to run.

GRANT SELECT ON idempotency_records TO tcp_worker;
