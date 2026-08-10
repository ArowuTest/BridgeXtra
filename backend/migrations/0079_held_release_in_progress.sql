-- 0079_held_release_in_progress.sql — BX-HIGH-002.
--
-- ApproveRelease booked the recovery (Recovery.Ingest) and THEN CAS-flipped the hold
-- HELD -> RELEASED. A concurrent Reject (HELD -> REJECTED) could win between the booking
-- and the flip, leaving money BOOKED while the control record read REJECTED. Detection
-- after the money moved is not prevention.
--
-- Introduce an intermediate RELEASE_IN_PROGRESS state so ApproveRelease can atomically CLAIM
-- the hold BEFORE it ingests. A Reject requires HELD, so once a hold is claimed a reject can
-- no longer win — money is never booked against a hold that ends REJECTED. RELEASE_IN_PROGRESS
-- is a one-way street to RELEASED (a crash between claim and finalise is completed by a
-- retried approval, which replays the idempotent ingest); it can never revert to HELD (which
-- would re-open the reject window) or jump to REJECTED.
ALTER TABLE held_recharge_events DROP CONSTRAINT held_recharge_events_status_check;
ALTER TABLE held_recharge_events ADD CONSTRAINT held_recharge_events_status_check
  CHECK (status IN ('HELD','RELEASE_IN_PROGRESS','RELEASED','REJECTED'));

-- Extend the HIGH-014 immutability trigger: RELEASED/REJECTED stay frozen (unchanged), and a
-- RELEASE_IN_PROGRESS hold may only finalise to RELEASED. Everything else is exactly as 0074.
CREATE OR REPLACE FUNCTION held_recharge_immutable()
RETURNS trigger AS $$
BEGIN
  IF OLD.status IN ('RELEASED','REJECTED') THEN
    RAISE EXCEPTION 'held_recharge_events: a %-state hold is immutable (BX-HIGH-014)', OLD.status;
  END IF;
  -- BX-HIGH-002: a claimed hold can only move forward to RELEASED — never back to HELD (which
  -- would re-open the approve-vs-reject race) and never to REJECTED (the money is being booked).
  IF OLD.status = 'RELEASE_IN_PROGRESS' AND NEW.status NOT IN ('RELEASE_IN_PROGRESS','RELEASED') THEN
    RAISE EXCEPTION 'held_recharge_events: a RELEASE_IN_PROGRESS hold may only finalise to RELEASED (BX-HIGH-002)';
  END IF;
  IF NEW.held_id         IS DISTINCT FROM OLD.held_id
  OR NEW.telco_id        IS DISTINCT FROM OLD.telco_id
  OR NEW.source_event_id IS DISTINCT FROM OLD.source_event_id
  OR NEW.msisdn_token    IS DISTINCT FROM OLD.msisdn_token
  OR NEW.amount_minor    IS DISTINCT FROM OLD.amount_minor
  OR NEW.currency        IS DISTINCT FROM OLD.currency
  OR NEW.occurred_at     IS DISTINCT FROM OLD.occurred_at
  OR NEW.reason          IS DISTINCT FROM OLD.reason
  OR NEW.held_at         IS DISTINCT FROM OLD.held_at THEN
    RAISE EXCEPTION 'held_recharge_events: identity/amount/event evidence are immutable (BX-HIGH-014)';
  END IF;
  IF OLD.requested_by IS NOT NULL AND NEW.requested_by IS DISTINCT FROM OLD.requested_by THEN
    RAISE EXCEPTION 'held_recharge_events: requested_by is immutable once set (BX-HIGH-014)';
  END IF;
  -- Four-eyes AT THE DB: a release approval requires a request that already exists in a prior
  -- committed state, so requester and approver can never be set in one UPDATE.
  IF NEW.approved_by IS NOT NULL AND OLD.requested_by IS NULL THEN
    RAISE EXCEPTION 'held_recharge_events: a release approval requires a prior distinct request — four-eyes (BX-HIGH-014)';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
