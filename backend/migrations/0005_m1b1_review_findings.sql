-- 0005_m1b1_review_findings.sql — folds reviewer interim findings
-- (build/reviews/REVIEW-2-M1b1-interim.md) M1B-F2/F3/F4.

-- M1B-F2: divergent-duplicate detection. A journal stores the content hash of
-- its lines; a "duplicate" posting whose lines differ is a LOUD typed error
-- (amount drift = bug), never a silent return of the original.
-- DEFAULT '' covers any pre-0005 rows (legacy rows skip the divergence check).
ALTER TABLE journals ADD COLUMN lines_hash TEXT NOT NULL DEFAULT '';

-- M1B-F3: offer money-identity CHECK exhaustive over BOTH fee models
-- (the previous constraint pinned only DEDUCTED_UPFRONT).
ALTER TABLE offers DROP CONSTRAINT offer_money_identity;
ALTER TABLE offers ADD CONSTRAINT offer_money_identity CHECK (
  (fee_model = 'DEDUCTED_UPFRONT'
     AND face_value_minor = disbursed_minor + fee_minor
     AND repayment_minor  = face_value_minor)
  OR
  (fee_model = 'ADDED_TO_REPAYMENT'
     AND disbursed_minor  = face_value_minor
     AND repayment_minor  = face_value_minor + fee_minor)
);

-- M1B-F4: an offer births at most one advance — structural, not conventional.
CREATE UNIQUE INDEX advances_offer_uq ON advances (offer_id);
