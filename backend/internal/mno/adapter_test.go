package mno_test

// Adapter ↔ simulator contract tests: the EDG-005 primitive at adapter level
// (timeout → Unknown → enquiry → Confirmed), definitive failure, idempotent
// replay, and NOT_FOUND for never-landed requests. The adapter endpoint comes
// from governed config activated through the real lifecycle — proving an
// admin config change re-points the adapter without redeploy.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/mno"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
	"github.com/ArowuTest/telco-credit-platform/simulator/sim"
)

// pointAdapterAt activates a telco.adapter config version targeting url —
// through the full governed lifecycle (draft/submit/approve/activate).
func pointAdapterAt(t *testing.T, svc *configsvc.Service, url string, timeoutMs int) {
	t.Helper()
	ctx := context.Background()
	content := fmt.Sprintf(`{"fulfilment_url":%q,"request_timeout_ms":%d,"retry_budget":0,"circuit_error_threshold_pct":50,"circuit_min_requests":20}`, url, timeoutMs)
	c, err := svc.CreateDraft(ctx, "telco.adapter", "telco:SIM_NG", "alice", "point at test sim", json.RawMessage(content))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Submit(ctx, c.ConfigVersionID, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Approve(ctx, c.ConfigVersionID, "bob"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Activate(ctx, c.ConfigVersionID, "bob", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
}

func newAdapterFixture(t *testing.T, suffix string, hold time.Duration, timeoutMs int) (*mno.HTTPAdapter, *sim.Simulator) {
	t.Helper()
	db := testutil.MustSetup(t, suffix)
	simulator := sim.New(slog.Default(), "test", hold)
	srv := httptest.NewServer(simulator.Handler())
	t.Cleanup(srv.Close)
	cfg := configsvc.New(db.Worker)
	pointAdapterAt(t, cfg, srv.URL, timeoutMs)
	return mno.NewHTTPAdapter(cfg), simulator
}

func req(id, token string) mno.FulfilmentRequest {
	return mno.FulfilmentRequest{
		PlatformRequestID:   id,
		SubscriberAccountID: "sub_sim_0001",
		MSISDNToken:         token,
		ProductType:         "AIRTIME_ADVANCE",
		FaceValue:           entity.MustMoney(10_000, entity.NGN),
		OfferSnapshotID:     "off_test",
	}
}

func TestAdapter_SuccessAndDefinitiveFailure(t *testing.T) {
	a, _ := newAdapterFixture(t, "mno_basic", 0, 2_000)
	ctx := context.Background()

	ok, err := a.SubmitFulfilment(ctx, "SIM_NG", "idem-1", req("PRQ-1", "tok_sim_0001"))
	if err != nil {
		t.Fatal(err)
	}
	if ok.Outcome != mno.OutcomeConfirmed || ok.TelcoReference == "" {
		t.Fatalf("want Confirmed with ref, got %+v", ok)
	}

	fail, err := a.SubmitFulfilment(ctx, "SIM_NG", "idem-2", req("PRQ-2", "tok_FAIL_01"))
	if err != nil {
		t.Fatal(err)
	}
	if fail.Outcome != mno.OutcomeFailed {
		t.Fatalf("FAIL token must be Failed, got %+v", fail)
	}
}

func TestEDG005_TimeoutAfterSuccess_UnknownThenEnquiryConfirms(t *testing.T) {
	// Simulator holds the response for 2s; adapter timeout is 300ms: the
	// submission classifies Unknown, the credit HAS happened, and status
	// enquiry reveals Confirmed — never a resubmission.
	a, _ := newAdapterFixture(t, "mno_edg005", 2*time.Second, 300)
	ctx := context.Background()

	res, err := a.SubmitFulfilment(ctx, "SIM_NG", "idem-t1", req("PRQ-T1", "tok_TIMEOUT_01"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != mno.OutcomeUnknown {
		t.Fatalf("timeout must classify Unknown (INV-009), got %+v", res)
	}

	enq, err := a.EnquireStatus(ctx, "SIM_NG", "PRQ-T1")
	if err != nil {
		t.Fatal(err)
	}
	if enq.Outcome != mno.OutcomeConfirmed || enq.TelcoReference == "" {
		t.Fatalf("enquiry after timeout-after-success must be Confirmed, got %+v", enq)
	}
}

func TestAdapter_EnquiryNotFound_ForNeverLandedRequest(t *testing.T) {
	a, _ := newAdapterFixture(t, "mno_notfound", 0, 2_000)
	res, err := a.EnquireStatus(context.Background(), "SIM_NG", "PRQ-NEVER-SENT")
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != mno.OutcomeNotFound {
		t.Fatalf("never-landed request must be NOT_FOUND (safe to fail), got %+v", res)
	}
}

func TestM1B2F2_NonDefinitiveHTTPCodes_ClassifyUnknown(t *testing.T) {
	// 408/429/503 can come from an aggregator edge while the telco backend is
	// still processing; only the definitive-rejection allowlist may be Failed.
	db := testutil.MustSetup(t, "mno_codes")
	cfg := configsvc.New(db.Worker)

	var code int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(code)
		_, _ = w.Write([]byte(`{"error":"gateway"}`))
	}))
	t.Cleanup(srv.Close)
	pointAdapterAt(t, cfg, srv.URL, 2_000)
	a := mno.NewHTTPAdapter(cfg)

	cases := []struct {
		code int
		want mno.Outcome
	}{
		{http.StatusRequestTimeout, mno.OutcomeUnknown},     // 408: edge timeout, backend may proceed
		{http.StatusTooManyRequests, mno.OutcomeUnknown},    // 429: throttled at edge
		{http.StatusServiceUnavailable, mno.OutcomeUnknown}, // 503
		{http.StatusBadGateway, mno.OutcomeUnknown},         // 502
		{http.StatusBadRequest, mno.OutcomeFailed},          // 400: definitive
		{http.StatusUnprocessableEntity, mno.OutcomeFailed}, // 422: definitive
		{http.StatusConflict, mno.OutcomeFailed},            // 409: definitive
	}
	for i, c := range cases {
		code = c.code
		res, err := a.SubmitFulfilment(context.Background(), "SIM_NG",
			fmt.Sprintf("idem-code-%d", i), req(fmt.Sprintf("PRQ-C%d", i), "tok_sim_0001"))
		if err != nil {
			t.Fatal(err)
		}
		if res.Outcome != c.want {
			t.Errorf("HTTP %d must classify %s, got %s", c.code, c.want, res.Outcome)
		}
	}
}

func TestAdapter_DuplicateSubmitReplaysOriginal(t *testing.T) {
	a, _ := newAdapterFixture(t, "mno_dup", 0, 2_000)
	ctx := context.Background()

	r1, err := a.SubmitFulfilment(ctx, "SIM_NG", "idem-dup", req("PRQ-D1", "tok_sim_0001"))
	if err != nil {
		t.Fatal(err)
	}
	r2, err := a.SubmitFulfilment(ctx, "SIM_NG", "idem-dup", req("PRQ-D1", "tok_sim_0001"))
	if err != nil {
		t.Fatal(err)
	}
	if r2.Outcome != mno.OutcomeConfirmed || r2.TelcoReference != r1.TelcoReference {
		t.Fatalf("duplicate submit must replay original ref %s, got %+v", r1.TelcoReference, r2)
	}
}
