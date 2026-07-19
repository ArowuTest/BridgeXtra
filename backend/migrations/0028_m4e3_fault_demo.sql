-- 0028_m4e3_fault_demo.sql — M4e-3: the non-engineer fault demo. A demo run
-- is an IMMUTABLE pointer record: it names the scenario and the real
-- artifacts (offer, advance, correlation) the run produced by driving the
-- ORDINARY origination path against a fault-shaped simulator token. There are
-- no special-cased money paths — the artifact chain is read from the real
-- tables by correlation, and this record can never be rewritten (INSERT-only
-- grants; no UPDATE exists for any application role — EXT-3-born).

CREATE TABLE demo_runs (
  run_id         TEXT PRIMARY KEY,
  telco_id       TEXT NOT NULL,
  programme_id   TEXT NOT NULL REFERENCES programmes(programme_id),
  scenario       TEXT NOT NULL,
  msisdn_token   TEXT NOT NULL,
  offer_id       TEXT NOT NULL REFERENCES offers(offer_id),
  advance_id     TEXT NOT NULL REFERENCES advances(advance_id),
  correlation_id TEXT NOT NULL,
  requested_by   TEXT NOT NULL,
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX demo_runs_recent_ix ON demo_runs (telco_id, created_at DESC);

ALTER TABLE demo_runs ENABLE ROW LEVEL SECURITY;
CREATE POLICY t_demo_runs ON demo_runs
  USING (telco_id = current_setting('app.telco_id', true))
  WITH CHECK (telco_id = current_setting('app.telco_id', true));

GRANT SELECT, INSERT ON demo_runs TO tcp_app;
GRANT SELECT ON demo_runs TO tcp_worker;

-- Governed demo catalogue (C3 floor: consumer refuses absent/invalid config;
-- validator refuses enabled-without-telcos — armed-but-dead prevention both
-- ways). allowed telcos seeded to the SIMULATOR tenant ONLY: the handler
-- refuses any telco not in this map, so the demo structurally cannot run
-- against a real telco integration. Token pools are the deterministic
-- fault-shaped rows the simulator plants in its feature file (C6: several
-- per scenario, rotated past the one-active-advance constraint).
INSERT INTO config_versions
  (config_version_id, domain, scope, version_no, state, content, content_hash,
   effective_from, created_by, approved_by, reason)
VALUES
  ('cfg_seed_ops_fault_demo_v1', 'ops.fault_demo', 'global', 1, 'ACTIVE',
   '{"enabled":true,"telcos":{"SIM_NG":{"programme_id":"prg_sim_airtime01"}},"scenarios":{"happy_path":{"tokens":["tok_sim_demo_ok_01","tok_sim_demo_ok_02","tok_sim_demo_ok_03"],"description":"Normal airtime advance: offer, confirm, telco credits, advance ACTIVE."},"hard_fail":{"tokens":["tok_sim_demo_FAIL_01","tok_sim_demo_FAIL_02","tok_sim_demo_FAIL_03"],"description":"Telco refuses the credit: attempt FAILED, reservation released, no exposure."},"timeout_unknown":{"tokens":["tok_sim_demo_TIMEOUT_01","tok_sim_demo_TIMEOUT_02","tok_sim_demo_TIMEOUT_03"],"description":"EDG-005: the credit HAPPENS but the platform never hears back. Attempt goes UNKNOWN; the resolver chases the telco until the truth lands."}}}',
   encode(sha256('{"enabled":true,"telcos":{"SIM_NG":{"programme_id":"prg_sim_airtime01"}},"scenarios":{"happy_path":{"tokens":["tok_sim_demo_ok_01","tok_sim_demo_ok_02","tok_sim_demo_ok_03"],"description":"Normal airtime advance: offer, confirm, telco credits, advance ACTIVE."},"hard_fail":{"tokens":["tok_sim_demo_FAIL_01","tok_sim_demo_FAIL_02","tok_sim_demo_FAIL_03"],"description":"Telco refuses the credit: attempt FAILED, reservation released, no exposure."},"timeout_unknown":{"tokens":["tok_sim_demo_TIMEOUT_01","tok_sim_demo_TIMEOUT_02","tok_sim_demo_TIMEOUT_03"],"description":"EDG-005: the credit HAPPENS but the platform never hears back. Attempt goes UNKNOWN; the resolver chases the telco until the truth lands."}}}'::bytea),'hex'),
   now(), 'seed:builder', 'seed:reviewer',
   'Seeded M4e-3 fault-demo catalogue: SIM_NG only (structural sim-only guard), three scenarios over the simulator''s deterministic fault-token pools (V2-SIM-002)');
