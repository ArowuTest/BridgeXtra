package mno_test

// R-P0-8b: at the adapter level, a DOWN telco (persistent 5xx) trips the
// circuit, and once open the adapter short-circuits to Unknown WITHOUT dialing
// — no more doomed HTTP calls hammering the telco. The seeded policy is 50%
// errors over 20 requests.

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

	// The circuit is now OPEN: the next call short-circuits to Unknown and does
	// NOT dial the down telco.
	res, err := a.SubmitFulfilment(ctx, "SIM_NG", "k-after-open", req("PRQ", "tok_sim_0001"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != mno.OutcomeUnknown || !strings.Contains(string(res.ResponseEvidence), "circuit_open") {
		t.Fatalf("open circuit must short-circuit to Unknown with circuit_open evidence, got %s / %s", res.Outcome, res.ResponseEvidence)
	}
	if atomic.LoadInt64(&hits) != 20 {
		t.Fatalf("an open circuit must NOT dial the telco, dials rose to %d", atomic.LoadInt64(&hits))
	}
}
