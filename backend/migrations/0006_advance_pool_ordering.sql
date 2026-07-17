-- 0006_advance_pool_ordering.sql — deadlock fix (EDG-002 concurrency test,
-- verified against live Postgres deadlock DETAIL).
--
-- Cycle found: winner's tx2 ConfirmUtilisation waited on the pool row held by
-- a contender's tx1 Reserve, while that contender's advances INSERT waited on
-- the winner's tx2 uncommitted one-active index entry (state UPDATE creates a
-- new row version in the partial index). Fix: tx1 now decides the one-active
-- contest (INSERT advance) BEFORE reserving the pool, so a loser never holds
-- the pool row. That requires the advance to be born pool-less.
--
-- Integrity preserved: funding_pool_id may be NULL only in the pre-reservation
-- and declined states; every state from EXPOSURE_RESERVED onward MUST have a
-- pool (CHECK below).

ALTER TABLE advances ALTER COLUMN funding_pool_id DROP NOT NULL;
ALTER TABLE advances ADD CONSTRAINT advances_pool_by_state CHECK (
  funding_pool_id IS NOT NULL
  OR state IN ('REQUESTED','VALIDATED','DECLINED')
);
