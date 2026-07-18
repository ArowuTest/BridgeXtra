-- 0019_ext3_config_immutability.sql — EXT-3 / DAP-1: config integrity in depth.
--
-- config_versions had a table-wide UPDATE grant to tcp_worker (the M0
-- "revisit at M4 RBAC" note). That let the config service — or any bug or
-- injection reaching it — rewrite the CONTENT, hash, domain, scope, version
-- or maker of an already-created version. A governed, decision-pinned,
-- append-only config record must not be silently mutable. Two independent
-- layers now enforce that the immutable fields never change after creation:
--
--   1. Least-privilege GRANT: tcp_worker may UPDATE only the lifecycle
--      columns (state/approver/effective window/updated_at) — the column
--      discipline already used for tcp_worker on advances/funding_pools.
--   2. Trigger backstop: even the table owner (migrations, superuser) cannot
--      change content/content_hash/domain/scope/version_no/created_by via
--      UPDATE — the record's identity and payload are write-once.

-- Layer 1: column-scoped UPDATE (INSERT grant is retained).
REVOKE UPDATE ON config_versions FROM tcp_worker;
GRANT UPDATE (state, approved_by, effective_from, effective_to, updated_at)
  ON config_versions TO tcp_worker;

-- Layer 2: immutability trigger on the identity + payload columns.
CREATE OR REPLACE FUNCTION config_versions_forbid_immutable_change()
RETURNS trigger AS $$
BEGIN
  IF NEW.content      IS DISTINCT FROM OLD.content
  OR NEW.content_hash IS DISTINCT FROM OLD.content_hash
  OR NEW.domain       IS DISTINCT FROM OLD.domain
  OR NEW.scope        IS DISTINCT FROM OLD.scope
  OR NEW.version_no   IS DISTINCT FROM OLD.version_no
  OR NEW.created_by   IS DISTINCT FROM OLD.created_by THEN
    RAISE EXCEPTION 'config_versions: content/content_hash/domain/scope/version_no/created_by are immutable after creation (EXT-3)';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER config_versions_immutable
  BEFORE UPDATE ON config_versions
  FOR EACH ROW EXECUTE FUNCTION config_versions_forbid_immutable_change();
