-- 0040_rp06d3_completeness_override.sql — R-P0-6 Slice D3 (VR-48 forward note).
-- The completeness floor (min_completeness_ratio) correctly REJECTS a re-reconcile
-- whose windowed source shrank below the floor — the protection against an empty
-- or truncated feed wiping a good run. But it cannot tell a truncated feed from a
-- LEGITIMATELY low-volume window (records correctly voided/retracted by the telco).
-- Such a window is stuck: the floor keeps rejecting the re-reconcile, so the good
-- earlier statement stays live and the true (smaller) state is never reconciled.
--
-- This adds a two-actor "accept-anyway" override, schema-enforced four-eyes like
-- the guardrail re-arm (0014): a maker proposes an override for the specific
-- rejected window, a DISTINCT checker approves, and the next re-reconcile of that
-- window consumes the override to supersede despite the floor. The override is
-- tightly scoped so it can never become a standing floor bypass:
--   * authorized_source_count — the re-reconcile may consume it only if its source
--     count is >= the low count the two actors actually reviewed (a WORSE shrink
--     is a different feed and needs its own review);
--   * baseline_active_run_id — it authorizes superseding EXACTLY the ACTIVE run the
--     actors reviewed; if the window was re-reconciled in the meantime the override
--     is stale and will not consume;
--   * single-use — consuming it flips it to CONSUMED, naming the run that used it.

CREATE TABLE recon_completeness_overrides (
  override_id             TEXT PRIMARY KEY,
  telco_id                TEXT NOT NULL,
  programme_id            TEXT NOT NULL,
  layer                   TEXT NOT NULL CHECK (layer IN ('FULFILMENT','RECOVERY','SETTLEMENT','BUREAU')),
  period_start            TIMESTAMPTZ NOT NULL,
  period_end              TIMESTAMPTZ NOT NULL,
  -- The REJECTED run whose low volume the two actors reviewed, and the ACTIVE run
  -- the authorized re-reconcile is allowed to supersede.
  rejected_run_id         TEXT NOT NULL REFERENCES recon_runs(run_id),
  baseline_active_run_id  TEXT NOT NULL REFERENCES recon_runs(run_id),
  -- The source record count the actors accepted; a re-reconcile may consume this
  -- override only if its windowed source count is >= this value.
  authorized_source_count BIGINT NOT NULL CHECK (authorized_source_count >= 0),
  state                   TEXT NOT NULL DEFAULT 'PENDING'
                          CHECK (state IN ('PENDING','APPROVED','CONSUMED','DECLINED')),
  proposed_by             TEXT NOT NULL,
  reason                  TEXT NOT NULL,
  approved_by             TEXT,
  approved_at             TIMESTAMPTZ,
  consumed_by_run_id      TEXT REFERENCES recon_runs(run_id),
  consumed_at             TIMESTAMPTZ,
  created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
  -- Four-eyes: an approved override must name a checker DISTINCT from the maker.
  -- SQLSTATE 23514 on violation maps to ErrSelfApproveOverride in the usecase.
  CHECK (approved_by IS NULL OR approved_by <> proposed_by),
  -- An approved or consumed override must carry its approver; a consumed one must
  -- name the run that used it. Keeps the record self-consistent.
  CHECK (state IN ('PENDING','DECLINED') OR approved_by IS NOT NULL),
  CHECK (state <> 'CONSUMED' OR (consumed_by_run_id IS NOT NULL AND consumed_at IS NOT NULL))
);

-- At most ONE live (PENDING or APPROVED) override per window: two stacked
-- overrides for the same period can never coexist (fail-closed).
CREATE UNIQUE INDEX recon_completeness_overrides_live_uq
  ON recon_completeness_overrides (telco_id, programme_id, layer, period_start)
  WHERE state IN ('PENDING','APPROVED');
CREATE INDEX recon_completeness_overrides_lookup_ix
  ON recon_completeness_overrides (telco_id, programme_id, layer, period_start, state);

ALTER TABLE recon_completeness_overrides ENABLE ROW LEVEL SECURITY;
CREATE POLICY t_recon_completeness_overrides ON recon_completeness_overrides
  USING (telco_id = current_setting('app.telco_id', true))
  WITH CHECK (telco_id = current_setting('app.telco_id', true));
-- Column-scoped UPDATE (the FSM-grant-lockdown discipline — no broad UPDATE): the
-- only mutations are approve (state/approved_by/approved_at), consume
-- (state/consumed_by_run_id/consumed_at), and decline (state).
GRANT SELECT, INSERT ON recon_completeness_overrides TO tcp_app;
GRANT UPDATE (state, approved_by, approved_at, consumed_by_run_id, consumed_at)
  ON recon_completeness_overrides TO tcp_app;
GRANT SELECT ON recon_completeness_overrides TO tcp_worker;
