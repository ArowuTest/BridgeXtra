-- 0025_grantscope_append_only.sql — EXT-3/DAP-1 final (reviewer VR-32, task #42
-- close-out). The last two broadly-granted FSM tables, subscriber_accounts and
-- bureau_export_batches, are append-only in production TODAY:
--   subscriber_accounts   — written only via INSERT ... ON CONFLICT DO NOTHING
--                           (features.go:145, :223); no UPDATE anywhere.
--   bureau_export_batches — written only via plain INSERT ... VALUES
--                           (ops.go); no ON CONFLICT, no UPDATE anywhere. The
--                           bureau pipe is dormant (state CHECK allows only
--                           'STAGED'; 'SENT' arrives with licensing).
--
-- Reviewer ruling (verified at source): REVOKE UPDATE on both now. A grant
-- authorises what the code does today, not a roadmap. If/when a status
-- lifecycle lands (consent-withdrawal on accounts; bureau SENT), that feature's
-- own migration adds a column-scoped GRANT UPDATE(status) co-located with the
-- code and its test — and the first test exercising the write fails loud in dev
-- with a clear permission error, forcing the grant to be scoped at the moment
-- the column is actually written. That is the discipline working, not a trap.
--
-- After this, zero table-level UPDATE grants remain to tcp_app except
-- portal_sessions' already-scoped (revoked_at) grant: a symmetric,
-- fully-enumerated immutability posture (17 tables ever granted table-level
-- UPDATE -> 15 column-scoped/frozen -> 2 revoked here -> 0 broad remaining).

REVOKE UPDATE ON subscriber_accounts FROM tcp_app;
REVOKE UPDATE ON bureau_export_batches FROM tcp_app;
