// Package sim is the standing telco simulator (V2-SIM-001..012): a REAL
// external service implementing the canonical telco contract — the same
// schema production adapters certify against. Not a stub of business logic.
//
// Fault catalogue (M1, deterministic and explicit — V2-SIM-006): behaviour is
// selected by markers in the msisdn_token, so a test or demo picks the
// scenario by choosing the subscriber, and the same request always replays
// the same outcome:
//
//	token contains "FAIL"    -> FAILED, nothing credited
//	token contains "TIMEOUT" -> TIMEOUT-AFTER-SUCCESS (EDG-005): the credit IS
//	                            recorded, but the response is held longer than
//	                            any sane client timeout; a later status enquiry
//	                            reveals SUCCESS. The classic double-credit trap.
//	otherwise                -> SUCCESS immediately
package sim

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

type FulfilmentRequest struct {
	PlatformRequestID   string `json:"platform_request_id"`
	SubscriberAccountID string `json:"subscriber_account_id"`
	MSISDNToken         string `json:"msisdn_token"`
	ProductType         string `json:"product_type"`
	FaceValueMinor      int64  `json:"face_value_minor"`
	Currency            string `json:"currency"`
	OfferSnapshotID     string `json:"offer_snapshot_id"`
	CallbackURL         string `json:"callback_url"`
}

type FulfilmentResponse struct {
	TelcoTransactionReference string `json:"telco_transaction_reference"`
	Status                    string `json:"status"`
}

// Transaction is the telco-side record — the source of truth reconciliation
// compares against (M1b-5), exposed via /sim/transactions.
type Transaction struct {
	PlatformRequestID string    `json:"platform_request_id"`
	TelcoReference    string    `json:"telco_transaction_reference"`
	MSISDNToken       string    `json:"msisdn_token"`
	FaceValueMinor    int64     `json:"face_value_minor"`
	Currency          string    `json:"currency"`
	Status            string    `json:"status"`
	CreditedAt        time.Time `json:"credited_at"`
}

type Simulator struct {
	Log  *slog.Logger
	Seed string
	// HoldDuration is how long TIMEOUT-scenario responses are held before
	// answering (must exceed the platform adapter's request timeout).
	HoldDuration time.Duration

	mu           sync.Mutex
	byIdemKey    map[string]FulfilmentResponse // V2-TEL-003 idempotent replay
	transactions map[string]Transaction        // by platform_request_id
}

func New(log *slog.Logger, seed string, hold time.Duration) *Simulator {
	return &Simulator{
		Log: log, Seed: seed, HoldDuration: hold,
		byIdemKey:    map[string]FulfilmentResponse{},
		transactions: map[string]Transaction{},
	}
}

// Handler returns the full canonical mux (shared by main and tests).
func (s *Simulator) Handler() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "seed": s.Seed})
	})
	mux.HandleFunc("POST /v1/telcos/{telcoId}/fulfilments", s.fulfil)
	mux.HandleFunc("GET /v1/telcos/{telcoId}/fulfilments/{platformRequestId}", s.enquire)
	mux.HandleFunc("GET /sim/transactions", s.listTransactions)
	return mux
}

func (s *Simulator) fulfil(w http.ResponseWriter, r *http.Request) {
	idemKey := r.Header.Get("Idempotency-Key")
	if idemKey == "" {
		http.Error(w, `{"error":"missing Idempotency-Key"}`, http.StatusBadRequest)
		return
	}
	var req FulfilmentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.PlatformRequestID == "" {
		http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	if prev, ok := s.byIdemKey[idemKey]; ok {
		s.mu.Unlock()
		s.Log.Info("duplicate fulfilment replayed", "idem_key", idemKey)
		writeJSON(w, http.StatusOK, prev)
		return
	}

	ref := fmt.Sprintf("SIM-%08x", stableHash(s.Seed+req.PlatformRequestID))
	token := req.MSISDNToken
	var resp FulfilmentResponse
	hold := false
	switch {
	case strings.Contains(token, "FAIL"):
		resp = FulfilmentResponse{TelcoTransactionReference: ref, Status: "FAILED"}
		s.transactions[req.PlatformRequestID] = Transaction{
			PlatformRequestID: req.PlatformRequestID, TelcoReference: ref,
			MSISDNToken: token, FaceValueMinor: req.FaceValueMinor,
			Currency: req.Currency, Status: "FAILED", CreditedAt: time.Now().UTC(),
		}
	case strings.Contains(token, "TIMEOUT"):
		// EDG-005: the credit HAPPENS — recorded before the response is held.
		resp = FulfilmentResponse{TelcoTransactionReference: ref, Status: "SUCCESS"}
		s.transactions[req.PlatformRequestID] = Transaction{
			PlatformRequestID: req.PlatformRequestID, TelcoReference: ref,
			MSISDNToken: token, FaceValueMinor: req.FaceValueMinor,
			Currency: req.Currency, Status: "SUCCESS", CreditedAt: time.Now().UTC(),
		}
		hold = true
	default:
		resp = FulfilmentResponse{TelcoTransactionReference: ref, Status: "SUCCESS"}
		s.transactions[req.PlatformRequestID] = Transaction{
			PlatformRequestID: req.PlatformRequestID, TelcoReference: ref,
			MSISDNToken: token, FaceValueMinor: req.FaceValueMinor,
			Currency: req.Currency, Status: "SUCCESS", CreditedAt: time.Now().UTC(),
		}
	}
	s.byIdemKey[idemKey] = resp
	s.mu.Unlock()

	if hold {
		s.Log.Warn("TIMEOUT scenario: credit recorded, holding response", "request", req.PlatformRequestID)
		select {
		case <-r.Context().Done(): // client gave up — exactly the point
			return
		case <-time.After(s.HoldDuration):
		}
	}
	s.Log.Info("fulfilment answered", "request", req.PlatformRequestID, "status", resp.Status, "ref", ref)
	writeJSON(w, http.StatusOK, resp)
}

func (s *Simulator) enquire(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("platformRequestId")
	s.mu.Lock()
	txn, ok := s.transactions[id]
	s.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such fulfilment"})
		return
	}
	writeJSON(w, http.StatusOK, FulfilmentResponse{
		TelcoTransactionReference: txn.TelcoReference, Status: txn.Status,
	})
}

func (s *Simulator) listTransactions(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	out := make([]Transaction, 0, len(s.transactions))
	for _, t := range s.transactions {
		out = append(out, t)
	}
	s.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].CreditedAt.Before(out[j].CreditedAt) })
	writeJSON(w, http.StatusOK, out)
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
