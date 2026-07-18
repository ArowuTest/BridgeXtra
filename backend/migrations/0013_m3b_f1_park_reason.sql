-- 0013_m3b_f1_park_reason.sql — M3B-F1 (VR-16, MEDIUM): a reversal whose
-- application collides with an existing invariant (subscriber already has a
-- new open advance blocking the reopen; pool lacks headroom to re-fund) must
-- land SOMEWHERE an operator can see — parked with a distinct reason, never
-- aborted into nowhere. The aging partial index on PARKED rows is the
-- operator queue; the reason says why it waits.
ALTER TABLE pending_reversals
  ADD COLUMN park_reason TEXT NOT NULL DEFAULT 'ORIGINAL_UNSEEN'
  CHECK (park_reason IN ('ORIGINAL_UNSEEN','ORIGINAL_NOT_ALLOCATED',
                         'SUBSCRIBER_HAS_OPEN_ADVANCE','POOL_HEADROOM'));
