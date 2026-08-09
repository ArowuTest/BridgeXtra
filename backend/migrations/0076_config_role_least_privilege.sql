-- 0076_config_role_least_privilege.sql — BX-HIGH-012 Part B, Stage 1 of the
-- control-plane / data-plane split.
--
-- Problem: the request-serving API process (cmd/api) held a tcp_worker pool —
-- BYPASSRLS locally, DB owner on managed Postgres (Render). It used it for THREE
-- things only: config reads, config writes, and the programme->telco resolver
-- (OperatorReader.Resolve). None of those needs to bypass RLS on the tenant money
-- tables, yet holding a BYPASSRLS/owner credential in the internet-facing process
-- means any compromise of that process (RCE, SSRF-to-DB, dependency compromise)
-- can read and write EVERY tenant's money, ledger and subscriber data at once,
-- defeating the entire RLS tenant-isolation boundary. That is the catastrophic
-- failure mode HIGH-012 flags for a multi-tenant financial platform.
--
-- Fix (Stage 1): introduce a dedicated, NON-BYPASSRLS tcp_config role scoped to
-- EXACTLY those three operations, and repoint cmd/api off tcp_worker onto it. The
-- API then holds no credential that can bypass a tenant policy. Its only
-- cross-tenant reach is the narrow, SELECT-only, two-column programmes policy
-- below — the programme->telco mapping metadata, never money data.
--
-- (Stage 2, separately: split cmd/api into a public data-plane deployment carrying
-- only tcp_app + tcp_operator, and an internal control-plane deployment carrying
-- tcp_config, so config-write and the resolver are not even reachable from the
-- public surface. This migration is the role foundation both stages build on.)
--
-- Managed-Postgres note: unlike tcp_worker, tcp_config never needs BYPASSRLS, so it
-- is created identically on local and managed clusters — no owner-fallback path,
-- and the resolver works via the policy rather than an owner connection. This also
-- removes the API's dependency on an owner-level connection on Render.

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'tcp_config') THEN
    -- NON-BYPASSRLS, NOSUPERUSER, owns no tables -> RLS fully applies. This role
    -- can never bypass a tenant policy; its ONLY cross-tenant reach is the
    -- SELECT-only programmes resolver policy below.
    CREATE ROLE tcp_config LOGIN PASSWORD 'devlocal_config' NOSUPERUSER NOCREATEDB NOCREATEROLE;
  END IF;
END $$;

GRANT USAGE ON SCHEMA public TO tcp_config;

-- Config governance. config_versions is a GLOBAL table (no RLS) governed by the
-- maker-checker usecase (configsvc): the role reads and writes versions, reads
-- telcos for the adapter-realism validator (BX-HIGH-009 telcos.is_synthetic), and
-- writes platform-scope config audit rows. Those audit rows carry telco_id NULL
-- (config is not tenant-scoped), which the audit_tenant WITH CHECK admits
-- (... OR telco_id IS NULL) even though RLS now applies to this non-BYPASSRLS role.
GRANT SELECT, INSERT, UPDATE ON config_versions TO tcp_config;
GRANT SELECT                 ON telcos          TO tcp_config;
GRANT SELECT, INSERT         ON audit_events    TO tcp_config;

-- Programme->telco resolver (OperatorReader.Resolve) — the ONLY reason the API ever
-- needed a bypass credential. Replace the bypass with a tightly-bounded policy:
--   * FOR SELECT only        -> tcp_config can never mutate programmes.
--   * TO tcp_config          -> applies to no other role; tcp_app / tcp_operator
--                               keep their tenant-scoped programmes_tenant policy,
--                               unaffected (permissive policies are OR-ed per role).
--   * column grant (below)   -> tcp_config can read ONLY (programme_id, telco_id),
--                               never programme codes / config / status.
-- USING(true) because the resolver runs at operator-session bootstrap BEFORE the
-- tenant is known — its whole job is to discover which telco owns a programme, so
-- it cannot itself be tenant-scoped. Net exposure is therefore capped to the
-- programme->telco mapping: the SAME metadata tcp_worker read under 0045, but now
-- select-only, two-column, single-role, and fully RLS-subject on every other table.
GRANT SELECT (programme_id, telco_id) ON programmes TO tcp_config;
CREATE POLICY programmes_config_resolve ON programmes
  FOR SELECT TO tcp_config
  USING (true);
