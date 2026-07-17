// cmd/simulator — standing telco simulator (V2-SIM-001..012). M0 scope: the
// canonical fulfilment API happy path plus deterministic, seed-driven
// behaviour hooks; the full fault catalogue (timeout-after-success, duplicate
// callbacks, out-of-order reversals, malformed files) lands in M1 alongside
// the walking skeleton that must survive it.
//
// Determinism (V2-SIM-006): behaviour is selected by a stable hash of the
// platform_request_id and the scenario seed — the same request replays the
// same outcome, so failures found in CI are reproducible.
package main

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

type fulfilmentReq struct {
	PlatformRequestID   string `json:"platform_request_id"`
	SubscriberAccountID string `json:"subscriber_account_id"`
	MSISDNToken         string `json:"msisdn_token"`
	ProductType         string `json:"product_type"`
	FaceValueMinor      int64  `json:"face_value_minor"`
	Currency            string `json:"currency"`
	OfferSnapshotID     string `json:"offer_snapshot_id"`
	CallbackURL         string `json:"callback_url"`
}

type fulfilmentResp struct {
	TelcoTransactionReference string `json:"telco_transaction_reference"`
	Status                    string `json:"status"`
}

type simulator struct {
	log  *slog.Logger
	seed string

	mu sync.Mutex
	// idempotency: same Idempotency-Key returns the same outcome (V2-TEL-003)
	seen map[string]fulfilmentResp
}

func (s *simulator) fulfil(w http.ResponseWriter, r *http.Request) {
	idemKey := r.Header.Get("Idempotency-Key")
	if idemKey == "" {
		http.Error(w, `{"error":"missing Idempotency-Key"}`, http.StatusBadRequest)
		return
	}
	var req fulfilmentReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.PlatformRequestID == "" {
		http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	if prev, ok := s.seen[idemKey]; ok {
		s.mu.Unlock()
		s.log.Info("duplicate fulfilment replayed", "idem_key", idemKey)
		writeJSON(w, http.StatusOK, prev)
		return
	}
	resp := fulfilmentResp{
		TelcoTransactionReference: fmt.Sprintf("SIM-%08x", stableHash(s.seed+req.PlatformRequestID)),
		Status:                    "SUCCESS",
	}
	s.seen[idemKey] = resp
	s.mu.Unlock()

	s.log.Info("fulfilment credited", "request", req.PlatformRequestID,
		"value_minor", req.FaceValueMinor, "ref", resp.TelcoTransactionReference)
	writeJSON(w, http.StatusOK, resp)
}

func stableHash(s string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return h.Sum32()
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	s := &simulator{log: log, seed: env("SIM_SEED", "m0"), seen: map[string]fulfilmentResp{}}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "seed": s.seed})
	})
	mux.HandleFunc("POST /v1/telcos/{telcoId}/fulfilments", s.fulfil)

	addr := env("SIM_ADDR", ":8091")
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Info("simulator listening", "addr", addr, "seed", s.seed)
	if err := srv.ListenAndServe(); err != nil {
		log.Error("simulator stopped", "err", err)
		os.Exit(1)
	}
}
