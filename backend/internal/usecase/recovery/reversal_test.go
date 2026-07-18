package recovery_test

// M3b full-matrix pack: EDG-019 reversal-before-original nets exactly;
// reversal-after-close re-opens the book; over-reversal refused; EDG-021
// post-write-off recovery is income; DD-19 quarantine carries a telco-level
// balanced journal. Every path checks ledger + pool arithmetic.

import (
	"context"
	"strings"
	"testing"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/origination"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/recovery"
)

func (f *fixture) reverse(t *testing.T, reversalID, originalID string, minor int64) recovery.ReverseResult {
	t.Helper()
	out, err := f.rec.Reverse(tenantCtx(), recovery.ReverseCmd{
		ReversalSourceEventID: reversalID, OriginalSourceEventID: originalID,
		Amount: entity.MustMoney(minor, entity.NGN), CorrelationID: "cor-" + reversalID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (f *fixture) advanceRow(t *testing.T, advanceID string) (state string, outstanding int64) {
	t.Helper()
	if err := f.db.Admin.QueryRow(context.Background(),
		`SELECT state, outstanding_minor FROM advances WHERE advance_id = $1`, advanceID).
		Scan(&state, &outstanding); err != nil {
		t.Fatal(err)
	}
	return
}

// EDG-019 canonical: the reversal arrives FIRST. It parks. The original then
// arrives, allocates, and the parked reversal applies in the same
// transaction — the book nets to exactly its pre-pair state.
func TestEDG019_ReversalBeforeOriginal_NetsExactly(t *testing.T) {
	f := newFixture(t, "rev_before")
	adv := f.activeAdvance(t)

	// Reversal first: parks.
	rev := f.reverse(t, "rvsl-1", "src-orig-1", 2_000)
	if !rev.Parked {
		t.Fatalf("reversal before original must park (EDG-019): %+v", rev)
	}
	// Replay of the parked reversal is a no-op.
	if again := f.reverse(t, "rvsl-1", "src-orig-1", 2_000); !again.Parked || !again.Replayed {
		t.Fatalf("re-parking must replay: %+v", again)
	}

	// Original arrives: allocates AND the parked reversal applies with it.
	res := f.ingest(t, "src-orig-1", 2_000)
	if res.State != entity.RecoveryAllocated {
		t.Fatalf("original must allocate: %+v", res)
	}

	// Net effect: outstanding back to the full 5000, advance still open.
	state, outstanding := f.advanceRow(t, adv.AdvanceID)
	if outstanding != 5_000 || state != string(entity.AdvPartiallyRecovered) {
		t.Fatalf("pair must net to pre-pair book: state=%s outstanding=%d", state, outstanding)
	}
	// Parked row closed.
	var prState string
	if err := f.db.Admin.QueryRow(context.Background(),
		`SELECT state FROM pending_reversals WHERE original_source_event_id='src-orig-1'`).Scan(&prState); err != nil {
		t.Fatal(err)
	}
	if prState != "APPLIED" {
		t.Fatalf("parked reversal must be APPLIED, got %s", prState)
	}
	// Ledger: applied + reversed journals both exist and the book balances.
	assertBalancedBook(t, f)
	// Pool: utilisation restored to the full obligation.
	var utilised int64
	if err := f.db.Admin.QueryRow(context.Background(),
		`SELECT utilised_minor FROM funding_pools WHERE pool_id='pool_sim_01'`).Scan(&utilised); err != nil {
		t.Fatal(err)
	}
	if utilised != 5_000 {
		t.Fatalf("pool must fund the restored obligation: utilised=%d", utilised)
	}
}

// Reversal AFTER the advance closed: the book re-opens (CLOSED ->
// PARTIALLY_RECOVERED) — the controlled-reversal transition.
func TestEDG019_ReversalAfterClose_ReopensBook(t *testing.T) {
	f := newFixture(t, "rev_reopen")
	adv := f.activeAdvance(t)

	if res := f.ingest(t, "src-full-1", 5_000); !res.AdvanceClosed {
		t.Fatalf("full recovery must close: %+v", res)
	}
	rev := f.reverse(t, "rvsl-2", "src-full-1", 5_000)
	if rev.Parked || !rev.AdvanceReopened {
		t.Fatalf("reversal after close must re-open, not park: %+v", rev)
	}

	state, outstanding := f.advanceRow(t, adv.AdvanceID)
	if state != string(entity.AdvPartiallyRecovered) || outstanding != 5_000 {
		t.Fatalf("book must re-open with restored outstanding: state=%s outstanding=%d", state, outstanding)
	}
	// Fully-reversed event is visible as REVERSED.
	var evtState string
	if err := f.db.Admin.QueryRow(context.Background(),
		`SELECT state FROM recovery_events WHERE source_event_id='src-full-1'`).Scan(&evtState); err != nil {
		t.Fatal(err)
	}
	if evtState != "REVERSED" {
		t.Fatalf("fully-reversed event must be REVERSED, got %s", evtState)
	}
	assertBalancedBook(t, f)
}

// A reversal exceeding the event's net applied amount is refused loudly —
// never partially guessed.
func TestReversal_ExceedingApplied_RefusedLoudly(t *testing.T) {
	f := newFixture(t, "rev_exceed")
	f.activeAdvance(t)
	f.ingest(t, "src-part-1", 2_000)

	_, err := f.rec.Reverse(tenantCtx(), recovery.ReverseCmd{
		ReversalSourceEventID: "rvsl-3", OriginalSourceEventID: "src-part-1",
		Amount: entity.MustMoney(3_000, entity.NGN), CorrelationID: "cor-rvsl-3",
	})
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("over-reversal must refuse loudly, got %v", err)
	}
}

// EDG-021: recovery after write-off is INCOME — the advance stays
// WRITTEN_OFF, outstanding stays 0, the money lands in
// WRITEOFF_RECOVERY_INCOME with a WRITEOFF_INCOME allocation.
func TestEDG021_PostWriteoffRecovery_IsIncome(t *testing.T) {
	f := newFixture(t, "rev_wo")
	adv := f.activeAdvance(t)

	// Crystallise the loss directly (the M3c write-off usecase arrives next
	// slice; the FSM + schema paths are what this test exercises).
	ctx := context.Background()
	if _, err := f.db.Admin.Exec(ctx, `
		UPDATE advances SET state='WRITTEN_OFF', outstanding_minor=0, version=version+1, updated_at=now()
		WHERE advance_id=$1`, adv.AdvanceID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(ctx, `
		UPDATE funding_pools SET utilised_minor = utilised_minor - 5000 WHERE pool_id='pool_sim_01'`); err != nil {
		t.Fatal(err)
	}

	res := f.ingest(t, "src-po-1", 1_500)
	if res.State != entity.RecoveryAllocated || res.Applied.Amount() != 1_500 {
		t.Fatalf("post-write-off recovery must allocate as income: %+v", res)
	}

	state, outstanding := f.advanceRow(t, adv.AdvanceID)
	if state != "WRITTEN_OFF" || outstanding != 0 {
		t.Fatalf("loss must stay crystallised: state=%s outstanding=%d", state, outstanding)
	}
	var income int64
	if err := f.db.Admin.QueryRow(ctx, `
		SELECT COALESCE(SUM(credit_minor),0) FROM journal_entries WHERE account_code='WRITEOFF_RECOVERY_INCOME'`).
		Scan(&income); err != nil {
		t.Fatal(err)
	}
	if income != 1_500 {
		t.Fatalf("income must be recognised: %d", income)
	}
	var comp string
	if err := f.db.Admin.QueryRow(ctx, `
		SELECT component FROM recovery_allocations ra
		JOIN recovery_events re ON re.recovery_event_id = ra.recovery_event_id
		WHERE re.source_event_id='src-po-1'`).Scan(&comp); err != nil {
		t.Fatal(err)
	}
	if comp != "WRITEOFF_INCOME" {
		t.Fatalf("allocation component must be WRITEOFF_INCOME, got %s", comp)
	}
	assertBalancedBook(t, f)
}

// DD-19: quarantined money now carries a TELCO-LEVEL balanced journal (no
// programme) — the books say what the suspense table says.
func TestDD19_Quarantine_TelcoLevelJournal(t *testing.T) {
	f := newFixture(t, "rev_dd19")
	// Subscriber exists (seeded) but has NO advance at all.
	res := f.ingest(t, "src-q-1", 3_000)
	if res.State != entity.RecoveryQuarantined {
		t.Fatalf("no-advance event must quarantine: %+v", res)
	}

	ctx := context.Background()
	var progID *string
	var suspense int64
	if err := f.db.Admin.QueryRow(ctx, `
		SELECT j.programme_id,
		  (SELECT COALESCE(SUM(credit_minor),0) FROM journal_entries e
		    WHERE e.journal_id = j.journal_id AND e.account_code='RECOVERY_SUSPENSE')
		FROM journals j WHERE j.event_type='RECOVERY_QUARANTINED'`).Scan(&progID, &suspense); err != nil {
		t.Fatal(err)
	}
	if progID != nil {
		t.Fatalf("quarantine journal must be telco-level (NULL programme), got %v", *progID)
	}
	if suspense != 3_000 {
		t.Fatalf("suspense liability must be booked: %d", suspense)
	}
	assertBalancedBook(t, f)
}

// M3B-F1 (VR-16): a reversal whose reopen collides with the subscriber's NEW
// open advance parks with a distinct reason — never aborted into nowhere —
// and the telco's retry APPLIES once the blocker clears, draining the queue.
func TestM3BF1_ReopenCollision_ParksThenAppliesAfterBlockerClears(t *testing.T) {
	f := newFixture(t, "rev_collide")
	advA := f.activeAdvance(t)

	// A fully recovers and closes; the subscriber legally takes advance B.
	if res := f.ingest(t, "src-A-full", 5_000); !res.AdvanceClosed {
		t.Fatalf("A must close: %+v", res)
	}
	offers, err := f.orig.GetOffers(tenantCtx(), "prg_sim_airtime01", "tok_sim_0001")
	if err != nil {
		t.Fatal(err)
	}
	resB, err := f.orig.Confirm(tenantCtx(), origination.ConfirmCmd{
		ProgrammeID: "prg_sim_airtime01", OfferID: offers[0].OfferID, MSISDNToken: "tok_sim_0001",
		IdemKey: "rec-adv-B", CorrelationID: "cor-rec-B",
	})
	if err != nil {
		t.Fatal(err)
	}

	// The telco reverses a recovery on A: the reopen would collide with B's
	// one-active slot — the reversal must PARK with the collision recorded.
	rev := f.reverse(t, "rvsl-A", "src-A-full", 5_000)
	if !rev.Parked || rev.AdvanceReopened {
		t.Fatalf("collision must park, not apply: %+v", rev)
	}
	var reason, prState string
	if err := f.db.Admin.QueryRow(context.Background(), `
		SELECT park_reason, state FROM pending_reversals
		WHERE original_source_event_id='src-A-full'`).Scan(&reason, &prState); err != nil {
		t.Fatal(err)
	}
	if prState != "PARKED" || reason != "SUBSCRIBER_HAS_OPEN_ADVANCE" {
		t.Fatalf("must park with the collision as reason: state=%s reason=%s", prState, reason)
	}
	// A is untouched (still CLOSED) and B is untouched (still ACTIVE).
	if state, _ := f.advanceRow(t, advA.AdvanceID); state != "CLOSED" {
		t.Fatalf("A must stay CLOSED while parked: %s", state)
	}
	if state, _ := f.advanceRow(t, resB.Advance.AdvanceID); state != "ACTIVE" {
		t.Fatalf("B must be unaffected: %s", state)
	}

	// B closes (B took the next rung on the still-valid ladder, so recover
	// its ACTUAL outstanding); the telco retries the SAME reversal — it now
	// applies and the parked row drains from the operator queue.
	if res := f.ingest(t, "src-B-full", resB.Advance.Outstanding.Amount()); !res.AdvanceClosed {
		t.Fatalf("B must close: %+v", res)
	}
	rev2 := f.reverse(t, "rvsl-A", "src-A-full", 5_000)
	if rev2.Parked || !rev2.AdvanceReopened {
		t.Fatalf("retry after blocker clears must apply: %+v", rev2)
	}
	if err := f.db.Admin.QueryRow(context.Background(), `
		SELECT state FROM pending_reversals WHERE original_source_event_id='src-A-full'`).Scan(&prState); err != nil {
		t.Fatal(err)
	}
	if prState != "APPLIED" {
		t.Fatalf("parked row must drain to APPLIED: %s", prState)
	}
	if state, outstanding := f.advanceRow(t, advA.AdvanceID); state != string(entity.AdvPartiallyRecovered) || outstanding != 5_000 {
		t.Fatalf("A must re-open with restored outstanding: %s/%d", state, outstanding)
	}
	assertBalancedBook(t, f)
}

// M3B-F1 second collision class: the pool no longer has headroom to re-fund
// the reopened obligation — park with POOL_HEADROOM, never a raw abort.
func TestM3BF1_PoolHeadroomCollision_Parks(t *testing.T) {
	f := newFixture(t, "rev_headroom")
	f.activeAdvance(t)
	if res := f.ingest(t, "src-H-full", 5_000); !res.AdvanceClosed {
		t.Fatalf("must close: %+v", res)
	}
	// Shrink the pool so the reopen cannot be funded.
	if _, err := f.db.Admin.Exec(context.Background(),
		`UPDATE funding_pools SET committed_minor = 1000 WHERE pool_id='pool_sim_01'`); err != nil {
		t.Fatal(err)
	}

	rev := f.reverse(t, "rvsl-H", "src-H-full", 5_000)
	if !rev.Parked {
		t.Fatalf("headroom collision must park: %+v", rev)
	}
	var reason string
	if err := f.db.Admin.QueryRow(context.Background(), `
		SELECT park_reason FROM pending_reversals WHERE original_source_event_id='src-H-full'`).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason != "POOL_HEADROOM" {
		t.Fatalf("reason must be POOL_HEADROOM, got %s", reason)
	}
}

// assertBalancedBook: every journal balances per currency (INV-004 shape).
func assertBalancedBook(t *testing.T, f *fixture) {
	t.Helper()
	var unbalanced int
	if err := f.db.Admin.QueryRow(context.Background(), `
		SELECT count(*) FROM (
			SELECT journal_id FROM journal_entries GROUP BY journal_id, currency
			HAVING SUM(debit_minor) <> SUM(credit_minor)) x`).Scan(&unbalanced); err != nil {
		t.Fatal(err)
	}
	if unbalanced != 0 {
		t.Fatal("INV-004 violated: unbalanced journal in the matrix path")
	}
}
