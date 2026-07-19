-- 0022_grantscope_money_core.sql — EXT-3/DAP-1 continuation (reviewer VR-32),
-- money-core cluster. Column-scope the tcp_app UPDATE grants on the two most
-- sensitive FSM tables so that amounts and identity can NEVER be rewritten in
-- place — only the lifecycle columns the code actually writes.
--
-- Each column set below was enumerated from the PRODUCTION write paths (not the
-- schema): the reviewer's rule is "scope from what the code writes."

-- advances — every non-test UPDATE:
--   repo/credit.go ReserveTransition: state, funding_pool_id, version, updated_at
--   repo/credit.go Transition:        state, version, updated_at, activated_at, closed_at
--   repo/recovery.go ApplyRecovery:   outstanding_minor, state, version, updated_at, closed_at
--     (also the write-off crystallisation, via ApplyRecovery -> WRITTEN_OFF)
--   repo/reversals.go reopen:         state, outstanding_minor, version, closed_at, updated_at
--   repo/writeoffs.go Classify:       delinquency_bucket, bucket_as_of
-- LOCKED: advance_id, telco_id, programme_id, subscriber_account_id, offer_id,
--   idempotency_key, correlation_id, face_value_minor, fee_minor,
--   disbursed_minor, currency, accepted_at.
REVOKE UPDATE ON advances FROM tcp_app;
GRANT UPDATE (state, funding_pool_id, version, updated_at, activated_at,
              closed_at, outstanding_minor, delinquency_bucket, bucket_as_of)
  ON advances TO tcp_app;

-- funding_pools — every non-test UPDATE touches only reserved/utilised:
--   repo/credit.go reserve/utilise/release, repo/reversals.go re-add.
-- LOCKED: pool_id, programme_id, telco_id, currency, committed_minor (the
--   committed capital base — changing it changes lending capacity and must be
--   a governed operation, never a raw UPDATE). This matches the tcp_worker
--   column scope already in place (0004).
REVOKE UPDATE ON funding_pools FROM tcp_app;
GRANT UPDATE (reserved_minor, utilised_minor) ON funding_pools TO tcp_app;
