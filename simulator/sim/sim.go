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

// FeatureRow is one subscriber's row in the canonical batch feature file
// (V2-SCR-001/002). All quantities are integers: minor units, day counts —
// the scoring perimeter is float-free (BC-1).
type FeatureRow struct {
	MSISDNToken         string   `json:"msisdn_token"`
	TenureDays          int      `json:"tenure_days"`
	ActivityDays30d     int      `json:"activity_days_30d"`
	ActiveDays90d       int      `json:"active_days_90d"`
	WeeklyRechargeMinor []int64  `json:"weekly_recharge_minor"` // 13 weeks, most recent first
	Currency            string   `json:"currency"`
	QualityFlags        []string `json:"quality_flags,omitempty"`
}

// FeatureFile is the canonical batch file shape.
type FeatureFile struct {
	TelcoID string       `json:"telco_id"`
	AsOf    time.Time    `json:"as_of"`
	Rows    []FeatureRow `json:"rows"`
}

type Simulator struct {
	Log  *slog.Logger
	Seed string
	// HoldDuration is how long TIMEOUT-scenario responses are held before
	// answering (must exceed the platform adapter's request timeout).
	HoldDuration time.Duration

	mu            sync.Mutex
	byIdemKey     map[string]FulfilmentResponse // V2-TEL-003 idempotent replay
	transactions  map[string]Transaction        // by platform_request_id
	holdEnquiries bool                          // fault: enquiry route unresponsive
	smsByIdemKey  map[string]string             // idem key -> provider ref
	smsLog        []SMSRecord
}

// CreditDirect injects a SUCCESS transaction as if the telco credited without
// the platform ever hearing back — the crash-after-telco-success shape
// (EDG-007) for certification tests (V2-SIM-002 fault catalogue).
func (s *Simulator) CreditDirect(platformRequestID string, faceValueMinor int64, currency string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ref := fmt.Sprintf("SIM-%08x", stableHash(s.Seed+platformRequestID))
	s.transactions[platformRequestID] = Transaction{
		PlatformRequestID: platformRequestID, TelcoReference: ref,
		FaceValueMinor: faceValueMinor, Currency: currency,
		Status: "SUCCESS", CreditedAt: time.Now().UTC(),
	}
}

// HoldEnquiries toggles the enquiry-route-unresponsive fault: status
// enquiries hang past any sane client timeout (aggregator edge outage —
// the still-unknown resolver cycle).
func (s *Simulator) HoldEnquiries(hold bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.holdEnquiries = hold
}

func New(log *slog.Logger, seed string, hold time.Duration) *Simulator {
	return &Simulator{
		Log: log, Seed: seed, HoldDuration: hold,
		byIdemKey:    map[string]FulfilmentResponse{},
		transactions: map[string]Transaction{},
		smsByIdemKey: map[string]string{},
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
	mux.HandleFunc("GET /v1/telcos/{telcoId}/feature-file", s.featureFile)
	mux.HandleFunc("POST /v1/telcos/{telcoId}/sms", s.sendSMS)
	mux.HandleFunc("GET /sim/sms", s.listSMS)
	mux.HandleFunc("GET /sim/transactions", s.listTransactions)
	return mux
}

// SMSRequest is the canonical SMS submission (V2 §10.2 notifications).
type SMSRequest struct {
	MSISDNToken string `json:"msisdn_token"`
	SenderID    string `json:"sender_id"`
	Body        string `json:"body"`
}

// SMSRecord is the telco-side evidence of a delivered message.
type SMSRecord struct {
	ProviderRef string    `json:"provider_ref"`
	MSISDNToken string    `json:"msisdn_token"`
	SenderID    string    `json:"sender_id"`
	Body        string    `json:"body"`
	ReceivedAt  time.Time `json:"received_at"`
}

// sendSMS accepts a message; idempotent per Idempotency-Key like fulfilment.
func (s *Simulator) sendSMS(w http.ResponseWriter, r *http.Request) {
	idemKey := r.Header.Get("Idempotency-Key")
	if idemKey == "" {
		http.Error(w, `{"error":"missing Idempotency-Key"}`, http.StatusBadRequest)
		return
	}
	var req SMSRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.MSISDNToken == "" || req.Body == "" {
		http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if prev, ok := s.smsByIdemKey[idemKey]; ok {
		writeJSON(w, http.StatusOK, map[string]string{"provider_ref": prev})
		return
	}
	ref := fmt.Sprintf("SMS-%08x", stableHash(s.Seed+idemKey))
	s.smsByIdemKey[idemKey] = ref
	s.smsLog = append(s.smsLog, SMSRecord{
		ProviderRef: ref, MSISDNToken: req.MSISDNToken, SenderID: req.SenderID,
		Body: req.Body, ReceivedAt: time.Now().UTC(),
	})
	writeJSON(w, http.StatusOK, map[string]string{"provider_ref": ref})
}

func (s *Simulator) listSMS(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	out := make([]SMSRecord, len(s.smsLog))
	copy(out, s.smsLog)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, out)
}

// featureFile serves the canonical batch feature file (V2-SCR-001). Content
// is DETERMINISTIC in (seed, count, as_of-date): fetching the same day's file
// twice yields byte-identical content, so platform-side file dedup engages.
// Profiles are index-derived so tests pick scenarios by row position:
//
//	every 13th row -> SPIKY: one enormous recharge week (EDG-013 material)
//	every 7th row  -> THIN:  short tenure, few active days (cold start)
//	?malformed=1   -> appends one contract-violating row (negative amount)
//	                  to exercise quarantine, never silent drops
func (s *Simulator) featureFile(w http.ResponseWriter, r *http.Request) {
	count := 100
	if c := r.URL.Query().Get("count"); c != "" {
		if _, err := fmt.Sscanf(c, "%d", &count); err != nil || count < 1 || count > 2_000_000 {
			http.Error(w, `{"error":"count must be 1..2000000"}`, http.StatusBadRequest)
			return
		}
	}
	asOf := time.Now().UTC().Truncate(24 * time.Hour)
	if q := r.URL.Query().Get("as_of"); q != "" {
		t, err := time.Parse("2006-01-02", q)
		if err != nil {
			http.Error(w, `{"error":"as_of must be YYYY-MM-DD"}`, http.StatusBadRequest)
			return
		}
		asOf = t.UTC()
	}

	file := FeatureFile{TelcoID: r.PathValue("telcoId"), AsOf: asOf, Rows: make([]FeatureRow, 0, count)}
	for i := 1; i <= count; i++ {
		row := s.featureRow(i)
		// M4e-3 fault-demo pool: a few deterministic rows carry FAULT-SHAPED
		// tokens (the fulfilment route faults on token substring, V2-SIM-002)
		// but ORDINARY healthy histories, so the real scoring pipeline makes
		// them eligible and the portal demo can originate against them.
		// Indexes chosen off the special classes (not %7 / %13).
		if tok, ok := demoTokens[i]; ok {
			row.MSISDNToken = tok
		}
		file.Rows = append(file.Rows, row)
	}
	if r.URL.Query().Get("malformed") == "1" {
		// #nosec G101 -- not a credential: a synthetic tokenised-MSISDN row id
		// for the contract-violation fault (negative amount exercises the
		// platform's quarantine path). G101 pattern-matches the field name.
		file.Rows = append(file.Rows, FeatureRow{
			MSISDNToken: "tok_sim_malformed", TenureDays: 100, ActivityDays30d: 10,
			ActiveDays90d: 30, WeeklyRechargeMinor: []int64{-500}, Currency: "NGN",
		})
	}
	if r.URL.Query().Get("corrupt") == "1" {
		// G2-F3 fault: a structurally VALID row carrying an absurd value near
		// int64-max (feed corruption / unit error). Must be quarantined by
		// the platform's plausibility ceiling — it would otherwise overflow
		// spike-ratio arithmetic and score a garbage tier.
		weekly := make([]int64, 13)
		weekly[0] = int64(1)<<62 + 12345
		// #nosec G101 -- synthetic tokenised-MSISDN row id, not a credential.
		file.Rows = append(file.Rows, FeatureRow{
			MSISDNToken: "tok_sim_corrupt", TenureDays: 400, ActivityDays30d: 20,
			ActiveDays90d: 60, WeeklyRechargeMinor: weekly, Currency: "NGN",
		})
	}
	writeJSON(w, http.StatusOK, file)
}

// demoTokens maps feature-file row indexes to the fault-demo token pool
// (M4e-3). All indexes avoid the SPIKY (%13) and THIN (%7) special classes so
// the pool scores eligible. FAIL tokens trigger the hard-fail fulfilment
// fault; TIMEOUT tokens the credit-then-hang EDG-005 fault; the demo_ok
// tokens are plain (dedicated so demos never collide with test subscribers).
var demoTokens = map[int]string{
	80: "tok_sim_demo_ok_01", 82: "tok_sim_demo_ok_02", 83: "tok_sim_demo_ok_03",
	92: "tok_sim_demo_FAIL_01", 94: "tok_sim_demo_FAIL_02", 95: "tok_sim_demo_FAIL_03",
	96: "tok_sim_demo_TIMEOUT_01", 97: "tok_sim_demo_TIMEOUT_02", 99: "tok_sim_demo_TIMEOUT_03",
}

// featureRow derives one subscriber's deterministic features from the seed
// and row index.
func (s *Simulator) featureRow(i int) FeatureRow {
	h := int64(stableHash(fmt.Sprintf("%s/features/%d", s.Seed, i)))
	row := FeatureRow{
		MSISDNToken:     fmt.Sprintf("tok_sim_%04d", i),
		TenureDays:      180 + int(h%1500),
		ActivityDays30d: 15 + int(h%16),
		ActiveDays90d:   45 + int(h%46),
		Currency:        "NGN",
	}
	weekly := make([]int64, 13)
	base := 5_000 + (h % 45_000) // ₦50..₦500 per week in kobo
	for w := range weekly {
		weekly[w] = base + (h>>uint(w%8))%7_000
	}
	switch {
	case i%13 == 0: // SPIKY: one giant week on an otherwise modest pattern
		weekly[0] = base * 40
	case i%7 == 0: // THIN: new subscriber, sparse history
		row.TenureDays = 10 + int(h%60)
		row.ActivityDays30d = int(h % 8)
		row.ActiveDays90d = int(h % 12)
		for w := 4; w < 13; w++ {
			weekly[w] = 0
		}
		row.QualityFlags = []string{"SHORT_HISTORY"}
	}
	if i%17 == 0 { // MISSING: telco reports source gaps behind this row (V2-SCR-017)
		row.QualityFlags = append(row.QualityFlags, "MISSING_FIELDS")
	}
	row.WeeklyRechargeMinor = weekly
	return row
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
	hold := s.holdEnquiries
	s.mu.Unlock()
	if hold {
		s.Log.Warn("HOLD-ENQUIRIES fault: enquiry hanging", "request", id)
		select {
		case <-r.Context().Done():
			return
		case <-time.After(s.HoldDuration):
		}
	}
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
