package origination_test

// BX-MED-002 (REOPENED): exact confirm replay must be CRASH-SAFE.
//
// The first remediation wrote the confirm-response snapshot best-effort, in a SEPARATE transaction
// AFTER the outcome had already committed. That left the original defect intact in one window:
//
//	tx2 commits the economic outcome -> process/DB failure before the response write -> retry
//	-> the reply is built from the advance's MUTABLE CURRENT state.
//
// The fix makes the outcome and its idempotent response commit in ONE transaction, and removes the
// fallback entirely: after an outcome has committed, a replay is answered from the persisted
// response or REFUSED — never from the row. These tests prove both halves, on both writers of
// economic outcomes (the saga here; the resolver worker in the fulfilmentresolver package).

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/invariants"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/mno"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/origination"
)

// faultAdapter wraps the real MNO client and runs a hook in the tx1/tx2 window — the exact instant
// the auditor's crash scenario occupies: the instruction has been sent (the telco may already have
// credited the customer) and no transaction of ours is open.
type faultAdapter struct {
	inner  mno.Client
	before func()
}

func (f faultAdapter) SubmitFulfilment(ctx context.Context, telcoID, key string, req mno.FulfilmentRequest) (mno.Result, error) {
	res, err := f.inner.SubmitFulfilment(ctx, telcoID, key, req)
	if f.before != nil {
		f.before() // after the send, before tx2 — the crash window
	}
	return res, err
}

func (f faultAdapter) EnquireStatus(ctx context.Context, telcoID, platformRequestID string) (mno.Result, error) {
	return f.inner.EnquireStatus(ctx, telcoID, platformRequestID)
}

// blockConfirmResponseWrite makes the confirm-response write fail the way a real fault would: the
// application role loses UPDATE on idempotency_records. Preferred over a bespoke trigger because it
// IS a production failure mode (a bad grant / role drift), and it is per-test-database, so it
// cannot leak. Returns a restore func.
func blockConfirmResponseWrite(t *testing.T, db *testutil.DB) func() {
	t.Helper()
	ctx := context.Background()
	if _, err := db.Admin.Exec(ctx, `REVOKE UPDATE ON idempotency_records FROM tcp_app`); err != nil {
		t.Fatal(err)
	}
	restore := func() {
		if _, err := db.Admin.Exec(ctx,
			`GRANT UPDATE (terminal, response_status, response_body) ON idempotency_records TO tcp_app`); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(restore)
	return restore
}

func advanceFacts(t *testing.T, f *fixture, advID string) (state string, journals int, reserved, utilised int64, respStatus int) {
	t.Helper()
	if err := f.db.Admin.QueryRow(context.Background(), `
		SELECT (SELECT state FROM advances WHERE advance_id=$1),
		       (SELECT count(*) FROM journals),
		       (SELECT reserved_minor FROM funding_pools WHERE pool_id='pool_sim_01'),
		       (SELECT utilised_minor FROM funding_pools WHERE pool_id='pool_sim_01'),
		       (SELECT COALESCE(MAX(response_status),0) FROM idempotency_records WHERE operation='advance.confirm')`,
		advID).Scan(&state, &journals, &reserved, &utilised, &respStatus); err != nil {
		t.Fatal(err)
	}
	return
}

// THE CORE PROOF. If the confirm response cannot be recorded, the ECONOMIC OUTCOME MUST NOT COMMIT.
// There is therefore no reachable state in which an outcome is durable but its response is lost —
// which is the state the auditor's scenario requires, and the state the old best-effort write
// produced routinely.
//
// Mutation proof: restore the old shape (persist best-effort in a separate tx after
// ResolveOutcome, or swallow recordConfirmResponse's error) and the advance commits ACTIVE with a
// journal while response_status stays 0 — every assertion below goes RED.
func TestBXMED002_OutcomeDoesNotCommitWhenItsResponseCannotBeRecorded(t *testing.T) {
	f := newFixture(t, "med002_atomic", 0, 2_000)
	offers := f.offersFor(t, "tok_sim_0001")
	cmd := acceptFor(offers[0], "tok_sim_0001", "med002-atomic", "cor-med002-atomic")

	// Fail the response write in the tx1/tx2 window, after the telco has been instructed.
	f.svc.Adapter = faultAdapter{inner: f.svc.Adapter, before: func() { blockConfirmResponseWrite(t, f.db) }}

	res, err := f.svc.Confirm(tenantCtx(), cmd)
	if err == nil {
		t.Fatalf("BX-MED-002: confirm must FAIL when the outcome's response cannot be recorded, got %+v", res)
	}

	var advID string
	if e := f.db.Admin.QueryRow(context.Background(), `SELECT advance_id FROM advances`).Scan(&advID); e != nil {
		t.Fatal(e)
	}
	state, journals, reserved, utilised, respStatus := advanceFacts(t, f, advID)

	// The outcome did NOT commit: no settled state, no journal, no utilisation. The reservation is
	// still held (exposure is not silently released), and the attempt stays claimable by the
	// resolver — so this converges rather than stranding.
	if state != string(entity.AdvPendingFulfilment) {
		t.Errorf("advance must stay PENDING_FULFILMENT when the outcome rolls back, got %s", state)
	}
	if journals != 0 {
		t.Errorf("BX-MED-002: no journal may be posted when the confirm response cannot be recorded, got %d", journals)
	}
	if utilised != 0 {
		t.Errorf("no utilisation may be booked when the outcome rolls back, got %d", utilised)
	}
	if reserved == 0 {
		t.Error("the reservation must still be held (exposure is not silently released)")
	}
	if respStatus != 0 {
		t.Errorf("no response may be recorded, got %d", respStatus)
	}
}

// After an outcome has committed, a replay whose recorded response is missing must REFUSE — never
// answer from the advance's current row. This is the auditor's "no fallback to mutable current
// state" clause, tested at the PRIMARY replay site (the tx1 idempotency-claim replay).
//
// Mutation proof: restore `return s.advances.GetByIdemKey(...)` as the fallback in
// replayConfirmAdvance and the retry succeeds with the live ACTIVE advance — RED.
func TestBXMED002_PostOutcomeReplayRefusesInsteadOfReturningMutableState(t *testing.T) {
	f := newFixture(t, "med002_refuse", 0, 2_000)
	offers := f.offersFor(t, "tok_sim_0001")
	cmd := acceptFor(offers[0], "tok_sim_0001", "med002-refuse", "cor-med002-refuse")

	r1, err := f.svc.Confirm(tenantCtx(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	if r1.Advance.State != entity.AdvActive {
		t.Fatalf("setup: expected ACTIVE, got %s", r1.Advance.State)
	}

	// Simulate the pre-fix damage: the outcome is committed but its response was lost. (The
	// write-once trigger must be stepped around as owner — proof that no application role can
	// reach this state, only deliberate surgery or the old best-effort code path.)
	ctx := context.Background()
	for _, s := range []string{
		`ALTER TABLE idempotency_records DISABLE TRIGGER idempotency_response_write_once`,
		`UPDATE idempotency_records SET response_status = 0 WHERE operation='advance.confirm'`,
		`ALTER TABLE idempotency_records ENABLE TRIGGER idempotency_response_write_once`,
	} {
		if _, err := f.db.Admin.Exec(ctx, s); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := f.svc.Confirm(tenantCtx(), cmd); !errors.Is(err, origination.ErrConfirmReplayUnavailable) {
		t.Fatalf("BX-MED-002: a post-outcome replay with no recorded response must REFUSE (ErrConfirmReplayUnavailable), got %v", err)
	}
}

// ResolveOutcome must keep returning the LIVE post-transition advance. Persisting the confirm
// snapshot and returning the outcome are DECOUPLED: a plausible refactor that returns the decoded
// snapshot instead would report FULFILMENT_UNKNOWN for a row that is now ACTIVE, with a zeroed
// pool id and version — silently corrupting every caller of the outcome function.
func TestBXMED002_ResolveOutcomeReturnsLiveAdvanceNotThePersistedSnapshot(t *testing.T) {
	f := newFixture(t, "med002_decoupled", 500*time.Millisecond, 100)
	f.seedSubscriber(t, "sub_m2d", "tok_TIMEOUT_m2d", 50_000)
	offers := f.offersFor(t, "tok_TIMEOUT_m2d")
	cmd := acceptFor(offers[0], "tok_TIMEOUT_m2d", "med002-decoupled", "cor-med002-dec")

	r1, err := f.svc.Confirm(tenantCtx(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	if r1.Advance.State != entity.AdvFulfilmentUnknown || r1.HTTPStatus != 202 {
		t.Fatalf("setup: want UNKNOWN/202, got %s/%d", r1.Advance.State, r1.HTTPStatus)
	}

	var attemptID string
	if err := f.db.Admin.QueryRow(context.Background(),
		`SELECT attempt_id FROM fulfilment_attempts WHERE advance_id=$1`, r1.Advance.AdvanceID).Scan(&attemptID); err != nil {
		t.Fatal(err)
	}
	settled, err := f.svc.ResolveOutcome(tenantCtx(), r1.Advance.AdvanceID, attemptID,
		mno.Result{Outcome: mno.OutcomeConfirmed, TelcoReference: "tref-m2d"})
	if err != nil {
		t.Fatal(err)
	}
	// The LIVE advance, fully populated — not the snapshot (which says UNKNOWN and carries no
	// pool id or version).
	if settled.State != entity.AdvActive {
		t.Fatalf("ResolveOutcome must return the LIVE settled advance (ACTIVE), got %s", settled.State)
	}
	if settled.FundingPoolID == "" || settled.Version == 0 {
		t.Fatalf("ResolveOutcome must return the full live row, got pool=%q version=%d — this is the decoded snapshot, not the advance",
			settled.FundingPoolID, settled.Version)
	}
	// ...while the replay still reproduces the ORIGINAL response.
	r2, err := f.svc.Confirm(tenantCtx(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	if r2.Advance.State != entity.AdvFulfilmentUnknown || r2.HTTPStatus != 202 {
		t.Fatalf("replay must reproduce the ORIGINAL response (UNKNOWN/202), got %s/%d", r2.Advance.State, r2.HTTPStatus)
	}
}

// Status semantics across a CLASS boundary: an UNKNOWN confirm earned 202 (poll me). If the
// resolver later settles it FAILED, the replay must STILL be 202 — that is what the caller's
// command earned. Re-deriving from live state would answer 422, telling the channel its request was
// rejected when in truth it was accepted-and-pending at the time.
func TestBXMED002_ReplayKeepsOriginalStatusClassWhenOutcomeLaterFails(t *testing.T) {
	f := newFixture(t, "med002_class", 500*time.Millisecond, 100)
	f.seedSubscriber(t, "sub_m2c", "tok_TIMEOUT_m2c", 50_000)
	offers := f.offersFor(t, "tok_TIMEOUT_m2c")
	cmd := acceptFor(offers[0], "tok_TIMEOUT_m2c", "med002-class", "cor-med002-class")

	r1, err := f.svc.Confirm(tenantCtx(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	if r1.HTTPStatus != 202 {
		t.Fatalf("setup: want 202, got %d", r1.HTTPStatus)
	}
	var attemptID string
	if err := f.db.Admin.QueryRow(context.Background(),
		`SELECT attempt_id FROM fulfilment_attempts WHERE advance_id=$1`, r1.Advance.AdvanceID).Scan(&attemptID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.ResolveOutcome(tenantCtx(), r1.Advance.AdvanceID, attemptID,
		mno.Result{Outcome: mno.OutcomeNotFound}); err != nil {
		t.Fatal(err)
	}

	r2, err := f.svc.Confirm(tenantCtx(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	if r2.HTTPStatus != 202 {
		t.Fatalf("BX-MED-002: replay must carry the ORIGINAL status class 202, got %d (re-derived from the now-FAILED row?)", r2.HTTPStatus)
	}
	if r2.Advance.State != entity.AdvFulfilmentUnknown {
		t.Fatalf("replay must reproduce the original UNKNOWN body, got %s", r2.Advance.State)
	}
}

// INV-020 is the CLASS-LEVEL control behind BX-MED-002: a per-request read guard only converts a
// future atomicity bug into a customer-facing refusal; the standing invariant makes "outcome
// committed => confirm response recorded" checkable across every writer and every process. It runs
// in CI, on `worker -invariants`, and in the daily control cycle.
func TestBXMED002_INV020_FiresWhenAnOutcomeHasNoRecordedResponse(t *testing.T) {
	f := newFixture(t, "med002_inv020", 0, 2_000)
	offers := f.offersFor(t, "tok_sim_0001")
	cmd := acceptFor(offers[0], "tok_sim_0001", "med002-inv020", "cor-med002-inv020")

	if _, err := f.svc.Confirm(tenantCtx(), cmd); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	check := func() []invariants.Violation {
		v, err := (&invariants.Checker{Pool: f.db.Worker}).Check(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	// Healthy: outcome and response committed together.
	for _, v := range check() {
		t.Errorf("no invariant may be violated after a clean confirm: %s", v)
	}

	// Break the atomicity invariant the way the OLD best-effort write did (outcome durable,
	// response lost) — stepping around the write-once trigger as owner, which is itself proof that
	// no application role can produce this state.
	for _, s := range []string{
		`ALTER TABLE idempotency_records DISABLE TRIGGER idempotency_response_write_once`,
		`UPDATE idempotency_records SET response_status = 0 WHERE operation='advance.confirm'`,
		`ALTER TABLE idempotency_records ENABLE TRIGGER idempotency_response_write_once`,
	} {
		if _, err := f.db.Admin.Exec(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	var fired bool
	for _, v := range check() {
		if v.Invariant == "INV-020" {
			fired = true
		}
	}
	if !fired {
		t.Fatal("INV-020 must fire when an advance's outcome is committed with no recorded confirm response (BX-MED-002)")
	}
}
