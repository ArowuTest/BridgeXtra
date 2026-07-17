-- 0003_admin_and_m1_config.sql — admin management surface + M1 config domains.
-- Owner directive (Jul 17): no hardcoding, no stubs — admin manages config.
-- Every M1 knob is seeded here (BUILD_PLAN §7a) with maker≠checker seeds;
-- validators live in configsvc and gate every approval/activation.

-- ---------------------------------------------------------------------------
-- Platform administrator credentials (the actor identity behind maker-checker
-- on the admin API). Hash only, never the key (V2-SEC-005). Distinct from
-- telco_api_credentials: admins are platform-scope, not tenant-scope.
-- ---------------------------------------------------------------------------
CREATE TABLE admin_credentials (
  admin_id    TEXT PRIMARY KEY,
  actor       TEXT NOT NULL UNIQUE,   -- stable identity used in maker/approver fields
  key_hash    BYTEA NOT NULL,
  status      TEXT NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE','REVOKED')),
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX admin_credentials_hash_uq ON admin_credentials (key_hash);

GRANT SELECT ON admin_credentials TO tcp_app, tcp_worker;

-- ---------------------------------------------------------------------------
-- M1 seeded config domains (V1 no-hardcoding; conservative defaults).
-- Scopes: 'global', 'telco:<id>', 'programme:<id>'. The walking skeleton runs
-- on telco SIM_NG / programme prg_sim_airtime01.
-- ---------------------------------------------------------------------------
INSERT INTO programmes (programme_id, telco_id, code, name, status)
VALUES ('prg_sim_airtime01', 'SIM_NG', 'AIRTIME01', 'Airtime Advance (simulated)', 'CERTIFICATION');

INSERT INTO config_versions
  (config_version_id, domain, scope, version_no, state, content, content_hash,
   effective_from, created_by, approved_by, reason)
VALUES
  ('cfg_seed_product_airtime_v1', 'product.airtime', 'programme:prg_sim_airtime01', 1, 'ACTIVE',
   '{"currency":"NGN","denominations_minor":[5000,10000,20000,50000],"fee_bps":1000,"fee_model":"DEDUCTED_UPFRONT","offer_expiry_minutes":1440}',
   encode(sha256('{"currency":"NGN","denominations_minor":[5000,10000,20000,50000],"fee_bps":1000,"fee_model":"DEDUCTED_UPFRONT","offer_expiry_minutes":1440}'::bytea),'hex'),
   now(), 'seed:builder', 'seed:reviewer',
   'Seeded walking-skeleton product: N50-N500 ladder, 10% upfront fee, 24h offer expiry (A-7 TEST-ONLY tiering; risk approves real ladder pre-pilot)'),

  ('cfg_seed_reservation_v1', 'advance.reservation', 'global', 1, 'ACTIVE',
   '{"reservation_ttl_minutes":30,"expired_repair":"RELEASE_WITH_AUDIT"}',
   encode(sha256('{"reservation_ttl_minutes":30,"expired_repair":"RELEASE_WITH_AUDIT"}'::bytea),'hex'),
   now(), 'seed:builder', 'seed:reviewer',
   'Seeded default (V2-ADV-003/V1-ADV-005 hook): abandoned reservations release after 30m with audit'),

  ('cfg_seed_fulfilment_v1', 'advance.fulfilment', 'telco:SIM_NG', 1, 'ACTIVE',
   '{"status_enquiry_delays_seconds":[10,30,60,300,900],"unknown_escalation_minutes":60}',
   encode(sha256('{"status_enquiry_delays_seconds":[10,30,60,300,900],"unknown_escalation_minutes":60}'::bytea),'hex'),
   now(), 'seed:builder', 'seed:reviewer',
   'Seeded default (V2-ADV-009): FULFILMENT_UNKNOWN enquiry backoff then operator escalation at 60m'),

  ('cfg_seed_allocation_v1', 'recovery.allocation', 'programme:prg_sim_airtime01', 1, 'ACTIVE',
   '{"waterfall":["FEE","PRINCIPAL"],"over_recovery":"QUARANTINE_SUSPENSE"}',
   encode(sha256('{"waterfall":["FEE","PRINCIPAL"],"over_recovery":"QUARANTINE_SUSPENSE"}'::bytea),'hex'),
   now(), 'seed:builder', 'seed:reviewer',
   'Seeded default (V2-COL-002/004, EDG-020): fee-first waterfall; excess recovery quarantined to suspense'),

  ('cfg_seed_adapter_sim_v1', 'telco.adapter', 'telco:SIM_NG', 1, 'ACTIVE',
   '{"fulfilment_url":"http://localhost:8091","request_timeout_ms":3000,"retry_budget":0,"circuit_error_threshold_pct":50,"circuit_min_requests":20}',
   encode(sha256('{"fulfilment_url":"http://localhost:8091","request_timeout_ms":3000,"retry_budget":0,"circuit_error_threshold_pct":50,"circuit_min_requests":20}'::bytea),'hex'),
   now(), 'seed:builder', 'seed:reviewer',
   'Seeded simulator adapter (A-2/A-14): retry_budget=0 — fulfilment is NEVER transport-retried (INV-009); circuit opens at 50% errors over 20 reqs'),

  ('cfg_seed_recon_v1', 'recon.tolerance', 'programme:prg_sim_airtime01', 1, 'ACTIVE',
   '{"amount_tolerance_minor":0,"auto_resolve":false,"break_aging_alert_hours":24}',
   encode(sha256('{"amount_tolerance_minor":0,"auto_resolve":false,"break_aging_alert_hours":24}'::bytea),'hex'),
   now(), 'seed:builder', 'seed:reviewer',
   'Seeded default (V2-REC-011): ZERO tolerance and no auto-resolution until finance approves otherwise — fail-closed floor');
