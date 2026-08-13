-- 0085_med004a2_fenced_publisher.sql — BX-MED-004-A2: schema for the fenced freshness publisher.
--
-- Design: C:\Users\sanus\bridgextra-audit-plans\design_MED-004-A2_v3.md (survived 3 rounds of
-- adversarial critique on the architecture; implementation mechanics finished directly against real
-- Postgres per the round-3 pivot decision — see project memory).
--
-- Threat model (design doc §T, restated): this does NOT defend against a tcp_app role that has
-- achieved arbitrary DML capability (SQL injection) — no schema design can, since recon_runs itself
-- is written by that same role. What it DOES defend: a bug or an unintended code path within tcp_app's
-- own application logic advancing the recharge-webhook money gate without genuine, re-verified,
-- authenticated evidence. tcp_app already holds broad write reach across this platform's money-critical
-- tables (funding_pools/advances/recovery_events/offers UPDATE, journals/journal_entries INSERT,
-- held_recharge_events status UPDATE — migration 0004) — carving out "unforgeable by tcp_app" for this
-- one gate in isolation is not an achievable or meaningful property; making the gate's ONLY legitimate
-- advance-path structurally narrow, auditable, and independently re-verified against the CURRENT
-- governed threshold at publish time is.

-- ---------------------------------------------------------------------------------------------------
-- 1. recon_runs gains the money figure it doesn't persist today. Populate-only: NULL on every
--    pre-A2 historical row is fine (nothing ever compares across rows; the fenced publisher only ever
--    reads the row for the run_id its own qualification names).
-- ---------------------------------------------------------------------------------------------------
ALTER TABLE recon_runs ADD COLUMN matched_control_total_minor BIGINT;

-- ---------------------------------------------------------------------------------------------------
-- 2. recon_recovery_qualifications — a THIN POINTER, not a second source of truth. It carries no
--    money figures at all: the fenced publisher reads matched/platform totals from recon_runs itself
--    (the same append-only, structurally-protected row the reconciliation engine wrote), never from a
--    copy here. A forged qualification pointing at a real run_id gains nothing — the publisher
--    recomputes the confirmation decision from recon_runs' own numbers, not from anything this table
--    claims. The audit-only columns record what was ACTUALLY used at qualification time for later
--    dispute/audit reconstruction; the publisher's own decision always re-resolves the CURRENT governed
--    config independently (never trusts these columns as inputs to that decision).
-- ---------------------------------------------------------------------------------------------------
CREATE TABLE recon_recovery_qualifications (
  qualification_id                  TEXT PRIMARY KEY,
  run_id                             TEXT NOT NULL REFERENCES recon_runs(run_id),
  telco_id                           TEXT NOT NULL,
  layer                              TEXT NOT NULL CHECK (layer = 'RECOVERY'),
  min_confirmation_ratio_used        NUMERIC NOT NULL,
  arm_freshness_max_seconds_used     INT NOT NULL,
  recon_recovery_config_version_id   TEXT NOT NULL,
  created_at                         TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (run_id)
);
ALTER TABLE recon_recovery_qualifications ENABLE ROW LEVEL SECURITY;
CREATE POLICY t_recon_recovery_qualifications ON recon_recovery_qualifications
  USING (telco_id = current_setting('app.telco_id', true))
  WITH CHECK (telco_id = current_setting('app.telco_id', true));
-- Written by RunRecoveryControl (tcp_app pool, same role that already writes recon_runs). Structural
-- narrowness comes from the Go-level fence (writeQualification is unexported and the sole call site is
-- proven to be RunRecoveryControl's post-dayConfirmed-true branch), not from the DB grant — the grant
-- necessarily stays as broad as recon_runs' own INSERT grant, for the same reason recon_runs' does.
GRANT SELECT, INSERT ON recon_recovery_qualifications TO tcp_app;
GRANT SELECT, INSERT ON recon_recovery_qualifications TO tcp_worker;
-- No UPDATE/DELETE grant to ANY role. Append-only, immutable evidence, same posture as recon_runs.

-- ---------------------------------------------------------------------------------------------------
-- 3. tcp_freshness — the new, deliberately minimal publisher role. NON-BYPASSRLS, NOSUPERUSER, owns no
--    tables. Its entire authority: read evidence, read the CURRENT governed recon.recovery threshold,
--    publish freshness. Nothing else — not INSERT anywhere, not access to any other config domain.
-- ---------------------------------------------------------------------------------------------------
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'tcp_freshness') THEN
    CREATE ROLE tcp_freshness LOGIN PASSWORD 'devlocal_freshness' NOSUPERUSER NOCREATEDB NOCREATEROLE;
  END IF;
END $$;

GRANT USAGE ON SCHEMA public TO tcp_freshness;
GRANT SELECT ON recon_runs                       TO tcp_freshness;
GRANT SELECT ON recon_recovery_qualifications     TO tcp_freshness;
GRANT SELECT, UPDATE (last_recon_at, arm_freshness_max_seconds) ON recon_layer_arming TO tcp_freshness;

-- Scoped config access via a security-barrier view, not a table-wide grant. config_versions spans
-- >=10 unrelated governed domains (recon.tolerance, treasury.guardrails, telco.recharge_feed,
-- telco.adapter, origination.self_exclusion, origination.nin_gate, scoring.policy, scoring.schedule,
-- ledger.accounts, ledger.templates, product.airtime, product.concurrency, telco.recovery_feed,
-- recon.recovery — verified by grep across all migrations) — tcp_freshness needs exactly one.
-- config_versions carries no RLS anywhere (verified), so a security_barrier view is sufficient scoping
-- with no RLS/view interaction to reason about. GRANT SELECT on the view requires no grant on the
-- underlying table (verified empirically against a disposable Postgres instance).
CREATE VIEW config_versions_recon_recovery WITH (security_barrier) AS
  SELECT * FROM config_versions WHERE domain = 'recon.recovery';
GRANT SELECT ON config_versions_recon_recovery TO tcp_freshness;

-- Rotation: tcp_freshness's password MUST come from TCP_FRESHNESS_PASSWORD in production (V2-SEC-005 —
-- see backend/internal/platform/dbroles/dbroles.go's envPasswords map, updated in the same commit as
-- this migration). The literal above is a local-dev default only, exactly like every other role here.
