-- 0041_rp06e1_break_resolution_maker_checker.sql — R-P0-6 Slice E1.
-- The recon.tolerance auto_resolve=false floor means a break is NEVER
-- force-matched or auto-cleared — it must be resolved by a human. Until now a
-- break was cleared by a SINGLE operator's RESOLVE action. Slice E wires the
-- floor into a two-actor (maker-checker) resolution: a maker PROPOSES a
-- resolution, a DISTINCT checker APPROVES it, and only then does the break
-- clear. Four-eyes is schema-enforced (like the guardrail re-arm 0014 and the
-- completeness override 0040).

ALTER TABLE recon_items
  ADD COLUMN resolution_proposed_by     TEXT,
  ADD COLUMN resolution_proposed_reason TEXT,
  ADD COLUMN resolution_proposed_at     TIMESTAMPTZ,
  ADD COLUMN resolved_by                TEXT;

-- A break may be cleared (resolved_at set) ONLY via a complete two-actor
-- decision: a proposer AND a distinct approver. This is the auto_resolve=false
-- floor made structural — no single actor, and no code path, can clear a break
-- alone. NOT VALID grandfathers pre-Slice-E resolved breaks (which have no
-- proposer) exactly like the R-P0-7 add-constraint-to-existing-data lesson;
-- new resolutions are fully enforced.
ALTER TABLE recon_items
  ADD CONSTRAINT recon_items_two_actor_resolution
  CHECK (
    resolved_at IS NULL OR (
      resolution_proposed_by IS NOT NULL
      AND resolved_by IS NOT NULL
      AND resolved_by <> resolution_proposed_by
    )
  ) NOT VALID;

-- Column-scoped UPDATE grant for the new maker-checker columns (the FSM-grant
-- lockdown discipline — extends 0016's assigned_to/resolved_at/resolution).
GRANT UPDATE (resolution_proposed_by, resolution_proposed_reason, resolution_proposed_at, resolved_by)
  ON recon_items TO tcp_app;

-- The append-only break action log gains the two-actor verbs.
ALTER TABLE recon_break_actions DROP CONSTRAINT recon_break_actions_action_check;
ALTER TABLE recon_break_actions ADD CONSTRAINT recon_break_actions_action_check
  CHECK (action IN ('ASSIGN','RESOLVE','ESCALATE','NOTE','PROPOSE_RESOLVE','APPROVE_RESOLVE'));
