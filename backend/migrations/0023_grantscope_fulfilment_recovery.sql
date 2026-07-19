-- 0023_grantscope_fulfilment_recovery.sql — EXT-3/DAP-1 continuation
-- (reviewer VR-32, task #42), M1 fulfilment/recovery cluster. Column-scope the
-- tcp_app UPDATE grants on the origination/recovery FSM tables so that the
-- recorded money algebra (offers), the telco request evidence
-- (fulfilment_attempts) and the money-in event record (recovery_events) can
-- NEVER be rewritten in place — only the lifecycle columns the code writes.
--
-- Each column set was enumerated from the PRODUCTION write paths, not the
-- schema (reviewer rule: "scope from what the code writes"). Verified via
--   grep 'UPDATE <table>' backend --include=*.go | grep -v _test.go
-- returning the exact sites cited below and no others.

-- offers — the only non-test UPDATE is the FSM transition:
--   repo/credit.go:184  UPDATE offers SET state = $3 WHERE offer_id=$1 AND state=$2
-- LOCKED: face_value_minor, fee_minor, disbursed_minor, repayment_minor,
--   currency, fee_model, decision_snapshot_id, product_config_version_id,
--   expires_at, subscriber_account_id. The offer's money algebra and pinned
--   terms are fixed at generation (offer_money_identity CHECK) and must never
--   be edited in place.
REVOKE UPDATE ON offers FROM tcp_app;
GRANT UPDATE (state) ON offers TO tcp_app;

-- fulfilment_attempts — two non-test write paths:
--   repo/credit.go Attempts.Resolve: state, telco_reference, response_evidence,
--     next_enquiry_at, resolved_at
--   repo/recovery.go enquiry tick:    enquiry_count, next_enquiry_at, state
-- LOCKED: attempt_id, advance_id, attempt_no, telco_idempotency_key,
--   request_evidence, submitted_at. The request evidence + idempotency identity
--   are the immutable audit trail of what we sent the telco.
REVOKE UPDATE ON fulfilment_attempts FROM tcp_app;
GRANT UPDATE (state, telco_reference, response_evidence, next_enquiry_at,
              resolved_at, enquiry_count)
  ON fulfilment_attempts TO tcp_app;

-- recovery_events — the only non-test UPDATE is the FSM transition:
--   repo/recovery.go:62  UPDATE recovery_events SET state=$3 WHERE ... AND state=$2
-- LOCKED: recovery_event_id, telco_id, source_event_id, subscriber_account_id,
--   amount_minor, currency, occurred_at, received_at. The recorded money-in
--   event (amount + source) is immutable; only its allocation state advances.
REVOKE UPDATE ON recovery_events FROM tcp_app;
GRANT UPDATE (state) ON recovery_events TO tcp_app;
