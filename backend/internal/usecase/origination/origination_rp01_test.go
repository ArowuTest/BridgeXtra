package origination_test

// R-P0-1 adversarial pack: confirm idempotency must enforce request
// EQUIVALENCE, not just key presence. A reused key with the SAME request is a
// valid replay (one advance, original outcome); a reused key with a DIFFERENT
// request is a divergent duplicate — refused loudly with a security audit,
// never a silent replay of the original advance (API-002/003, ADV-002/006).

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/origination"
)

func (f *fixture) advanceCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.db.Admin.QueryRow(context.Background(), `SELECT count(*) FROM advances`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A reused key with a divergent DIFFERENT OFFER must be refused, and must NOT
// return the original advance.
func TestRP01_SameKeyDifferentOffer_DivergentDuplicate(t *testing.T) {
	f := newFixture(t, "rp01_offer", 0, 2_000)
	offers := f.offersFor(t, "tok_sim_0001")
	if len(offers) < 2 {
		t.Fatal("need >=2 offers")
	}
	r1, err := f.svc.Confirm(tenantCtx(), origination.ConfirmCmd{
		ProgrammeID: "prg_sim_airtime01", OfferID: offers[0].OfferID, MSISDNToken: "tok_sim_0001",
		IdemKey: "rp01-k1", CorrelationID: "cor-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	// SAME key, DIFFERENT offer.
	_, err = f.svc.Confirm(tenantCtx(), origination.ConfirmCmd{
		ProgrammeID: "prg_sim_airtime01", OfferID: offers[1].OfferID, MSISDNToken: "tok_sim_0001",
		IdemKey: "rp01-k1", CorrelationID: "cor-2",
	})
	if !errors.Is(err, origination.ErrDivergentDuplicate) {
		t.Fatalf("same key + different offer must be a divergent duplicate, got %v", err)
	}
	// The original advance is untouched; no second advance was created.
	if n := f.advanceCount(t); n != 1 {
		t.Fatalf("divergent duplicate must not create a second advance, got %d", n)
	}
	// A DIVERGENT_DUPLICATE security-audit was written.
	var audits int
	if err := f.db.Admin.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_events WHERE action='advance.confirm.divergent_duplicate' AND target_id='rp01-k1'`).
		Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Fatalf("divergent duplicate must record a security audit, got %d", audits)
	}
	// A later SAME-body replay of the ORIGINAL request still works.
	r2, err := f.svc.Confirm(tenantCtx(), origination.ConfirmCmd{
		ProgrammeID: "prg_sim_airtime01", OfferID: offers[0].OfferID, MSISDNToken: "tok_sim_0001",
		IdemKey: "rp01-k1", CorrelationID: "cor-3",
	})
	if err != nil || !r2.Replayed || r2.Advance.AdvanceID != r1.Advance.AdvanceID {
		t.Fatalf("same-request replay must still return the original advance: %+v %v", r2, err)
	}
}

// A reused key with a DIFFERENT SUBSCRIBER TOKEN must be refused — this is the
// dangerous shape (piggybacking a changed command on a completed key).
func TestRP01_SameKeyDifferentToken_DivergentDuplicate(t *testing.T) {
	f := newFixture(t, "rp01_token", 0, 2_000)
	f.seedSubscriber(t, "sub_rp01_b", "tok_rp01_b", 50_000)
	offersA := f.offersFor(t, "tok_sim_0001")
	offersB := f.offersFor(t, "tok_rp01_b")

	if _, err := f.svc.Confirm(tenantCtx(), origination.ConfirmCmd{
		ProgrammeID: "prg_sim_airtime01", OfferID: offersA[0].OfferID, MSISDNToken: "tok_sim_0001",
		IdemKey: "rp01-shared", CorrelationID: "cor-a",
	}); err != nil {
		t.Fatal(err)
	}
	_, err := f.svc.Confirm(tenantCtx(), origination.ConfirmCmd{
		ProgrammeID: "prg_sim_airtime01", OfferID: offersB[0].OfferID, MSISDNToken: "tok_rp01_b",
		IdemKey: "rp01-shared", CorrelationID: "cor-b",
	})
	if !errors.Is(err, origination.ErrDivergentDuplicate) {
		t.Fatalf("same key across subscribers must be a divergent duplicate, got %v", err)
	}
	// Subscriber B got NO advance from the reused key.
	var bAdvances int
	if err := f.db.Admin.QueryRow(context.Background(),
		`SELECT count(*) FROM advances WHERE subscriber_account_id='sub_rp01_b'`).Scan(&bAdvances); err != nil {
		t.Fatal(err)
	}
	if bAdvances != 0 {
		t.Fatalf("a reused key must never mint an advance for a different subscriber, got %d", bAdvances)
	}
}

// Concurrent same-key/same-body: exactly one advance, both callers see it.
func TestRP01_ConcurrentSameKeySameBody_OneAdvanceBothReplay(t *testing.T) {
	f := newFixture(t, "rp01_cc_same", 0, 2_000)
	offers := f.offersFor(t, "tok_sim_0001")
	cmd := origination.ConfirmCmd{
		ProgrammeID: "prg_sim_airtime01", OfferID: offers[0].OfferID, MSISDNToken: "tok_sim_0001",
		IdemKey: "rp01-cc", CorrelationID: "cor-cc",
	}
	const n = 6
	var wg sync.WaitGroup
	ids := make([]string, n)
	errs := make([]error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			r, err := f.svc.Confirm(tenantCtx(), cmd)
			errs[i] = err
			if err == nil {
				ids[i] = r.Advance.AdvanceID
			}
		}(i)
	}
	wg.Wait()
	first := ""
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("same-body concurrent confirm must never error, got %v", errs[i])
		}
		if first == "" {
			first = ids[i]
		} else if ids[i] != first {
			t.Fatalf("all concurrent same-key confirms must resolve to ONE advance: %s vs %s", first, ids[i])
		}
	}
	if adv := f.advanceCount(t); adv != 1 {
		t.Fatalf("concurrent same-key confirms must create exactly one advance, got %d", adv)
	}
}

// Concurrent same-key/DIFFERENT-body: exactly one advance; the divergent
// contenders are refused, never silently replayed.
func TestRP01_ConcurrentSameKeyDifferentBody_DivergentRefused(t *testing.T) {
	f := newFixture(t, "rp01_cc_div", 0, 2_000)
	offers := f.offersFor(t, "tok_sim_0001")
	if len(offers) < 2 {
		t.Fatal("need >=2 offers")
	}
	const n = 8
	var wg sync.WaitGroup
	results := make([]struct {
		id  string
		err error
	}, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			// Half use offer0, half offer1 — same key for all.
			off := offers[i%2]
			r, err := f.svc.Confirm(tenantCtx(), origination.ConfirmCmd{
				ProgrammeID: "prg_sim_airtime01", OfferID: off.OfferID, MSISDNToken: "tok_sim_0001",
				IdemKey: "rp01-ccdiv", CorrelationID: "cor",
			})
			results[i].err = err
			if err == nil {
				results[i].id = r.Advance.AdvanceID
			}
		}(i)
	}
	wg.Wait()

	winners, divergent := 0, 0
	var winnerID string
	for _, r := range results {
		switch {
		case r.err == nil:
			winners++
			winnerID = r.id
		case errors.Is(r.err, origination.ErrDivergentDuplicate):
			divergent++
		default:
			// A same-body replay that lost the claim race is also acceptable
			// (it returns the winner's advance) — but a hard error is not.
			t.Fatalf("unexpected error from concurrent confirm: %v", r.err)
		}
	}
	if winnerID == "" {
		t.Fatal("at least one confirm must succeed")
	}
	if divergent == 0 {
		t.Fatal("the divergent-body contenders must be refused, not silently replayed")
	}
	// Exactly one advance exists regardless of the race outcome.
	if adv := f.advanceCount(t); adv != 1 {
		t.Fatalf("a divergent concurrent storm must still yield exactly one advance, got %d", adv)
	}
	_ = entity.AdvActive
}

// Crash-after-commit-before-response: the advance is durable, so a retry with
// the SAME body replays it rather than double-lending. (Simulated by a normal
// confirm followed by a retry — the first "response" is assumed lost.)
func TestRP01_CrashAfterCommit_RetryReplaysNotDoubleLends(t *testing.T) {
	f := newFixture(t, "rp01_crash", 0, 2_000)
	offers := f.offersFor(t, "tok_sim_0001")
	cmd := origination.ConfirmCmd{
		ProgrammeID: "prg_sim_airtime01", OfferID: offers[0].OfferID, MSISDNToken: "tok_sim_0001",
		IdemKey: "rp01-crash", CorrelationID: "cor",
	}
	r1, err := f.svc.Confirm(tenantCtx(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	// The client never saw r1 (crash) and retries with the SAME body.
	r2, err := f.svc.Confirm(tenantCtx(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	if !r2.Replayed || r2.Advance.AdvanceID != r1.Advance.AdvanceID {
		t.Fatalf("retry after lost response must replay the original advance: %+v", r2)
	}
	if adv := f.advanceCount(t); adv != 1 {
		t.Fatalf("retry must not double-lend, got %d advances", adv)
	}
}
