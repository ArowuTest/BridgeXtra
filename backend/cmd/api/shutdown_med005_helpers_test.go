package main

// Helpers for the BX-MED-005 lifecycle tests. Kept separate so the tests read as the proof and
// not as plumbing.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ArowuTest/telco-credit-platform/simulator/sim"
)

func statusOfURL(url string) int {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return -1
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}

func waitFor(t *testing.T, cond func() bool, limit time.Duration, what string) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener address is %T, want *net.TCPAddr", ln.Addr())
	}
	return addr.Port
}

// appDSNFor mirrors testutil's DSN construction (same host/port source) for the app role, so the
// subprocess connects to the very database this test provisioned.
func appDSNFor(dbName string) string {
	hp := os.Getenv("TCP_TEST_HOSTPORT")
	if hp == "" {
		hp = "localhost:5434"
	}
	return fmt.Sprintf("postgres://tcp_app:devlocal_app@%s/%s", hp, dbName)
}

func repoBackendDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd() // .../backend/cmd/api
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(filepath.Dir(wd))
}

// startHoldingSim runs the telco simulator with a hold, so a "TIMEOUT" token's fulfilment response
// is held long enough for the confirm to still be in flight when the signal lands.
func startHoldingSim(t *testing.T, hold time.Duration) (stop func(), url string) {
	t.Helper()
	srv := httptest.NewServer(sim.New(slog.Default(), "med005", hold).Handler())
	return srv.Close, srv.URL
}

func fetchOffer(t *testing.T, base, token string) (offerID, disclosureRef string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"programme_id": "prg_sim_airtime01", "msisdn_token": token})
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/offers", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", "sim-api-key")
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var offers []struct {
		OfferID       string `json:"offer_id"`
		DisclosureRef string `json:"disclosure_ref"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&offers); err != nil || len(offers) == 0 {
		t.Fatalf("offers: status=%d err=%v", resp.StatusCode, err)
	}
	return offers[0].OfferID, offers[0].DisclosureRef
}

func postConfirm(t *testing.T, base, offerID, disclosureRef, token, idemKey string) (int, error) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"programme_id": "prg_sim_airtime01", "offer_id": offerID, "msisdn_token": token,
		"disclosure_ref": disclosureRef, "channel": "USSD", "session_id": "med005-sess",
		"accepted_at": time.Now().UTC().Format(time.RFC3339),
	})
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/advances", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", "sim-api-key")
	req.Header.Set("Idempotency-Key", idemKey)
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, nil
}
