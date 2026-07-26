-- 0057_m6h_recon_arm_requests.sql — Phase 1 S3-C3: the FOUR-EYES arming request
-- table (build/PHASE1_S3_DESIGN.md §S3-C). Opening the webhook money door for a
-- telco's RECOVERY layer needs a maker PROPOSAL + a DISTINCT checker APPROVAL —
-- the same two-actor discipline as completeness-override / break-resolution /
-- held-release. DISARM stays single-actor (closing a money door is the fail-safe
-- direction) and is a direct SetDown, not a request.
--
-- The approval captures the human-confirmed feed basis as a HARD precondition:
--   * confirmed_feed_reversal_basis — GROSS vs NET_SAME_DAY (residual risk #1: the
--     reversal-aware NET platform figure is safe against both, but the expected
--     break volume depends on which MTN uses; ARM cannot proceed without it).
--   * confirmed_business_date_basis — how MTN assigns a deduction to a business day
--     (the whole property rests on feed.business_date == occurred_at@Lagos date).
--
-- freshness_max_seconds is captured from the governed recon.recovery config at
-- propose time and written into the arming row on approve (the structural CHECK
-- mirrors the config floor). NOT RLS-scoped — a pre-tenant control registry like
-- recon_layer_arming, administered by the worker cross-tenant.

CREATE TABLE recon_layer_arming_requests (
  request_id  TEXT PRIMARY KEY,
  telco_id    TEXT NOT NULL REFERENCES telcos(telco_id),
  layer       TEXT NOT NULL,
  state       TEXT NOT NULL DEFAULT 'PENDING' CHECK (state IN ('PENDING','APPROVED','REJECTED')),
  proposed_by TEXT NOT NULL,
  approved_by TEXT,
  reason      TEXT NOT NULL,
  freshness_max_seconds INT NOT NULL CHECK (freshness_max_seconds BETWEEN 3600 AND 604800),
  confirmed_feed_reversal_basis TEXT NOT NULL CHECK (confirmed_feed_reversal_basis IN ('GROSS','NET_SAME_DAY')),
  confirmed_business_date_basis TEXT NOT NULL,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  decided_at  TIMESTAMPTZ,
  -- The two-person rule, schema-enforced: a checker cannot be the proposer.
  CONSTRAINT recon_arm_two_actor CHECK (approved_by IS NULL OR approved_by <> proposed_by)
);

-- At most one PENDING arm request per (telco, layer) — a second propose while one
-- is open is refused, so an approval is never ambiguous about which it decides.
CREATE UNIQUE INDEX recon_arm_requests_one_pending_uq
  ON recon_layer_arming_requests (telco_id, layer) WHERE state = 'PENDING';

-- The worker (tcp_worker) proposes/approves; it already holds INSERT/DELETE on
-- recon_layer_arming (0052) for the SetLive/SetDown the approve/disarm perform.
GRANT SELECT, INSERT, UPDATE ON recon_layer_arming_requests TO tcp_worker;
