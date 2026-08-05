-- 0072 — Collections write-off reject four-eyes (review R1). Rejecting a write-off is a DECISION
-- and must be taken by a DISTINCT actor, exactly like approving one. But 0011's four-eyes CHECK
-- (write_offs_check1) gates only APPROVED/POSTED, so a maker — an ADMIN sits in both rolesets —
-- could reject their OWN request. No money moves on a reject, but require_distinct_approver was
-- only half-enforced (approve yes, reject no), and the code comment asserted a control that did
-- not exist. Close the asymmetry at the SCHEMA (un-bypassable by any future direct caller,
-- mirroring the approve guard) rather than in one call site.
--
-- Decide sets approved_by = the decider for a reject too, so a self-reject makes
-- approved_by = requested_by and trips this CHECK → 23514 → repo.ErrSelfApproval → 409, with the
-- state change rolled back. Safe to add: rejections are net-new (seed-dev never rejects; no
-- REJECTED row today violates it), so validation of existing rows passes.

ALTER TABLE write_offs
  ADD CONSTRAINT write_offs_reject_distinct_actor
  CHECK (state <> 'REJECTED' OR (approved_by IS NOT NULL AND approved_by <> requested_by));
