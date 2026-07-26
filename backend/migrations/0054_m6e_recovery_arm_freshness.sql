-- 0054_m6e_recovery_arm_freshness.sql — Phase 1 S3-B: couple the RECOVERY arming
-- marker to FEED FRESHNESS so "armed" means "actively reconciling a fresh feed",
-- closing the fail-OPEN gap (build/PHASE1_S3_DESIGN.md §S3-B).
--
-- Today recon_layer_arming.live_at is stamped once at first arm and never read, so
-- a telco armed once with a DEAD feed still passes IsLayerLive — the webhook keeps
-- taking money with no live reconciliation. These columns make "live" require a
-- RECENT confirmed recon:
--   * last_recon_at NULL = armed but NOT YET live (bootstrap: not live until the
--     first confirmed recon). Advanced by the recon driver ONLY on a day whose
--     booked recovery money the independent feed confirms (the positive-confirmation
--     gate) — never on "a run merely executed".
--   * arm_freshness_max_seconds = the freshness window, DENORMALISED onto the row so
--     IsLayerLive stays a single pre-tenant index-served query (no hot-path config
--     read). A STRUCTURAL CHECK (3600..604800) means NO writer — raw seed, migration,
--     bug, or the worker's own UPDATE — can ever store an unbounded window.
-- live_at stays the immutable arm-time audit fact.

ALTER TABLE recon_layer_arming
  ADD COLUMN last_recon_at             TIMESTAMPTZ,
  ADD COLUMN arm_freshness_max_seconds INT NOT NULL DEFAULT 172800
      CHECK (arm_freshness_max_seconds BETWEEN 3600 AND 604800);

-- The recon Service runs as tcp_app (it writes RLS-scoped recon_items via a tenant
-- tx), so it is what advances freshness on a confirmed recon. Column-scoped: it can
-- touch ONLY the two freshness columns — never telco_id / layer / live_at. Arming
-- (INSERT/DELETE) stays a tcp_worker privilege (0052; the S3-C four-eyes path).
GRANT UPDATE (last_recon_at, arm_freshness_max_seconds) ON recon_layer_arming TO tcp_app;
