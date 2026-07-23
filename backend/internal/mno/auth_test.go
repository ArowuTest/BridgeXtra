package mno_test

// Phase 1 S1 — outbound partner-auth BOUNDARY CONTRACT tests. A mock MNO records
// exactly what the adapter presents on the wire, so the contract is pinned: when
// the real MTN scheme/endpoints arrive, these assertions either match or fail
// loudly. Covers apikey (static header), oauth2 (client-credentials + Bearer +
// token caching), fail-closed on a missing secret (nothing is sent), the none
// default, and that status enquiries are authenticated too.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/mno"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
)

// capturingMNO is a stand-in MNO that records the credentials the adapter sends.
type capturingMNO struct {
	mu             sync.Mutex
	apiKeyHeader   string // which header to capture as the api key, if any
	gotAuthz       string
	gotAPIKey      string
	tokenHits      int
	fulfilHits     int
	enquiryGotAuth string
}

func (m *capturingMNO) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		m.mu.Lock()
		m.tokenHits++
		m.mu.Unlock()
		if r.Form.Get("grant_type") != "client_credentials" || r.Form.Get("client_id") == "" || r.Form.Get("client_secret") == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"access_token":"tok-abc123","expires_in":3600}`)
	})
	mux.HandleFunc("/v1/telcos/SIM_NG/fulfilments", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.fulfilHits++
		m.gotAuthz = r.Header.Get("Authorization")
		if m.apiKeyHeader != "" {
			m.gotAPIKey = r.Header.Get(m.apiKeyHeader)
		}
		m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"telco_transaction_reference":"REF-1","status":"SUCCESS"}`)
	})
	// Status enquiry: GET /v1/telcos/SIM_NG/fulfilments/{id}
	mux.HandleFunc("/v1/telcos/SIM_NG/fulfilments/", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.enquiryGotAuth = r.Header.Get("Authorization")
		m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"telco_transaction_reference":"REF-1","status":"SUCCESS"}`)
	})
	return mux
}

// adapterWithAuth activates a telco.adapter config whose optional auth block is
// authJSON (with %BASE% substituted for the mock server URL), and returns a live
// adapter plus the capturing MNO.
func adapterWithAuth(t *testing.T, suffix, authJSON string) (*mno.HTTPAdapter, *capturingMNO) {
	t.Helper()
	db := testutil.MustSetup(t, suffix)
	m := &capturingMNO{}
	srv := httptest.NewServer(m.handler())
	t.Cleanup(srv.Close)

	authJSON = strings.ReplaceAll(authJSON, "%BASE%", srv.URL)
	cfg := configsvc.New(db.Worker)
	content := fmt.Sprintf(`{"fulfilment_url":%q,"request_timeout_ms":2000,"retry_budget":0,"circuit_error_threshold_pct":50,"circuit_min_requests":20,"circuit_cooldown_seconds":30,"max_weekly_recharge_minor":100000000%s}`, srv.URL, authJSON)
	ctx := context.Background()
	c, err := cfg.CreateDraft(ctx, "telco.adapter", "telco:SIM_NG", "alice", "auth test", []byte(content))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Submit(ctx, c.ConfigVersionID, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Approve(ctx, c.ConfigVersionID, "bob"); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Activate(ctx, c.ConfigVersionID, "bob", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	return mno.NewHTTPAdapter(cfg), m
}

func authReq(id string) mno.FulfilmentRequest {
	return mno.FulfilmentRequest{
		PlatformRequestID:   id,
		SubscriberAccountID: "sub_sim_0001",
		MSISDNToken:         "tok_sim_0001",
		ProductType:         "AIRTIME_ADVANCE",
		FaceValue:           entity.MustMoney(10_000, entity.NGN),
		OfferSnapshotID:     "off_test",
	}
}

func TestAuth_APIKey_HeaderPresented(t *testing.T) {
	t.Setenv("TCP_TEST_MNO_KEY", "supersecret-key")
	a, m := adapterWithAuth(t, "auth_apikey",
		`,"auth":{"scheme":"apikey","header":"X-Api-Key","secret_env":"TCP_TEST_MNO_KEY"}`)
	m.apiKeyHeader = "X-Api-Key"

	res, err := a.SubmitFulfilment(context.Background(), "SIM_NG", "idem-ak", authReq("PRQ-AK"))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if res.Outcome != mno.OutcomeConfirmed {
		t.Fatalf("want Confirmed, got %+v", res)
	}
	if m.gotAPIKey != "supersecret-key" {
		t.Fatalf("MNO must receive the api key in X-Api-Key, got %q", m.gotAPIKey)
	}
}

func TestAuth_OAuth2_BearerPresentedAndCached(t *testing.T) {
	t.Setenv("TCP_TEST_MNO_CS", "client-secret-xyz")
	a, m := adapterWithAuth(t, "auth_oauth2",
		`,"auth":{"scheme":"oauth2","token_url":"%BASE%/oauth/token","client_id":"bridgextra","client_secret_env":"TCP_TEST_MNO_CS"}`)

	for i := 0; i < 2; i++ {
		res, err := a.SubmitFulfilment(context.Background(), "SIM_NG", fmt.Sprintf("idem-o%d", i), authReq(fmt.Sprintf("PRQ-O%d", i)))
		if err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
		if res.Outcome != mno.OutcomeConfirmed {
			t.Fatalf("want Confirmed, got %+v", res)
		}
	}
	if m.gotAuthz != "Bearer tok-abc123" {
		t.Fatalf("MNO must receive Bearer token, got %q", m.gotAuthz)
	}
	if m.tokenHits != 1 {
		t.Fatalf("token must be fetched once and cached across calls, got %d fetches", m.tokenHits)
	}
}

func TestAuth_FailClosed_MissingSecret(t *testing.T) {
	// secret_env points at an UNSET variable — the call must refuse, unsent.
	a, m := adapterWithAuth(t, "auth_failclosed",
		`,"auth":{"scheme":"apikey","header":"X-Api-Key","secret_env":"TCP_TEST_MNO_ABSENT"}`)

	_, err := a.SubmitFulfilment(context.Background(), "SIM_NG", "idem-fc", authReq("PRQ-FC"))
	if err == nil {
		t.Fatal("a configured auth with a missing secret must fail closed (error), not send unauthenticated")
	}
	if m.fulfilHits != 0 {
		t.Fatalf("nothing must reach the MNO when auth fails closed, got %d hits", m.fulfilHits)
	}
}

func TestAuth_None_NoHeader(t *testing.T) {
	a, m := adapterWithAuth(t, "auth_none", `,"auth":{"scheme":"none"}`)
	res, err := a.SubmitFulfilment(context.Background(), "SIM_NG", "idem-n", authReq("PRQ-N"))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if res.Outcome != mno.OutcomeConfirmed {
		t.Fatalf("want Confirmed, got %+v", res)
	}
	if m.gotAuthz != "" || m.gotAPIKey != "" {
		t.Fatalf("scheme=none must send no auth, got authz=%q apikey=%q", m.gotAuthz, m.gotAPIKey)
	}
}

func TestAuth_EnquireStatus_Authenticated(t *testing.T) {
	t.Setenv("TCP_TEST_MNO_KEY2", "enquiry-key")
	a, m := adapterWithAuth(t, "auth_enquiry",
		`,"auth":{"scheme":"apikey","header":"Authorization","secret_env":"TCP_TEST_MNO_KEY2"}`)
	if _, err := a.EnquireStatus(context.Background(), "SIM_NG", "PRQ-E"); err != nil {
		t.Fatalf("enquiry: %v", err)
	}
	if m.enquiryGotAuth != "enquiry-key" {
		t.Fatalf("status enquiry must be authenticated too, got %q", m.enquiryGotAuth)
	}
}
