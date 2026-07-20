package handler_test

// The walking skeleton over REAL HTTP: telco-authenticated channel requests
// through offers -> confirm -> status -> recovery, with BC-7 error envelopes,
// BC-6 correlation echo + journal lineage, and EDG-001/004 replay semantics
// at the wire level.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/handler"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/ledger"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/mno"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/origination"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/recovery"
	"github.com/ArowuTest/telco-credit-platform/simulator/sim"
)

type channelFixture struct {
	db  *testutil.DB
	srv *httptest.Server
}

func newChannelFixture(t *testing.T, suffix string, simHold time.Duration, adapterTimeoutMs int) *channelFixture {
	t.Helper()
	db := testutil.MustSetup(t, suffix)
	db.SeedTelco(t, "SIM_NG", "sim-api-key") // credential for the channel

	simulator := sim.New(slog.Default(), "chan-test", simHold)
	simSrv := httptest.NewServer(simulator.Handler())
	t.Cleanup(simSrv.Close)

	cfgW := configsvc.New(db.Worker)
	ctx := context.Background()
	content := fmt.Sprintf(`{"fulfilment_url":%q,"request_timeout_ms":%d,"retry_budget":0,"circuit_error_threshold_pct":50,"circuit_min_requests":20,"circuit_cooldown_seconds":30,"max_weekly_recharge_minor":100000000}`, simSrv.URL, adapterTimeoutMs)
	c, err := cfgW.CreateDraft(ctx, "telco.adapter", "telco:SIM_NG", "alice", "test sim", []byte(content))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfgW.Submit(ctx, c.ConfigVersionID, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := cfgW.Approve(ctx, c.ConfigVersionID, "bob"); err != nil {
		t.Fatal(err)
	}
	if err := cfgW.Activate(ctx, c.ConfigVersionID, "bob", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	appCfg := configsvc.New(db.App)
	led := ledger.New(appCfg)
	orig := origination.New(db.App, appCfg, led, mno.NewHTTPAdapter(appCfg), slog.Default())
	rec := recovery.New(db.App, appCfg, led, slog.Default())

	telcos := &repo.Telcos{Pool: db.App}
	auth := &handler.TenantAuth{Telcos: telcos, Pool: db.App, Log: slog.Default()}
	mux := http.NewServeMux()
	(&handler.Channel{Origination: orig, Recovery: rec, Limiter: testLimiter(), Log: slog.Default()}).Mount(mux, auth)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &channelFixture{db: db, srv: srv}
}

func (f *channelFixture) do(t *testing.T, method, path string, headers map[string]string, body any) (*http.Response, []byte) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequest(method, f.srv.URL+path, &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Api-Key", "sim-api-key")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := f.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, raw
}

func TestChannel_WalkingSkeleton_OverHTTP(t *testing.T) {
	f := newChannelFixture(t, "chan_e2e", 0, 2_000)

	// 1. Offers — priced from config.
	resp, body := f.do(t, http.MethodGet,
		"/v1/offers?programme_id=prg_sim_airtime01&msisdn_token=tok_sim_0001", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("offers: %d %s", resp.StatusCode, body)
	}
	var offers []struct {
		OfferID       string `json:"offer_id"`
		DisclosureRef string `json:"disclosure_ref"`
		DisclosureTxt string `json:"disclosure_text"`
		FaceValue     struct {
			AmountMinor int64  `json:"amount_minor"`
			Currency    string `json:"currency"`
		} `json:"face_value"`
	}
	if err := json.Unmarshal(body, &offers); err != nil {
		t.Fatal(err)
	}
	if len(offers) == 0 || offers[0].FaceValue.AmountMinor != 5_000 || offers[0].FaceValue.Currency != "NGN" {
		t.Fatalf("offer ladder wrong: %s", body)
	}
	// R-P0-7: the channel must receive a disclosure to render and a reference
	// to echo back.
	if offers[0].DisclosureRef == "" || offers[0].DisclosureTxt == "" {
		t.Fatalf("offers must carry disclosure evidence: %s", body)
	}

	// 2. Confirm with correlation — 201 ACTIVE. The channel echoes the
	// disclosure reference and supplies channel/session/acceptance evidence.
	confirmBody := map[string]any{
		"programme_id": "prg_sim_airtime01", "offer_id": offers[0].OfferID, "msisdn_token": "tok_sim_0001",
		"disclosure_ref": offers[0].DisclosureRef, "channel": "USSD",
		"session_id": "wire-sess-1", "accepted_at": time.Now().UTC().Format(time.RFC3339),
	}
	resp, body = f.do(t, http.MethodPost, "/v1/advances", map[string]string{
		"Idempotency-Key": "wire-idem-1", "X-Correlation-Id": "cor-wire-1",
	}, confirmBody)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("confirm: %d %s", resp.StatusCode, body)
	}
	if resp.Header.Get("X-Correlation-Id") != "cor-wire-1" {
		t.Fatal("BC-6: correlation id must echo on the response")
	}
	var adv struct {
		AdvanceID   string `json:"advance_id"`
		Status      string `json:"status"`
		StatusRoute string `json:"status_route"`
		Outstanding struct {
			AmountMinor int64 `json:"amount_minor"`
		} `json:"outstanding"`
	}
	if err := json.Unmarshal(body, &adv); err != nil {
		t.Fatal(err)
	}
	if adv.Status != "ACTIVE" || adv.Outstanding.AmountMinor != 5_000 {
		t.Fatalf("advance: %s", body)
	}

	// BC-6: the journal carries the wire correlation id.
	var cor string
	if err := f.db.Admin.QueryRow(context.Background(),
		`SELECT correlation_id FROM journals`).Scan(&cor); err != nil {
		t.Fatal(err)
	}
	if cor != "cor-wire-1" {
		t.Fatalf("journal correlation = %q, want cor-wire-1 (tap-to-journal lineage)", cor)
	}

	// 3. EDG-001 over the wire: same key replays -> 200, same advance.
	resp, body = f.do(t, http.MethodPost, "/v1/advances", map[string]string{
		"Idempotency-Key": "wire-idem-1",
	}, confirmBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("replay must be 200: %d %s", resp.StatusCode, body)
	}
	var replay struct {
		AdvanceID string `json:"advance_id"`
	}
	if err := json.Unmarshal(body, &replay); err != nil {
		t.Fatal(err)
	}
	if replay.AdvanceID != adv.AdvanceID {
		t.Fatal("replay must return the original advance")
	}

	// 4. Status route (EDG-004).
	resp, body = f.do(t, http.MethodGet, adv.StatusRoute, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status route: %d %s", resp.StatusCode, body)
	}

	// 5. Recovery event closes the loop.
	resp, body = f.do(t, http.MethodPost, "/v1/recovery/events", map[string]string{
		"X-Correlation-Id": "cor-wire-rec",
	}, map[string]any{
		"source_event_id": "wire-src-1", "msisdn_token": "tok_sim_0001",
		"amount":      map[string]any{"amount_minor": 5_000, "currency": "NGN"},
		"occurred_at": time.Now().UTC().Format(time.RFC3339),
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("recovery: %d %s", resp.StatusCode, body)
	}
	var recRes struct {
		State         string `json:"state"`
		AdvanceClosed bool   `json:"advance_closed"`
	}
	if err := json.Unmarshal(body, &recRes); err != nil {
		t.Fatal(err)
	}
	if recRes.State != "ALLOCATED" || !recRes.AdvanceClosed {
		t.Fatalf("recovery must close the advance: %s", body)
	}

	// 6. Final status: CLOSED, outstanding zero.
	resp, body = f.do(t, http.MethodGet, adv.StatusRoute, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatal("final status")
	}
	if err := json.Unmarshal(body, &adv); err != nil {
		t.Fatal(err)
	}
	if adv.Status != "CLOSED" || adv.Outstanding.AmountMinor != 0 {
		t.Fatalf("final: %s", body)
	}
}

func TestVR10F1_CorrelationIdBounded_RemintedNeverTruncated(t *testing.T) {
	f := newChannelFixture(t, "chan_vr10f1", 0, 2_000)

	// (A raw newline can't be tested here: Go's http.Client refuses to send
	// it — transport-level defense; the charset check covers other clients.)
	long := bytes.Repeat([]byte("x"), 300)
	for _, bad := range []string{string(long), "has spaces", "semi;colon"} {
		resp, _ := f.do(t, http.MethodGet,
			"/v1/offers?programme_id=prg_sim_airtime01&msisdn_token=tok_sim_0001",
			map[string]string{"X-Correlation-Id": bad}, nil)
		echoed := resp.Header.Get("X-Correlation-Id")
		if echoed == bad {
			t.Fatalf("invalid correlation id %q must be re-minted, was accepted", bad)
		}
		if len(echoed) == 0 || len(echoed) > 64 {
			t.Fatalf("re-minted id out of bounds: %q", echoed)
		}
	}
	// A valid caller id passes through unchanged.
	resp, _ := f.do(t, http.MethodGet,
		"/v1/offers?programme_id=prg_sim_airtime01&msisdn_token=tok_sim_0001",
		map[string]string{"X-Correlation-Id": "valid-id_1.a"}, nil)
	if resp.Header.Get("X-Correlation-Id") != "valid-id_1.a" {
		t.Fatal("valid correlation id must be preserved")
	}
}

func TestVR10F2_AdvanceStatus404_UsesAdvanceFamily(t *testing.T) {
	f := newChannelFixture(t, "chan_vr10f2", 0, 2_000)
	resp, body := f.do(t, http.MethodGet, "/v1/advances/adv_does_not_exist", nil, nil)
	if resp.StatusCode != http.StatusNotFound || !bytes.Contains(body, []byte("ADVANCE_NOT_FOUND")) {
		t.Fatalf("advance-status 404 must render ADVANCE_NOT_FOUND: %d %s", resp.StatusCode, body)
	}
}

func TestChannel_IdempotencyKeyRequired(t *testing.T) {
	f := newChannelFixture(t, "chan_idem", 0, 2_000)
	resp, body := f.do(t, http.MethodPost, "/v1/advances", nil,
		map[string]string{"programme_id": "p", "offer_id": "o", "msisdn_token": "tok"})
	if resp.StatusCode != http.StatusBadRequest || !bytes.Contains(body, []byte("ADVANCE_IDEMPOTENCY_KEY_REQUIRED")) {
		t.Fatalf("missing Idempotency-Key must be 400 with stable code: %d %s", resp.StatusCode, body)
	}
}

func TestChannel_UnknownFulfilment_202Processing(t *testing.T) {
	f := newChannelFixture(t, "chan_unknown", 2*time.Second, 300)
	if _, err := f.db.Admin.Exec(context.Background(), `
		INSERT INTO subscriber_accounts (subscriber_account_id, telco_id, msisdn_token, status)
		VALUES ('sub_w1','SIM_NG','tok_TIMEOUT_w1','ACTIVE')`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(context.Background(), `
		INSERT INTO decision_snapshots (decision_snapshot_id, telco_id, subscriber_account_id,
		  max_face_value_minor, currency, config_version_id)
		VALUES ('dec_w1','SIM_NG','sub_w1',50000,'NGN','cfg_seed_product_airtime_v1')`); err != nil {
		t.Fatal(err)
	}

	resp, body := f.do(t, http.MethodGet,
		"/v1/offers?programme_id=prg_sim_airtime01&msisdn_token=tok_TIMEOUT_w1", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("offers: %d", resp.StatusCode)
	}
	var offers []struct {
		OfferID       string `json:"offer_id"`
		DisclosureRef string `json:"disclosure_ref"`
	}
	if err := json.Unmarshal(body, &offers); err != nil {
		t.Fatal(err)
	}

	resp, body = f.do(t, http.MethodPost, "/v1/advances", map[string]string{
		"Idempotency-Key": "wire-to-1",
	}, map[string]any{
		"programme_id": "prg_sim_airtime01", "offer_id": offers[0].OfferID, "msisdn_token": "tok_TIMEOUT_w1",
		"disclosure_ref": offers[0].DisclosureRef, "channel": "USSD",
		"session_id": "wire-sess-to", "accepted_at": time.Now().UTC().Format(time.RFC3339),
	})
	// V2-ADV-016: ambiguity is a safe 202 PROCESSING with a status route —
	// never an exposed UNKNOWN, never an invitation to retry.
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("timeout-after-success must be 202: %d %s", resp.StatusCode, body)
	}
	var adv struct {
		Status      string `json:"status"`
		StatusRoute string `json:"status_route"`
	}
	if err := json.Unmarshal(body, &adv); err != nil {
		t.Fatal(err)
	}
	if adv.Status != "PROCESSING" || adv.StatusRoute == "" {
		t.Fatalf("must be PROCESSING with status route: %s", body)
	}
}

func TestChannel_ExpiredOffer_409StableCode(t *testing.T) {
	f := newChannelFixture(t, "chan_expired", 0, 2_000)
	resp, body := f.do(t, http.MethodGet,
		"/v1/offers?programme_id=prg_sim_airtime01&msisdn_token=tok_sim_0001", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatal("offers")
	}
	var offers []struct {
		OfferID       string `json:"offer_id"`
		DisclosureRef string `json:"disclosure_ref"`
	}
	if err := json.Unmarshal(body, &offers); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(context.Background(),
		`UPDATE offers SET expires_at = now() - interval '1 minute'`); err != nil {
		t.Fatal(err)
	}
	resp, body = f.do(t, http.MethodPost, "/v1/advances", map[string]string{
		"Idempotency-Key": "wire-exp-1",
	}, map[string]any{
		"programme_id": "prg_sim_airtime01", "offer_id": offers[0].OfferID, "msisdn_token": "tok_sim_0001",
		"disclosure_ref": offers[0].DisclosureRef, "channel": "USSD",
		"session_id": "wire-sess-exp", "accepted_at": time.Now().UTC().Format(time.RFC3339),
	})
	if resp.StatusCode != http.StatusConflict || !bytes.Contains(body, []byte("OFFER_EXPIRED")) {
		t.Fatalf("expired offer must be 409 OFFER_EXPIRED: %d %s", resp.StatusCode, body)
	}
}

// R-P0-7 over the wire: a confirm that fails to prove consent — no disclosure
// reference, a disallowed channel, or a disclosure reference that doesn't match
// the offer — is refused with the documented envelope, never a silent advance.
func TestChannel_RP07_DisclosureEvidence_Refusals(t *testing.T) {
	f := newChannelFixture(t, "chan_rp07", 0, 2_000)
	_, body := f.do(t, http.MethodGet,
		"/v1/offers?programme_id=prg_sim_airtime01&msisdn_token=tok_sim_0001", nil, nil)
	var offers []struct {
		OfferID       string `json:"offer_id"`
		DisclosureRef string `json:"disclosure_ref"`
	}
	if err := json.Unmarshal(body, &offers); err != nil || len(offers) < 2 {
		t.Fatalf("need >=2 offers with disclosure: %v %s", err, body)
	}
	base := func() map[string]any {
		return map[string]any{
			"programme_id": "prg_sim_airtime01", "offer_id": offers[0].OfferID, "msisdn_token": "tok_sim_0001",
			"disclosure_ref": offers[0].DisclosureRef, "channel": "USSD",
			"session_id": "wire-sess-rp07", "accepted_at": time.Now().UTC().Format(time.RFC3339),
		}
	}

	// (a) missing disclosure_ref → 400 DISCLOSURE_EVIDENCE_REQUIRED.
	b := base()
	delete(b, "disclosure_ref")
	resp, body := f.do(t, http.MethodPost, "/v1/advances", map[string]string{"Idempotency-Key": "rp07-http-noref"}, b)
	if resp.StatusCode != http.StatusBadRequest || !bytes.Contains(body, []byte("DISCLOSURE_EVIDENCE_REQUIRED")) {
		t.Fatalf("missing disclosure must be 400 DISCLOSURE_EVIDENCE_REQUIRED: %d %s", resp.StatusCode, body)
	}

	// (b) disallowed channel → 400 CHANNEL_NOT_ALLOWED.
	b = base()
	b["channel"] = "IVR"
	resp, body = f.do(t, http.MethodPost, "/v1/advances", map[string]string{"Idempotency-Key": "rp07-http-chan"}, b)
	if resp.StatusCode != http.StatusBadRequest || !bytes.Contains(body, []byte("CHANNEL_NOT_ALLOWED")) {
		t.Fatalf("disallowed channel must be 400 CHANNEL_NOT_ALLOWED: %d %s", resp.StatusCode, body)
	}

	// (c) foreign disclosure reference (another offer's) → 409 DISCLOSURE_MISMATCH.
	b = base()
	b["disclosure_ref"] = offers[1].DisclosureRef
	resp, body = f.do(t, http.MethodPost, "/v1/advances", map[string]string{"Idempotency-Key": "rp07-http-mism"}, b)
	if resp.StatusCode != http.StatusConflict || !bytes.Contains(body, []byte("DISCLOSURE_MISMATCH")) {
		t.Fatalf("foreign disclosure must be 409 DISCLOSURE_MISMATCH: %d %s", resp.StatusCode, body)
	}

	// No advance was created by any refused confirm.
	var advances int
	if err := f.db.Admin.QueryRow(context.Background(), `SELECT count(*) FROM advances`).Scan(&advances); err != nil {
		t.Fatal(err)
	}
	if advances != 0 {
		t.Fatalf("refused confirms must create no advance, got %d", advances)
	}
}
