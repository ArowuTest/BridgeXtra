-- 0009_m2_decision_replay_pins.sql — columns that make BC-4 replay a pure
-- recomputation: the full canonical decision document, the prior tier the
-- one-tier-up rule saw, and the exact scoring clock. Without prior_tier_code
-- a later run would destroy the input the original decision depended on —
-- replay must never depend on "current" state.

ALTER TABLE decision_snapshots
  ADD COLUMN decision_doc    JSONB,        -- canonical engine output (bit-exact)
  ADD COLUMN prior_tier_code TEXT,         -- '' = no prior at scoring time
  ADD COLUMN scored_at       TIMESTAMPTZ;  -- the engine clock input

-- Scored decisions must carry the replay pins (seeds exempt, as in 0007).
ALTER TABLE decision_snapshots ADD CONSTRAINT decision_replay_pins_ck CHECK (
  tier_code = 'SEED'
  OR (decision_doc IS NOT NULL AND prior_tier_code IS NOT NULL AND scored_at IS NOT NULL)
);

-- 0004 required max_face_value_minor > 0 — correct while every decision was
-- an eligible seed. Scored INELIGIBLE decisions are decisions too (auditable,
-- replayable) and carry face 0; zero is permitted ONLY when the canonical
-- document itself says ineligible.
ALTER TABLE decision_snapshots DROP CONSTRAINT decision_snapshots_max_face_value_minor_check;
ALTER TABLE decision_snapshots ADD CONSTRAINT decision_face_value_ck CHECK (
  max_face_value_minor > 0
  OR (decision_doc IS NOT NULL AND (decision_doc->>'eligible') = 'false')
);
