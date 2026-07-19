-- 0024_grantscope_decision_ops.sql — EXT-3/DAP-1 continuation (reviewer VR-32,
-- task #42), decision/engagement/ops cluster. Column-scope the tcp_app UPDATE
-- grants on the remaining FSM tables with clear, single-purpose write paths so
-- recorded identity/evidence can never be rewritten in place — only the
-- lifecycle columns the code writes.
--
-- Each column set enumerated from the PRODUCTION write paths (reviewer rule:
-- "scope from what the code writes"). Exhaustive per-table site sweep:
--   grep 'UPDATE <table>\b' backend --include=*.go | grep -v _test.go
-- returned exactly the sites cited below (feature_files 1, scoring_runs 2,
-- subscriber_flags 1, notifications 2, pending_reversals 2, complaints 1,
-- programmes 2) and no others.
--
-- NOTE: two remaining broadly-granted tables — subscriber_accounts and
-- bureau_export_batches — have ZERO production UPDATE paths (INSERT ... ON
-- CONFLICT DO NOTHING only). Whether they should be REVOKEd outright or gain a
-- future status lifecycle (consent-withdrawal; the currently-dormant bureau
-- pipe) is a design decision, not a code fact, so per method rule #4 they are
-- deliberately left untouched here and flagged to the reviewer rather than
-- guessed. They are the last two of the 13.

-- feature_files — features.go:54 ingest finalise: row_count, quarantined_rows,
--   status. LOCKED: feature_file_id, telco_id, source, as_of, content_hash,
--   received_at (the ingested-file identity + quality evidence).
REVOKE UPDATE ON feature_files FROM tcp_app;
GRANT UPDATE (row_count, quarantined_rows, status) ON feature_files TO tcp_app;

-- scoring_runs — scoringruns.go:62 progress tick + :77 finalise:
--   subjects_scored, subjects_skipped, status, completed_at. LOCKED:
--   scoring_run_id, telco_id, programme_id, feature_file_id, policy_version_id,
--   subjects_total, started_at (the run's pinned inputs + identity).
REVOKE UPDATE ON scoring_runs FROM tcp_app;
GRANT UPDATE (subjects_scored, subjects_skipped, status, completed_at)
  ON scoring_runs TO tcp_app;

-- subscriber_flags — engagement.go:68 close a flag: effective_to only (bitemporal
--   close). LOCKED: flag_id, telco_id, subscriber_account_id, flag, source,
--   effective_from, created_at — a flag's assertion is immutable; you close it,
--   you never rewrite what/when it asserted.
REVOKE UPDATE ON subscriber_flags FROM tcp_app;
GRANT UPDATE (effective_to) ON subscriber_flags TO tcp_app;

-- notifications — engagement.go:176 mark SENT + :189 mark FAILED: state,
--   provider_ref, sent_at. LOCKED: notification_id, telco_id,
--   subscriber_account_id, advance_id, kind, template_version, rendered_hash,
--   created_at (what was sent + to whom is immutable evidence).
REVOKE UPDATE ON notifications FROM tcp_app;
GRANT UPDATE (state, provider_ref, sent_at) ON notifications TO tcp_app;

-- pending_reversals — reversals.go:58 set park reason + :89 apply: park_reason,
--   state, applied_at. LOCKED: pending_reversal_id, telco_id,
--   original_source_event_id, reversal_source_event_id, amount_minor, currency,
--   received_at (the reversal money-event identity is immutable).
REVOKE UPDATE ON pending_reversals FROM tcp_app;
GRANT UPDATE (park_reason, state, applied_at) ON pending_reversals TO tcp_app;

-- complaints — ops.go:134 resolve: state, resolution, resolved_at. LOCKED:
--   complaint_id, telco_id, subscriber_account_id, advance_id, channel,
--   category, narrative, opened_at (the raised complaint's content is immutable;
--   only its resolution advances).
REVOKE UPDATE ON complaints FROM tcp_app;
GRANT UPDATE (state, resolution, resolved_at) ON complaints TO tcp_app;

-- programmes — guardrails.go:123 (guardrail suspend) + telcos.go:130 (admin
--   lifecycle): status only. LOCKED: programme_id, telco_id, code, name,
--   created_at (a programme's identity + code are immutable; only its
--   activation status changes).
REVOKE UPDATE ON programmes FROM tcp_app;
GRANT UPDATE (status) ON programmes TO tcp_app;
