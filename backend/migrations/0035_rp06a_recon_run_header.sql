-- 0035_rp06a_recon_run_header.sql — R-P0-6 Slice A (AUD-P0-006, REC-001..013,
-- FIN-004/005). The recon walking-skeleton wrote free-floating recon_items with
-- a generated run_id and no durable run header: no manifest, no control totals,
-- no supersession — every rerun just appended more rows. This adds an immutable
-- recon-run header that makes a run a self-verifying statement (source + platform
-- manifests with record counts, monetary control totals, and a source hash), and
-- makes reruns SUPERSEDE the prior run instead of piling up.

CREATE TABLE recon_runs (
  run_id            TEXT PRIMARY KEY,
  telco_id          TEXT NOT NULL,
  programme_id      TEXT NOT NULL,
  layer             TEXT NOT NULL CHECK (layer IN ('FULFILMENT','RECOVERY','SETTLEMENT','BUREAU')),
  -- Period the run reconciles. In Slice A this is the full-history window
  -- (period_start = epoch, period_end = run time); Slice C makes it a bounded,
  -- watermarked window. Recorded now so the manifest is complete from the start.
  period_start      TIMESTAMPTZ NOT NULL,
  period_end        TIMESTAMPTZ NOT NULL,
  -- Source manifest: exactly what telco-side set was ingested for this run.
  source_record_count          BIGINT NOT NULL CHECK (source_record_count >= 0),
  source_control_total_minor   BIGINT NOT NULL,  -- sum of source monetary values
  source_hash                  TEXT   NOT NULL,  -- sha256 over the canonical source set
  -- Platform manifest: the money-bearing platform set compared against.
  platform_record_count        BIGINT NOT NULL CHECK (platform_record_count >= 0),
  platform_control_total_minor BIGINT NOT NULL,
  -- Outcome, set once at insert (the header is a summary, never re-counted).
  matched_count     BIGINT NOT NULL DEFAULT 0 CHECK (matched_count >= 0),
  break_count       BIGINT NOT NULL DEFAULT 0 CHECK (break_count >= 0),
  -- REJECTED: a rerun whose source failed the completeness floor (empty or
  -- materially-truncated feed). It is recorded for audit but never becomes the
  -- live run — it does NOT supersede the prior ACTIVE, so a failed fetch can
  -- never wipe good reconciliation state.
  state             TEXT NOT NULL DEFAULT 'ACTIVE' CHECK (state IN ('ACTIVE','SUPERSEDED','REJECTED')),
  -- DEFERRABLE: supersession sets the prior run's superseded_by to the NEW run
  -- and then inserts that run, in one tx. The FK is validated at commit (by
  -- which point the successor exists), so the prior can be flipped to SUPERSEDED
  -- first — which is required, because the partial unique index forbids two
  -- ACTIVE rows for the scope, so the new ACTIVE row cannot be inserted until
  -- the prior one is no longer ACTIVE.
  superseded_by     TEXT REFERENCES recon_runs(run_id) DEFERRABLE INITIALLY DEFERRED,
  created_by        TEXT NOT NULL,
  created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (period_end >= period_start),
  -- A SUPERSEDED run must name its successor; an ACTIVE run must not.
  CHECK ((state = 'SUPERSEDED') = (superseded_by IS NOT NULL))
);

-- At most ONE active run per (telco, programme, layer): a rerun must supersede
-- the prior active run in the same tx, or this INSERT fails (fail-closed — the
-- framework can never leave two live reconciliations of the same scope).
CREATE UNIQUE INDEX recon_runs_active_uq
  ON recon_runs (telco_id, programme_id, layer) WHERE state = 'ACTIVE';
CREATE INDEX recon_runs_lookup_ix ON recon_runs (telco_id, programme_id, layer, created_at);

ALTER TABLE recon_runs ENABLE ROW LEVEL SECURITY;
CREATE POLICY t_recon_runs ON recon_runs
  USING (telco_id = current_setting('app.telco_id', true))
  WITH CHECK (telco_id = current_setting('app.telco_id', true));
-- Append-only header; the ONLY mutation is the supersession flip (state +
-- superseded_by). Manifests and counts are immutable once written.
GRANT SELECT, INSERT ON recon_runs TO tcp_app;
GRANT UPDATE (state, superseded_by) ON recon_runs TO tcp_app;
GRANT SELECT, INSERT ON recon_runs TO tcp_worker;
GRANT UPDATE (state, superseded_by) ON recon_runs TO tcp_worker;

-- Supersession is monotonic: a run may only go ACTIVE -> SUPERSEDED, and
-- superseded_by is write-once. Belt-and-suspenders over the column grant.
CREATE OR REPLACE FUNCTION recon_runs_supersede_once()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.state = 'SUPERSEDED' THEN
    RAISE EXCEPTION 'recon run % is already superseded (immutable)', OLD.run_id;
  END IF;
  IF NEW.state <> 'SUPERSEDED' OR NEW.superseded_by IS NULL THEN
    RAISE EXCEPTION 'a recon run may only transition ACTIVE -> SUPERSEDED with a successor';
  END IF;
  RETURN NEW;
END;
$$;
CREATE TRIGGER trg_recon_runs_supersede_once
  BEFORE UPDATE ON recon_runs
  FOR EACH ROW EXECUTE FUNCTION recon_runs_supersede_once();

-- Link recon_items to the run header. NOT VALID: enforce the reference for NEW
-- items (the framework writes the header first, in the same tx) without
-- re-validating legacy orphan run_ids from pre-framework demo runs — adding a
-- validating FK to a table with existing unreferenced rows would fail (the
-- R-P0-7 add-constraint-to-existing-data lesson).
ALTER TABLE recon_items
  ADD CONSTRAINT recon_items_run_fk FOREIGN KEY (run_id)
  REFERENCES recon_runs(run_id) NOT VALID;

-- Completeness floor (R-P0-6 Slice A): a rerun must carry at least this
-- fraction of the prior ACTIVE run's source record count to be allowed to
-- supersede it. An empty or truncated feed (0 records, or materially below the
-- prior) is REJECTED, leaving the good run ACTIVE. Governed config, not code —
-- data-driven supersede of the currently-active recon.tolerance (the
-- 0010/0033 pattern), so no version number is hardcoded.
WITH cur AS (
  SELECT config_version_id, version_no, content
  FROM config_versions
  WHERE domain = 'recon.tolerance' AND scope = 'programme:prg_sim_airtime01' AND state = 'ACTIVE'
  LIMIT 1
), closed AS (
  UPDATE config_versions c
  SET state = 'SUPERSEDED', effective_to = now()
  FROM cur WHERE c.config_version_id = cur.config_version_id
  RETURNING cur.version_no, cur.content
)
INSERT INTO config_versions
  (config_version_id, domain, scope, version_no, state, content, content_hash,
   effective_from, created_by, approved_by, reason)
SELECT
  'cfg_seed_recon_tol_completeness_v' || (version_no + 1),
  'recon.tolerance', 'programme:prg_sim_airtime01', version_no + 1, 'ACTIVE',
  content || '{"min_completeness_ratio":0.5}'::jsonb,
  encode(sha256((content || '{"min_completeness_ratio":0.5}'::jsonb)::text::bytea), 'hex'),
  now(), 'seed:builder', 'seed:reviewer',
  'R-P0-6 Slice A: recon completeness floor 0.5 — a rerun with under half the prior source count does not supersede (an empty or truncated feed cannot wipe a good reconciliation).'
FROM closed;
