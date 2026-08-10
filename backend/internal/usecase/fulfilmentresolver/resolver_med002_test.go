package fulfilmentresolver_test

// BX-MED-002 (REOPENED), resolver half. The auditor's clause: the confirm response must be
// persisted in the SAME transaction that commits the fulfilment outcome — and the resolver worker
// is the OTHER writer of economic outcomes. If only the saga were made atomic, the ordinary
// crash-between-tx1-and-tx2 path (EDG-007) would still commit an outcome with no recorded
// response, and a later retry would be answered from the advance's mutable current state.
//
// These tests drive the REAL worker entrypoint (RunOnce), not the usecase directly.

import (
	"context"
	"testing"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
)

func confirmResponseStatus(t *testing.T, db *testutil.DB, idemKey string) int {
	t.Helper()
	var st int
	if err := db.Admin.QueryRow(context.Background(),
		`SELECT COALESCE(response_status,0) FROM idempotency_records
		  WHERE operation='advance.confirm' AND idem_key=$1`, idemKey).Scan(&st); err != nil {
		t.Fatal(err)
	}
	return st
}

// When the RESOLVER settles an outcome, it must record the confirm response in the same
// transaction. Mutation proof: remove recordConfirmResponse from resolveOutcomeTx (or move it to a
// separate tx) and response_status stays 0 while the advance goes ACTIVE — RED.
func TestBXMED002_ResolverRecordsConfirmResponseWithTheOutcome(t *testing.T) {
	f := newFixture(t, "res_med002_record", 0, 2_000)
	f.seed(t, "sub_m2r", "tok_crash_m2r")

	offers, err := f.orig.GetOffers(tenantCtx(), "prg_sim_airtime01", "tok_crash_m2r")
	if err != nil {
		t.Fatal(err)
	}
	// The EDG-007 shape: tx1 committed (advance + idempotency record), the instruction landed at
	// the telco, then the process died before tx2. NOTHING has recorded a confirm response yet.
	advID := f.manualTx1(t, offers[0].Offer, "tok_crash_m2r")
	f.sim.CreditDirect(advID, 10_000, "NGN")
	if _, err := f.db.Admin.Exec(context.Background(),
		`UPDATE fulfilment_attempts SET submitted_at = now() - interval '1 hour'`); err != nil {
		t.Fatal(err)
	}
	if st := confirmResponseStatus(t, f.db, "crash-"+advID); st != 0 {
		t.Fatalf("setup: no response may be recorded before the resolver runs, got %d", st)
	}

	if n, err := f.resolver.RunOnce(context.Background(), "SIM_NG", 10); err != nil || n != 1 {
		t.Fatalf("resolver must settle the stale-SENT attempt: n=%d err=%v", n, err)
	}

	var state string
	if err := f.db.Admin.QueryRow(context.Background(),
		`SELECT state FROM advances WHERE advance_id=$1`, advID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "ACTIVE" {
		t.Fatalf("setup: resolver must settle the advance ACTIVE, got %s", state)
	}
	// THE ASSERTION: the outcome and its confirm response landed together. 201 is what a settled
	// fresh confirm earns; a replay of it later renders 200.
	if st := confirmResponseStatus(t, f.db, "crash-"+advID); st != 201 {
		t.Fatalf("BX-MED-002: the resolver must record the confirm response WITH the outcome, got response_status=%d", st)
	}
}

// The converse, on the resolver path: if the response cannot be recorded, the resolver must NOT
// commit the economic outcome. Mutation proof: swallow recordConfirmResponse's error (or wrap it in
// a savepoint so its failure does not abort the outcome — the plausible "availability fix", and
// byte-for-byte the defect the auditor reopened) and the advance settles ACTIVE with a journal
// while response_status stays 0 — RED.
func TestBXMED002_ResolverDoesNotCommitOutcomeWhenResponseCannotBeRecorded(t *testing.T) {
	f := newFixture(t, "res_med002_atomic", 0, 2_000)
	f.seed(t, "sub_m2a", "tok_crash_m2a")

	offers, err := f.orig.GetOffers(tenantCtx(), "prg_sim_airtime01", "tok_crash_m2a")
	if err != nil {
		t.Fatal(err)
	}
	advID := f.manualTx1(t, offers[0].Offer, "tok_crash_m2a")
	f.sim.CreditDirect(advID, 10_000, "NGN")
	ctx := context.Background()
	if _, err := f.db.Admin.Exec(ctx,
		`UPDATE fulfilment_attempts SET submitted_at = now() - interval '1 hour'`); err != nil {
		t.Fatal(err)
	}

	// A production failure mode: the app role loses UPDATE on idempotency_records.
	if _, err := f.db.Admin.Exec(ctx, `REVOKE UPDATE ON idempotency_records FROM tcp_app`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.db.Admin.Exec(context.Background(),
			`GRANT UPDATE (terminal, response_status, response_body) ON idempotency_records TO tcp_app`)
	})

	// RunOnce must not settle anything: the per-attempt work aborts and stays claimable.
	n, _ := f.resolver.RunOnce(ctx, "SIM_NG", 10)
	if n != 0 {
		t.Errorf("BX-MED-002: the resolver must not report a settled outcome when its response cannot be recorded, got n=%d", n)
	}

	var state string
	var journals int
	var utilised int64
	if err := f.db.Admin.QueryRow(ctx, `
		SELECT (SELECT state FROM advances WHERE advance_id=$1),
		       (SELECT count(*) FROM journals),
		       (SELECT utilised_minor FROM funding_pools WHERE pool_id='pool_sim_01')`,
		advID).Scan(&state, &journals, &utilised); err != nil {
		t.Fatal(err)
	}
	if state == "ACTIVE" {
		t.Error("BX-MED-002: the outcome must NOT commit when its confirm response cannot be recorded")
	}
	if journals != 0 {
		t.Errorf("no journal may be posted when the outcome rolls back, got %d", journals)
	}
	if utilised != 0 {
		t.Errorf("no utilisation may be booked when the outcome rolls back, got %d", utilised)
	}
	if st := confirmResponseStatus(t, f.db, "crash-"+advID); st != 0 {
		t.Errorf("no response may be recorded, got %d", st)
	}

	// And it CONVERGES: restore the grant and the very next sweep settles it atomically.
	if _, err := f.db.Admin.Exec(ctx,
		`GRANT UPDATE (terminal, response_status, response_body) ON idempotency_records TO tcp_app`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(ctx,
		`UPDATE fulfilment_attempts SET submitted_at = now() - interval '1 hour', next_enquiry_at = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if n, _ := f.resolver.RunOnce(ctx, "SIM_NG", 10); n == 1 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if st := confirmResponseStatus(t, f.db, "crash-"+advID); st == 0 {
		t.Error("after the fault clears, the resolver must settle the outcome AND record its response (no permanent stranding)")
	}
}
