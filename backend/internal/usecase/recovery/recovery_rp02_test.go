package recovery_test

// R-P0-2 adversarial pack: recovery ingest must enforce source-event
// EQUIVALENCE. A telco replay of the SAME event returns the EXACT original
// outcome (Applied/Excess/AdvanceClosed, not just State); a reused
// source_event_id with a DIFFERENT payload is a divergent duplicate — refused
// loudly with a security audit, never a silent replay that could mis-book or
// double-count money.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/recovery"
)

func (f *fixture) ingestCmd(t *testing.T, cmd recovery.IngestCmd) (recovery.IngestResult, error) {
	t.Helper()
	return f.rec.Ingest(tenantCtx(), cmd)
}

// A same-event replay returns the EXACT original outcome, not just the state
// — a partial recovery replay must report the same Applied and Excess.
func TestRP02_Replay_ReturnsExactOriginalOutcome(t *testing.T) {
	f := newFixture(t, "rp02_replay")
	adv := f.activeAdvance(t) // ₦50 advance, outstanding 5000

	// Over-recovery: applied 5000, excess 2000 (quarantined), advance closed.
	cmd := recovery.IngestCmd{
		SourceEventID: "evt_rp02_over", MSISDNToken: "tok_sim_0001",
		Amount: entity.MustMoney(7_000, entity.NGN), OccurredAt: time.Now().UTC(), CorrelationID: "cor-over",
	}
	r1, err := f.ingestCmd(t, cmd)
	if err != nil {
		t.Fatal(err)
	}
	if r1.Applied.Amount() != 5_000 || r1.Excess.Amount() != 2_000 || !r1.AdvanceClosed {
		t.Fatalf("baseline over-recovery outcome wrong: %+v", r1)
	}
	_ = adv

	// Exact replay: the same outcome, marked Replayed, and NO new money moved.
	r2, err := f.ingestCmd(t, cmd)
	if err != nil {
		t.Fatal(err)
	}
	if !r2.Replayed {
		t.Fatal("replay must be flagged")
	}
	if r2.Applied.Amount() != 5_000 || r2.Excess.Amount() != 2_000 || !r2.AdvanceClosed ||
		r2.RecoveryEventID != r1.RecoveryEventID || r2.State != r1.State {
		t.Fatalf("replay must reproduce the EXACT original outcome, got %+v want %+v", r2, r1)
	}
	// Exactly one recovery event and one applied allocation exist.
	var events, suspense int
	if err := f.db.Admin.QueryRow(context.Background(),
		`SELECT (SELECT count(*) FROM recovery_events), (SELECT count(*) FROM suspense_items)`).
		Scan(&events, &suspense); err != nil {
		t.Fatal(err)
	}
	if events != 1 || suspense != 1 {
		t.Fatalf("replay must not double-book: events=%d suspense=%d", events, suspense)
	}
}

// A reused source_event_id with a DIFFERENT amount is a divergent duplicate:
// refused, audited, and the original booking is untouched.
func TestRP02_DivergentAmount_RefusedAndAudited(t *testing.T) {
	f := newFixture(t, "rp02_divamt")
	_ = f.activeAdvance(t)

	base := recovery.IngestCmd{
		SourceEventID: "evt_rp02_x", MSISDNToken: "tok_sim_0001",
		Amount: entity.MustMoney(2_000, entity.NGN), OccurredAt: time.Now().UTC(), CorrelationID: "cor-1",
	}
	if _, err := f.ingestCmd(t, base); err != nil {
		t.Fatal(err)
	}
	// SAME source id, DIFFERENT amount.
	divergent := base
	divergent.Amount = entity.MustMoney(9_000, entity.NGN)
	divergent.CorrelationID = "cor-2"
	_, err := f.ingestCmd(t, divergent)
	if !errors.Is(err, recovery.ErrDivergentRecovery) {
		t.Fatalf("reused source id + different amount must be a divergent duplicate, got %v", err)
	}
	// The original applied amount is unchanged (still 2000 against outstanding).
	var applied int64
	if err := f.db.Admin.QueryRow(context.Background(),
		`SELECT COALESCE(sum(amount_minor),0) FROM recovery_allocations`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != 2_000 {
		t.Fatalf("divergent duplicate must not alter the original booking, allocations sum=%d", applied)
	}
	// A DIVERGENT_DUPLICATE security-audit was written.
	var audits int
	if err := f.db.Admin.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_events WHERE action='recovery.ingest.divergent_duplicate' AND target_id='evt_rp02_x'`).
		Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Fatalf("divergent recovery must record a security audit, got %d", audits)
	}
}

// A reused source id with a divergent TOKEN (retargeting the money at a
// different subscriber) is refused.
func TestRP02_DivergentToken_Refused(t *testing.T) {
	f := newFixture(t, "rp02_divtok")
	_ = f.activeAdvance(t)
	if _, err := f.ingestCmd(t, recovery.IngestCmd{
		SourceEventID: "evt_rp02_tok", MSISDNToken: "tok_sim_0001",
		Amount: entity.MustMoney(1_000, entity.NGN), OccurredAt: time.Now().UTC(), CorrelationID: "c1",
	}); err != nil {
		t.Fatal(err)
	}
	_, err := f.ingestCmd(t, recovery.IngestCmd{
		SourceEventID: "evt_rp02_tok", MSISDNToken: "tok_someone_else",
		Amount: entity.MustMoney(1_000, entity.NGN), OccurredAt: time.Now().UTC(), CorrelationID: "c2",
	})
	if !errors.Is(err, recovery.ErrDivergentRecovery) {
		t.Fatalf("reused source id + different token must be refused, got %v", err)
	}
}

// The idempotency record carries the hash + response, and the recovery event
// is created exactly once for the source id — proving the DB is the arbiter.
func TestRP02_IdempotencyRecord_IsTheArbiter(t *testing.T) {
	f := newFixture(t, "rp02_arb")
	_ = f.activeAdvance(t)
	cmd := recovery.IngestCmd{
		SourceEventID: "evt_rp02_arb", MSISDNToken: "tok_sim_0001",
		Amount: entity.MustMoney(1_500, entity.NGN), OccurredAt: time.Now().UTC(), CorrelationID: "c",
	}
	if _, err := f.ingestCmd(t, cmd); err != nil {
		t.Fatal(err)
	}
	// A stored idempotency record exists for the ingest with a populated body.
	var status int
	var reqHash string
	if err := f.db.Admin.QueryRow(context.Background(), `
		SELECT response_status, request_hash FROM idempotency_records
		WHERE operation='recovery.ingest' AND idem_key='evt_rp02_arb'`).Scan(&status, &reqHash); err != nil {
		t.Fatal(err)
	}
	if status != 200 || reqHash == "" {
		t.Fatalf("idempotency record must carry the outcome (status=%d) and hash", status)
	}
	// Exactly one recovery event exists for the source id.
	var events int
	if err := f.db.Admin.QueryRow(context.Background(),
		`SELECT count(*) FROM recovery_events WHERE source_event_id='evt_rp02_arb'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("exactly one recovery event per source id, got %d", events)
	}
}
