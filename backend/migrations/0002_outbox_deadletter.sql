-- 0002_outbox_deadletter.sql — G0-F2 fix (reviewer gate REVIEW-1-M0).
--
-- Head-of-line starvation: events that can never publish (unregistered type,
-- max attempts reached) were re-claimed every cycle and consumed the whole
-- claim batch. Fix part 2: a dead-letter marker takes permanently-failed
-- events OUT of the claim window as explicit operator-replay backlog
-- (V2-EVT-008/009) instead of silent drag.
--
-- FIFO semantics preserved deliberately (reviewer requirement): the claim
-- query's inner NOT-EXISTS guard keeps matching dead-lettered rows
-- (published_at IS NULL), so a quarantined head STILL blocks its own
-- aggregate's successors — ordering never silently skips a financial event.
-- It only stops occupying batch slots and blocking OTHER aggregates.

ALTER TABLE outbox ADD COLUMN dead_lettered_at TIMESTAMPTZ;

-- Claim-window index now excludes dead-lettered rows.
DROP INDEX outbox_unpublished_ix;
CREATE INDEX outbox_unpublished_ix
  ON outbox (seq) WHERE published_at IS NULL AND dead_lettered_at IS NULL;

-- DLQ visibility for operations (V3 queue dashboards).
CREATE INDEX outbox_deadletter_ix
  ON outbox (dead_lettered_at) WHERE dead_lettered_at IS NOT NULL;

-- NOTE: outbox_agg_unpublished_ix (aggregate_id, seq) WHERE published_at IS NULL
-- is intentionally UNCHANGED — it serves the inner FIFO guard, which must keep
-- seeing dead-lettered rows.

-- Worker may set/clear the dead-letter marker (requeue = operator replay).
GRANT UPDATE (dead_lettered_at) ON outbox TO tcp_worker;
