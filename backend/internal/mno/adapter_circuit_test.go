package mno_test

// R-P0-8b: at the adapter level, a DOWN telco (persistent 5xx) trips the
// circuit, and once open the adapter short-circuits WITHOUT dialing — no more
// doomed HTTP calls hammering the telco. The seeded policy is 50% errors over 20
// requests.
//
// BX-HIGH-010: a 5xx that was SENT stays UNKNOWN (the telco received it; did it
// process? ambiguous). But the circuit-OPEN short-circuit is a DETERMINISTIC
// non-send (nothing dialled) — it must classify FAILED+NotSent so the saga
// RELEASES the reservation, not a zombie UNKNOWN holding exposure for a request
// that was never sent. Mutation proof: revert the circuit-open branch to
// OutcomeUnknown and the post-open assertions below go red.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/mno"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
)

func TestRP08B_CircuitOpensOnTelcoDown_ShortCircuits(t *testing.T) {
	db := testutil.MustSetup(t, "rp08b_open")
	var hits int64
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.WriteHeader(http.StatusInternalServerError) // telco backend down
	}))
	t.Cleanup(down.Close)

	cfg := configsvc.New(db.Worker)
	pointAdapterAt(t, cfg, down.URL, 2_000) // 50% over 20 reqs, cooldown 30s
	a := mno.NewHTTPAdapter(cfg)
	ctx := context.Background()

	// 20 straight 5xx failures reach the min sample at 100% error → trip.
	for i := 0; i < 20; i++ {
		res, err := a.SubmitFulfilment(ctx, "SIM_NG", fmt.Sprintf("k%d", i), req("PRQ", "tok_sim_0001"))
		if err != nil {
			t.Fatal(err)
		}
		if res.Outcome != mno.OutcomeUnknown {
			t.Fatalf("a 5xx must classify Unknown (INV-009), got %s", res.Outcome)
		}
	}
	dialed := atomic.LoadInt64(&hits)
	if dialed != 20 {
		t.Fatalf("expected 20 dials before the circuit opens, got %d", dialed)
	}

	// The circuit is now OPEN: the next call short-circuits WITHOUT dialing, and —
	// BX-HIGH-010 — classifies FAILED+NotSent (a definite non-send releases the
	// reservation), NOT a zombie UNKNOWN. Evidence records circuit_open.
	res, err := a.SubmitFulfilment(ctx, "SIM_NG", "k-after-open", req("PRQ", "tok_sim_0001"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != mno.OutcomeFailed || !res.NotSent {
		t.Fatalf("open circuit is a deterministic non-send: must be FAILED+NotSent (release), got outcome=%s notSent=%v (BX-HIGH-010)", res.Outcome, res.NotSent)
	}
	if !strings.Contains(string(res.ResponseEvidence), "circuit_open") {
		t.Fatalf("open-circuit evidence must record circuit_open, got %s", res.ResponseEvidence)
	}
	if atomic.LoadInt64(&hits) != 20 {
		t.Fatalf("an open circuit must NOT dial the telco, dials rose to %d", atomic.LoadInt64(&hits))
	}
}
