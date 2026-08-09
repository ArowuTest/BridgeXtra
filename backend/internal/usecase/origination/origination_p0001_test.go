package origination_test

// BX-P0-001: the confirm saga must bind EVERY gate to the offer's programme.
// The suspension kill-switch (GetStatus), economics/lender-of-record
// (resolveEconomics) and the channel disclosure are resolved from the
// caller-supplied cmd.ProgrammeID; the advance/pool/fee/guardrail from
// offer.ProgrammeID. Without an equality guard a caller could name an ACTIVE
// programme to slip past a SUSPENDED offer-programme's kill-switch and book money
// on the suspended programme (and record the wrong lender-of-record).

import (
	"context"
	"errors"
	"testing"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/origination"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/treasury"
)

// seedActiveProgrammeB provisions a SECOND, fully-configured ACTIVE programme by
// cloning programme A's ACTIVE programme.economics + disclosure.policy (byte-
// identical content, so both pass origination's re-validation) re-scoped to B. B
// needs no funding pool — the money is bound to the offer's programme (A), never B.
func seedActiveProgrammeB(t *testing.T, f *fixture) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.db.Admin.Exec(ctx, `
		INSERT INTO programmes (programme_id, telco_id, code, name, status)
		VALUES ('prg_sim_b', 'SIM_NG', 'AIRTIMEB', 'Programme B (test)', 'ACTIVE')`); err != nil {
		t.Fatalf("seed programme B: %v", err)
	}
	if _, err := f.db.Admin.Exec(ctx, `
		INSERT INTO config_versions
		  (config_version_id, domain, scope, version_no, state, content, content_hash,
		   effective_from, created_by, approved_by, reason)
		SELECT domain || ':prg_sim_b', domain, 'programme:prg_sim_b', version_no, 'ACTIVE',
		       content, content_hash, effective_from, created_by, approved_by, reason
		FROM config_versions
		WHERE scope = 'programme:prg_sim_airtime01' AND state = 'ACTIVE'
		  AND domain IN ('programme.economics', 'disclosure.policy')`); err != nil {
		t.Fatalf("clone economics+disclosure for programme B: %v", err)
	}
}

func TestBXP0001_ConfirmProgrammeBinding_NoSuspendBypass(t *testing.T) {
	f := newFixture(t, "orig_p0001", 0, 2_000)
	ctx := context.Background()
	seedActiveProgrammeB(t, f)

	// An offer under programme A, created while A is still originatable.
	f.seedSubscriber(t, "sub_p1", "tok_p1", 50_000)
	offer := f.offersFor(t, "tok_p1")[0]

	// Suspend A. A confirm naming A's OWN programme is now refused by the
	// kill-switch — the control itself is intact.
	if _, err := f.db.Admin.Exec(ctx,
		`UPDATE programmes SET status = 'SUSPENDED' WHERE programme_id = 'prg_sim_airtime01'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Confirm(tenantCtx(), acceptFor(offer, "tok_p1", "p1-match", "cor-match")); !errors.Is(err, treasury.ErrProgrammeSuspended) {
		t.Fatalf("a confirm naming the offer's own (suspended) programme must be refused by the kill-switch, got %v", err)
	}

	// The attack: name the ACTIVE programme B while booking programme A's offer.
	// GetStatus(B) is ACTIVE and would pass the suspend gate — the equality guard
	// is the only thing that refuses it. (Mutation proof: delete the
	// `offer.ProgrammeID != cmd.ProgrammeID` guard in Confirm and this call books
	// an advance on the SUSPENDED programme A.)
	bypass := acceptFor(offer, "tok_p1", "p1-bypass", "cor-bypass")
	bypass.ProgrammeID = "prg_sim_b"
	if _, err := f.svc.Confirm(tenantCtx(), bypass); !errors.Is(err, origination.ErrProgrammeMismatch) {
		t.Fatalf("a confirm naming a DIFFERENT programme than the offer must be refused (ErrProgrammeMismatch), got %v", err)
	}

	// No advance booked on the suspended programme, and no idempotency record left
	// behind for the rejected bypass — the whole tx rolled back.
	var advances int
	if err := f.db.Admin.QueryRow(ctx, `
		SELECT count(*) FROM advances a JOIN subscriber_accounts s USING (subscriber_account_id)
		WHERE s.msisdn_token = 'tok_p1'`).Scan(&advances); err != nil {
		t.Fatal(err)
	}
	if advances != 0 {
		t.Fatalf("a programme-mismatch / suspend-bypass must book NO advance, got %d", advances)
	}
	var idem int
	if err := f.db.Admin.QueryRow(ctx,
		`SELECT count(*) FROM idempotency_records WHERE idem_key = 'p1-bypass'`).Scan(&idem); err != nil {
		t.Fatal(err)
	}
	if idem != 0 {
		t.Fatalf("a rejected mismatch must leave no idempotency record (tx rollback), got %d", idem)
	}

	// A stays suspended: the bypass changed nothing.
	var status string
	if err := f.db.Admin.QueryRow(ctx,
		`SELECT status FROM programmes WHERE programme_id = 'prg_sim_airtime01'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "SUSPENDED" {
		t.Fatalf("programme A must remain SUSPENDED, got %s", status)
	}
}
