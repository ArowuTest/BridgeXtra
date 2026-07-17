-- 0001_core.sql — M0 foundation schema.
-- Requirements: V2-TEN-001 (immutable telco_id on tenant rows), V2-DAT-002 (integrity via
-- keys/constraints), V2-CFG-001/004/006 (config lifecycle, hash, overlap rejection),
-- V2-API-003 (idempotency outcome persistence), SF-4 (outbox per-aggregate FIFO order key),
-- SF-5 (idempotency TTL floor + non-terminal protection), V1 no-hardcoding (seeded defaults).
--
-- RLS design: ENABLE (not FORCE) row level security. Migrations run as the table owner,
-- which RLS does not apply to unless FORCEd — deliberately avoiding the FORCE-RLS
-- non-superuser-owner seed trap (reference_migrator_force_rls). The application role
-- tcp_app is never a table owner, so RLS fully applies to it. Missing tenant setting
-- resolves to NULL -> zero rows: fail closed (zero-config floor).

CREATE EXTENSION IF NOT EXISTS btree_gist;

-- ---------------------------------------------------------------------------
-- Roles (idempotent; cluster-level)
-- ---------------------------------------------------------------------------
-- Roles are cluster-level: concurrent migrations in different databases can
-- race an IF-NOT-EXISTS check (TOCTOU on pg_authid). Exception guard instead.
DO $$
BEGIN
  BEGIN
    CREATE ROLE tcp_app LOGIN PASSWORD 'devlocal_app' NOSUPERUSER NOCREATEDB NOCREATEROLE;
  EXCEPTION WHEN duplicate_object OR unique_violation THEN NULL;
  END;
  BEGIN
    -- Outbox dispatcher & cross-tenant platform jobs: BYPASSRLS, still NOSUPERUSER.
    CREATE ROLE tcp_worker LOGIN PASSWORD 'devlocal_worker' NOSUPERUSER NOCREATEDB NOCREATEROLE BYPASSRLS;
  EXCEPTION WHEN duplicate_object OR unique_violation THEN NULL;
  END;
END $$;

-- ---------------------------------------------------------------------------
-- Telcos (global registry — no RLS; access governed at usecase layer)
-- ---------------------------------------------------------------------------
CREATE TABLE telcos (
  telco_id    TEXT PRIMARY KEY,
  name        TEXT NOT NULL,
  country     CHAR(2) NOT NULL,
  status      TEXT NOT NULL DEFAULT 'INACTIVE'
              CHECK (status IN ('INACTIVE','CERTIFICATION','ACTIVE','SUSPENDED')),
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE programmes (
  programme_id  TEXT PRIMARY KEY,
  telco_id      TEXT NOT NULL REFERENCES telcos(telco_id),
  code          TEXT NOT NULL,
  name          TEXT NOT NULL,
  status        TEXT NOT NULL DEFAULT 'DRAFT'
                CHECK (status IN ('DRAFT','CERTIFICATION','ACTIVE','SUSPENDED','CLOSED')),
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (telco_id, code)
);

-- API credentials: hash only, never the key itself (V2-SEC-005).
CREATE TABLE telco_api_credentials (
  credential_id TEXT PRIMARY KEY,
  telco_id      TEXT NOT NULL REFERENCES telcos(telco_id),
  key_hash      BYTEA NOT NULL,
  label         TEXT NOT NULL,
  status        TEXT NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE','REVOKED')),
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX telco_api_credentials_hash_uq ON telco_api_credentials (key_hash);

-- ---------------------------------------------------------------------------
-- Governed configuration (V2-CFG-001..010; V1-CFG-002/003/007)
-- ---------------------------------------------------------------------------
CREATE TABLE config_versions (
  config_version_id TEXT PRIMARY KEY,
  domain          TEXT NOT NULL,
  scope           TEXT NOT NULL,  -- 'global' | 'telco:<id>' | 'programme:<id>'
  version_no      INT  NOT NULL,
  state           TEXT NOT NULL
                  CHECK (state IN ('DRAFT','SUBMITTED','APPROVED','SCHEDULED','ACTIVE','SUPERSEDED','ROLLED_BACK','REJECTED')),
  content         JSONB NOT NULL,
  content_hash    TEXT  NOT NULL,
  effective_from  TIMESTAMPTZ,
  effective_to    TIMESTAMPTZ,
  created_by      TEXT NOT NULL,
  approved_by     TEXT,
  reason          TEXT NOT NULL,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (domain, scope, version_no),
  -- maker-checker at schema level too: an APPROVED/ACTIVE row must carry a distinct approver
  CHECK (state NOT IN ('APPROVED','SCHEDULED','ACTIVE') OR (approved_by IS NOT NULL AND approved_by <> created_by))
);
-- V2-CFG-006: overlapping ACTIVE effective periods rejected structurally.
ALTER TABLE config_versions ADD CONSTRAINT config_active_no_overlap
  EXCLUDE USING gist (
    domain WITH =,
    scope  WITH =,
    tstzrange(effective_from, COALESCE(effective_to, 'infinity'::timestamptz)) WITH &&
  ) WHERE (state = 'ACTIVE');
CREATE INDEX config_lookup_ix ON config_versions (domain, scope, state, effective_from);

-- ---------------------------------------------------------------------------
-- Idempotency store (V2-API-002/003; SF-5)
-- ---------------------------------------------------------------------------
CREATE TABLE idempotency_records (
  telco_id        TEXT NOT NULL REFERENCES telcos(telco_id),
  operation       TEXT NOT NULL,
  idem_key        TEXT NOT NULL,
  request_hash    TEXT NOT NULL,
  response_status INT  NOT NULL,
  response_body   JSONB NOT NULL,
  -- SF-5: sweep may only remove records whose underlying flow is terminal.
  terminal        BOOLEAN NOT NULL DEFAULT false,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (telco_id, operation, idem_key)
);
CREATE INDEX idempotency_sweep_ix ON idempotency_records (created_at) WHERE terminal;

-- ---------------------------------------------------------------------------
-- Transactional outbox (V2-EVT-001/002; ADR-0001 SF-4 per-aggregate FIFO)
-- seq is DB-assigned: total insertion order across all writers.
-- ---------------------------------------------------------------------------
CREATE TABLE outbox (
  seq             BIGINT GENERATED ALWAYS AS IDENTITY,
  id              TEXT PRIMARY KEY,          -- ULID (event_id in the canonical envelope)
  telco_id        TEXT NOT NULL,
  aggregate_type  TEXT NOT NULL,
  aggregate_id    TEXT NOT NULL,
  event_type      TEXT NOT NULL,
  schema_version  INT  NOT NULL DEFAULT 1,
  payload         JSONB NOT NULL,
  occurred_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  published_at    TIMESTAMPTZ,
  attempts        INT NOT NULL DEFAULT 0,
  last_error      TEXT
);
CREATE INDEX outbox_unpublished_ix     ON outbox (seq) WHERE published_at IS NULL;
CREATE INDEX outbox_agg_unpublished_ix ON outbox (aggregate_id, seq) WHERE published_at IS NULL;

-- ---------------------------------------------------------------------------
-- Audit (V2-OBS-005/006 — append-only by grants: no UPDATE/DELETE granted)
-- ---------------------------------------------------------------------------
CREATE TABLE audit_events (
  id          TEXT PRIMARY KEY,
  telco_id    TEXT,               -- NULL = platform-scope event
  actor       TEXT NOT NULL,
  action      TEXT NOT NULL,
  target_type TEXT NOT NULL,
  target_id   TEXT NOT NULL,
  reason      TEXT,
  detail      JSONB NOT NULL DEFAULT '{}',
  source_ip   TEXT,
  occurred_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX audit_actor_ix  ON audit_events (actor, occurred_at);
CREATE INDEX audit_target_ix ON audit_events (target_type, target_id, occurred_at);

-- ---------------------------------------------------------------------------
-- Row-level security: tenant tables scoped by app.telco_id.
-- current_setting(..., true) returns NULL when unset -> policies match nothing:
-- missing tenant context NEVER means "all tenants" (fail closed).
-- ---------------------------------------------------------------------------
ALTER TABLE programmes          ENABLE ROW LEVEL SECURITY;
ALTER TABLE idempotency_records ENABLE ROW LEVEL SECURITY;
ALTER TABLE outbox              ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit_events        ENABLE ROW LEVEL SECURITY;

CREATE POLICY programmes_tenant ON programmes
  USING (telco_id = current_setting('app.telco_id', true))
  WITH CHECK (telco_id = current_setting('app.telco_id', true));
CREATE POLICY idempotency_tenant ON idempotency_records
  USING (telco_id = current_setting('app.telco_id', true))
  WITH CHECK (telco_id = current_setting('app.telco_id', true));
CREATE POLICY outbox_tenant ON outbox
  USING (telco_id = current_setting('app.telco_id', true))
  WITH CHECK (telco_id = current_setting('app.telco_id', true));
-- audit: a tenant session may write/read its own rows; platform-scope rows (NULL telco)
-- are writable by any authenticated session but only readable by worker/admin roles.
CREATE POLICY audit_tenant ON audit_events
  USING (telco_id = current_setting('app.telco_id', true))
  WITH CHECK (telco_id = current_setting('app.telco_id', true) OR telco_id IS NULL);

-- Grants: least privilege (V2-SEC-002 deny-by-default posture).
GRANT USAGE ON SCHEMA public TO tcp_app, tcp_worker;
GRANT SELECT ON telcos, telco_api_credentials, config_versions TO tcp_app, tcp_worker;
GRANT SELECT, INSERT, UPDATE ON programmes           TO tcp_app;
GRANT SELECT, INSERT         ON idempotency_records  TO tcp_app;
GRANT UPDATE (terminal)      ON idempotency_records  TO tcp_app;
GRANT SELECT, INSERT         ON outbox               TO tcp_app;
GRANT SELECT, INSERT         ON audit_events         TO tcp_app, tcp_worker;
-- worker: dispatches outbox across tenants (BYPASSRLS), marks published/attempts.
GRANT SELECT, UPDATE (published_at, attempts, last_error) ON outbox TO tcp_worker;
-- config writes happen through the platform-admin path (migration owner / api service
-- account); tcp_app is read-only on config by design in M0.
GRANT INSERT, UPDATE ON config_versions TO tcp_worker; -- api admin runs as worker role in M0 (single service acct); revisit at M4 RBAC

-- ---------------------------------------------------------------------------
-- Seeded defaults (V1 no-hardcoding: every threshold is a config record).
-- Seeds are versioned, ACTIVE, with maker/checker distinct.
-- ---------------------------------------------------------------------------
INSERT INTO telcos (telco_id, name, country, status)
VALUES ('SIM_NG', 'Simulated Telco (Nigeria)', 'NG', 'ACTIVE');

INSERT INTO config_versions
  (config_version_id, domain, scope, version_no, state, content, content_hash,
   effective_from, created_by, approved_by, reason)
VALUES
  ('cfg_seed_idem_ttl_v1', 'platform.idempotency', 'global', 1, 'ACTIVE',
   '{"ttl_hours": 168, "min_floor_hours": 72}',
   encode(sha256('{"ttl_hours": 168, "min_floor_hours": 72}'::bytea), 'hex'),
   now(), 'seed:builder', 'seed:reviewer',
   'Seeded default (SF-5): idempotency retention 7d, floor 72h >= longest legitimate retry window'),
  ('cfg_seed_concurrency_v1', 'product.concurrency', 'global', 1, 'ACTIVE',
   '{"max_concurrent_advances": 1}',
   encode(sha256('{"max_concurrent_advances": 1}'::bytea), 'hex'),
   now(), 'seed:builder', 'seed:reviewer',
   'Seeded default (V1-PRD-005 / DD-09 / A-8): one active advance per subscriber; SF-2 guard enforces schema consistency'),
  ('cfg_seed_outbox_v1', 'platform.outbox', 'global', 1, 'ACTIVE',
   '{"claim_batch_size": 50, "max_attempts": 10, "retry_backoff_seconds": 30}',
   encode(sha256('{"claim_batch_size": 50, "max_attempts": 10, "retry_backoff_seconds": 30}'::bytea), 'hex'),
   now(), 'seed:builder', 'seed:reviewer',
   'Seeded default: outbox dispatch tuning (V2-EVT-007 bounded retry)');
