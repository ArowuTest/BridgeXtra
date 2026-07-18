-- 0018_m4b_operator_scope.sql — M4b: operator authorization SCOPE.
--
-- Reviewer M4A-F3: design the telco/programme scope dimension into the portal
-- BEFORE any tenant-scoped screen ships — a column now, not a retrofit onto
-- live money screens (V2-UI-001 / TEN-006; the property G4 attacks with a
-- cross-scope fetch through the portal).
--
-- Scope grammar: '*' = ALL scopes (platform admin); otherwise a single config
-- scope this operator governs, matching the config_versions.scope vocabulary
-- ('global', 'programme:<id>', 'telco:<CODE>'). Authorization rule, enforced
-- in the handler:
--   READ : '*'  OR  record.scope = 'global'  OR  session.scope = record.scope
--   WRITE: '*'  OR  session.scope = record.scope     (writing 'global' needs '*')
-- Global config is a shared platform default every operator may read; writing
-- it, or touching another tenant's scope, requires the matching grant.
--
-- Seeded/existing credentials default to '*' (platform admins) — no operator
-- silently loses access on migrate; scoped operators are provisioned explicitly.

ALTER TABLE admin_credentials
  ADD COLUMN scope TEXT NOT NULL DEFAULT '*'
  CHECK (scope = '*' OR scope ~ '^(global|programme:[A-Za-z0-9_]+|telco:[A-Za-z0-9_]+)$');

-- Session carries a scope SNAPSHOT taken at login, same discipline as role
-- (a scope change requires re-login; status is re-checked live — see 0017 +
-- the Resolve join added for M4A-F1).
ALTER TABLE portal_sessions
  ADD COLUMN scope TEXT NOT NULL DEFAULT '*';
