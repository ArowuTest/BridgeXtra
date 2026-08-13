package recon

// BX-MED-004-A2 reviewer finding F3 (registered alongside F1/F2, fixed in this follow-up):
// currentRecoveryCfg/recoveryConfigContentAt resolve "currently effective" using
// statement_timestamp(), not now() (== transaction_timestamp(), pinned to the instant Publish's
// transaction began). now() would keep judging effective_from/effective_to against that stale
// instant for the entire transaction, including this specific SELECT — which runs only after
// Step 1's evidence-binding round trip has already elapsed. A governed threshold that tightens
// and commits in that window would be invisible to a now()-based predicate, letting Publish act
// against an already-superseded, looser threshold.
//
// This does not touch F1: qualificationCreatedAt (the value actually stamped into
// last_recon_at) is unaffected by this fix — it is read once, in Step 1, straight from
// recon_recovery_qualifications.created_at, never any live clock. Delayed publication still
// stamps the qualification's own true, old creation time; it can only ever shrink how much
// arming window that evidence grants, never make it look fresher than it is.

import (
	"context"
	"testing"
)

// TestMED004A2_F3_ConfigResolutionSeesPolicyTightenedAfterTransactionBegan is the behavioral
// proof: open a transaction on the SAME pool/role Publish uses (pinning its now() to this
// instant), tighten the governed recon.recovery threshold on a SEPARATE connection so it commits
// strictly after that BEGIN, then resolve the config INSIDE the still-open transaction and assert
// it sees the tightened value — proving the resolution uses the instant of ITS OWN statement, not
// the transaction's start.
//
// Mutation-verified by hand (matching this session's established practice): reverting
// recoveryConfigContentAt's predicate back to now() makes this exact test fail (it would still
// observe the loose 0.10 threshold, since transaction-start predates the tightening) — confirmed,
// then restored byte-for-byte before this test was committed.
func TestMED004A2_F3_ConfigResolutionSeesPolicyTightenedAfterTransactionBegan(t *testing.T) {
	f := newRecoveryFixture(t, "a2_f3_statement_time")
	ctx := context.Background()

	// Loose threshold, active before anything below opens a transaction.
	med004a2ActivateRecoveryConfig(t, f, "telco:SIM_NG", 0.10, 172800)

	// Open the SAME kind of transaction Publish opens (tcp_freshness pool) and hold it — this
	// pins its now()/transaction_timestamp() to this instant.
	tx, err := f.db.Freshness.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	if _, err := tx.Exec(ctx, `SELECT set_config('app.telco_id', $1, true)`, "SIM_NG"); err != nil {
		t.Fatalf("set tenant context: %v", err)
	}

	pub := &FencedControlPublisher{}
	before, _, err := pub.currentRecoveryCfg(ctx, tx, "SIM_NG")
	if err != nil {
		t.Fatalf("currentRecoveryCfg (before): %v", err)
	}
	if before != 0.10 {
		t.Fatalf("precondition: expected the loose 0.10 threshold before tightening, got %v", before)
	}

	// Tighten on a SEPARATE connection while tx is still open — this commits strictly after tx's
	// own BEGIN, but strictly before the second read below.
	med004a2ActivateRecoveryConfig(t, f, "telco:SIM_NG", 0.99, 172800)

	// Sanity check: a genuinely fresh connection sees the tightened value immediately — proves
	// the activation really did commit before we read again inside tx.
	outside := &FencedControlPublisher{}
	outsideTx, err := f.db.Freshness.Begin(ctx)
	if err != nil {
		t.Fatalf("begin (outside check): %v", err)
	}
	if _, err := outsideTx.Exec(ctx, `SELECT set_config('app.telco_id', $1, true)`, "SIM_NG"); err != nil {
		t.Fatalf("set tenant context (outside check): %v", err)
	}
	sanity, _, err := outside.currentRecoveryCfg(ctx, outsideTx, "SIM_NG")
	if err != nil {
		t.Fatalf("currentRecoveryCfg (sanity): %v", err)
	}
	_ = outsideTx.Rollback(ctx)
	if sanity != 0.99 {
		t.Fatalf("precondition: a fresh transaction must already see the tightened 0.99 threshold, got %v", sanity)
	}

	// The actual claim: the SAME still-open tx (begun before the tightening committed) must ALSO
	// see 0.99, not the 0.10 it saw a moment ago — proving this SELECT uses its own statement
	// time, not the transaction's start time.
	after, _, err := pub.currentRecoveryCfg(ctx, tx, "SIM_NG")
	if err != nil {
		t.Fatalf("currentRecoveryCfg (after): %v", err)
	}
	if after != 0.99 {
		t.Fatalf("currentRecoveryCfg inside the still-open, already-begun transaction = %v, want the "+
			"tightened 0.99 that committed WHILE this transaction was open — a transaction-start-pinned "+
			"clock would incorrectly still return the stale 0.10 threshold (BX-MED-004-A2 finding F3)", after)
	}
}
