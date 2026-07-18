-- 0017_m4a_portal_auth.sql — M4a portal session auth + RBAC foundation.
--
-- Roles are DATA on the actor (deny-by-default: an actor without a role can
-- log in to nothing); sessions are server-side rows keyed by a HASH of the
-- opaque cookie token (a database leak does not leak live sessions), with
-- absolute expiry, revocation, and a per-session CSRF secret.

ALTER TABLE admin_credentials
  ADD COLUMN role TEXT NOT NULL DEFAULT 'ADMIN'
  CHECK (role IN ('ADMIN','RISK','FINANCE','OPS','SUPPORT'));

CREATE TABLE portal_sessions (
  session_hash BYTEA PRIMARY KEY,           -- sha256 of the cookie token
  actor        TEXT NOT NULL REFERENCES admin_credentials(actor),
  role         TEXT NOT NULL,               -- snapshot at login (role changes need re-login)
  csrf_hash    BYTEA NOT NULL,              -- sha256 of the CSRF token
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at   TIMESTAMPTZ NOT NULL,
  revoked_at   TIMESTAMPTZ
);
CREATE INDEX portal_sessions_actor_ix ON portal_sessions (actor) WHERE revoked_at IS NULL;

-- Platform-scope like admin_credentials (no telco column, no RLS): the
-- portal is the operator's console, tenancy applies to the DATA it reads,
-- which stays behind the RLS'd app role as everywhere else.
GRANT SELECT, INSERT, UPDATE (revoked_at) ON portal_sessions TO tcp_app;
