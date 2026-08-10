// Package mno is the telco adapter framework: core services speak ONLY the
// canonical contract (V2-TAR-002); this package translates it to a concrete
// telco endpoint. The M1 HTTP adapter targets the simulator, which implements
// the same canonical contract real operators are certified against (V2-SIM-012).
//
// INV-009 is structural here: there is NO retry code path for fulfilment
// submission. A transport failure or timeout after the request may have been
// sent classifies as Unknown — the resolver worker owns ambiguity via status
// enquiry, never a repeat submission. The telco.adapter config validator
// force-rejects any nonzero retry_budget, and this client honors it by having
// no retry loop at all.
package mno

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform/egress"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
)

// Outcome classifies a fulfilment interaction (V2 Appendix D ambiguity matrix).
type Outcome string

const (
	OutcomeConfirmed Outcome = "CONFIRMED"
	OutcomeFailed    Outcome = "FAILED"
	// OutcomeUnknown: the instruction MAY have been received/executed. Blind
	// retry prohibited; resolve via EnquireStatus or reconciliation.
	OutcomeUnknown Outcome = "UNKNOWN"
	// OutcomeNotFound (enquiry only): telco has no record of the request —
	// the instruction provably never landed, safe to mark failed.
	OutcomeNotFound Outcome = "NOT_FOUND"
)

// definitiveRejection (M1B2-F2): the ONLY status codes treated as proof the
// telco rejected the instruction. Everything else non-2xx — including 408
// Request Timeout and 429 Too Many Requests, which aggregator edges emit while
// the backend may still be processing — classifies Unknown.
var definitiveRejection = map[int]bool{
	http.StatusBadRequest:          true, // 400
	http.StatusUnauthorized:        true, // 401
	http.StatusForbidden:           true, // 403
	http.StatusNotFound:            true, // 404
	http.StatusConflict:            true, // 409
	http.StatusUnprocessableEntity: true, // 422
}

type FulfilmentRequest struct {
	PlatformRequestID   string
	SubscriberAccountID string
	MSISDNToken         string
	ProductType         string
	FaceValue           entity.Money
	OfferSnapshotID     string
}

type Result struct {
	Outcome          Outcome
	TelcoReference   string
	RequestEvidence  []byte // exact wire request (V2-TEL-002)
	ResponseEvidence []byte // exact wire response or transport error
	// NotSent (BX-HIGH-010): the request was DEFINITIVELY not dialled (a pre-send
	// config / build / auth fault). A NotSent FAILED releases the reservation with no
	// enquiry — distinct from a business FAILED (telco rejected a request it received)
	// and from UNKNOWN (maybe-sent, exposure held pending an enquiry).
	NotSent bool
}

// Client is the canonical fulfilment interface the saga depends on.
type Client interface {
	// SubmitFulfilment sends the instruction ONCE. Never retries.
	SubmitFulfilment(ctx context.Context, telcoID, telcoIdempotencyKey string, req FulfilmentRequest) (Result, error)
	// EnquireStatus resolves ambiguity by platform request id (V2-TEL-011).
	EnquireStatus(ctx context.Context, telcoID, platformRequestID string) (Result, error)
}

// HTTPAdapter implements Client against the canonical HTTP contract
// (api/simulator-openapi.yaml). Endpoint + timeout come from the governed
// telco.adapter config at call time — an admin config change takes effect
// without redeploy (owner no-hardcoding directive).
type HTTPAdapter struct {
	Config *configsvc.Service
	// HTTPClient is injectable for tests; timeout comes from config per call.
	HTTPClient *http.Client

	// R-P0-8b: one circuit breaker per telco, created lazily from the telco's
	// governed circuit_* config on first use.
	mu       sync.Mutex
	breakers map[string]*breaker

	// Phase 1 S1: cached oauth2 client-credentials tokens, keyed by
	// telco|token_url|client_id, reused until shortly before expiry.
	tokMu  sync.Mutex
	tokens map[string]cachedToken
}

func NewHTTPAdapter(cfg *configsvc.Service) *HTTPAdapter {
	// SSRF egress guard (VR-32): 0 = no client timeout, the per-call context
	// deadline governs.
	return &HTTPAdapter{
		Config: cfg, HTTPClient: egress.SafeClient(0),
		breakers: map[string]*breaker{}, tokens: map[string]cachedToken{},
	}
}

type adapterCfg struct {
	FulfilmentURL          string   `json:"fulfilment_url"`
	RequestTimeoutMs       int      `json:"request_timeout_ms"`
	CircuitErrThreshPct    int      `json:"circuit_error_threshold_pct"`
	CircuitMinRequests     int      `json:"circuit_min_requests"`
	CircuitCooldownSeconds int      `json:"circuit_cooldown_seconds"`
	Auth                   *authCfg `json:"auth"` // Phase 1 S1: optional outbound partner auth
}

// breakerFor returns the telco's circuit breaker, creating it from config on
// first use. Circuit policy changes take effect on restart (acceptable for a
// stability control; documented).
func (a *HTTPAdapter) breakerFor(telcoID string, cfg adapterCfg) *breaker {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.breakers == nil {
		a.breakers = map[string]*breaker{}
	}
	b := a.breakers[telcoID]
	if b == nil {
		b = newBreaker(breakerCfg{
			thresholdPct: cfg.CircuitErrThreshPct,
			minRequests:  cfg.CircuitMinRequests,
			cooldown:     time.Duration(cfg.CircuitCooldownSeconds) * time.Second,
		})
		a.breakers[telcoID] = b
	}
	return b
}

func (a *HTTPAdapter) cfgFor(ctx context.Context, telcoID string) (adapterCfg, error) {
	cv, err := a.Config.ActiveAt(ctx, "telco.adapter", "telco:"+telcoID, time.Now().UTC())
	if err != nil {
		return adapterCfg{}, fmt.Errorf("telco.adapter config for %s: %w", telcoID, err)
	}
	var c adapterCfg
	if err := json.Unmarshal(cv.Content, &c); err != nil {
		return adapterCfg{}, fmt.Errorf("telco.adapter config parse: %w", err)
	}
	return c, nil
}

// AuthenticatedGet performs a GET to url with the telco's governed outbound partner
// auth applied (fail-closed) through the SSRF-safe egress client. The feature-feed
// ingest fetches through this so the feed is authenticated with the SAME governed
// credentials as fulfilment, never unauthenticated (BX-HIGH-006). A configured
// scheme whose secret is missing refuses the call rather than sending it in the
// clear. No circuit breaker: the feed pull is a batch operation, not a per-request
// money path.
func (a *HTTPAdapter) AuthenticatedGet(ctx context.Context, telcoID, url string) (*http.Response, error) {
	cfg, err := a.cfgFor(ctx, telcoID)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if err := a.applyAuth(ctx, telcoID, cfg, req); err != nil {
		return nil, err
	}
	return a.HTTPClient.Do(req)
}

type wireFulfilmentRequest struct {
	PlatformRequestID   string `json:"platform_request_id"`
	SubscriberAccountID string `json:"subscriber_account_id"`
	MSISDNToken         string `json:"msisdn_token"`
	ProductType         string `json:"product_type"`
	FaceValueMinor      int64  `json:"face_value_minor"`
	Currency            string `json:"currency"`
	OfferSnapshotID     string `json:"offer_snapshot_id"`
}

type wireFulfilmentResponse struct {
	TelcoTransactionReference string `json:"telco_transaction_reference"`
	Status                    string `json:"status"`
}

func (a *HTTPAdapter) SubmitFulfilment(ctx context.Context, telcoID, telcoIdempotencyKey string, req FulfilmentRequest) (Result, error) {
	cfg, err := a.cfgFor(ctx, telcoID)
	if err != nil {
		// BX-HIGH-010: a pre-send fault (nothing dialled) is a DEFINITE non-send, not an
		// ambiguous UNKNOWN — classify FAILED+NotSent so the saga releases the reservation
		// instead of holding it as a zombie awaiting a doomed enquiry.
		return Result{Outcome: OutcomeFailed, NotSent: true,
			ResponseEvidence: []byte(fmt.Sprintf(`{"not_sent":true,"reason":%q}`, "adapter config load failed: "+err.Error()))}, nil
	}
	body, err := json.Marshal(wireFulfilmentRequest{
		PlatformRequestID:   req.PlatformRequestID,
		SubscriberAccountID: req.SubscriberAccountID,
		MSISDNToken:         req.MSISDNToken,
		ProductType:         req.ProductType,
		FaceValueMinor:      req.FaceValue.Amount(),
		Currency:            string(req.FaceValue.Currency()),
		OfferSnapshotID:     req.OfferSnapshotID,
	})
	if err != nil {
		return Result{Outcome: OutcomeFailed, NotSent: true, // BX-HIGH-010: never dialled
			ResponseEvidence: []byte(fmt.Sprintf(`{"not_sent":true,"reason":%q}`, "request marshal failed: "+err.Error()))}, nil
	}

	res := Result{RequestEvidence: body}

	// R-P0-8b + BX-HIGH-010: if this telco's circuit is OPEN, do NOT dial a down telco —
	// short-circuit BEFORE HTTPClient.Do. This is a DETERMINISTIC non-send: nothing reached
	// the telco, so classify FAILED+NotSent and let the saga RELEASE the reservation (a safe
	// decline; the subscriber retries once the telco recovers). Mapping it to UNKNOWN would
	// hold exposure as a zombie awaiting an enquiry that can only find NOT_FOUND — the telco
	// has no record of a request we never sent. INV-009 forbids blind RETRY of a maybe-sent
	// request; a definitely-not-sent request is a fresh decline, not a retry, so release is safe.
	br := a.breakerFor(telcoID, cfg)
	if !br.allow() {
		res.Outcome = OutcomeFailed
		res.NotSent = true
		res.ResponseEvidence = []byte(`{"not_sent":true,"reason":"circuit_open"}`)
		return res, nil
	}

	callCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.RequestTimeoutMs)*time.Millisecond)
	defer cancel()

	url := cfg.FulfilmentURL + "/v1/telcos/" + telcoID + "/fulfilments"
	httpReq, err := http.NewRequestWithContext(callCtx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		res.Outcome = OutcomeFailed // BX-HIGH-010: never dialled
		res.NotSent = true
		res.ResponseEvidence = []byte(fmt.Sprintf(`{"not_sent":true,"reason":%q}`, "build request failed: "+err.Error()))
		return res, nil
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Idempotency-Key", telcoIdempotencyKey)
	// Phase 1 S1: apply partner auth (fail-closed) BEFORE sending — never dial the telco
	// unauthenticated. BX-HIGH-010: nothing has been sent, so a missing secret is a clean
	// non-send — FAILED+NotSent so the saga releases the reservation (safe decline), never
	// a zombie UNKNOWN that holds exposure awaiting an enquiry for a request never sent.
	if err := a.applyAuth(callCtx, telcoID, cfg, httpReq); err != nil {
		res.Outcome = OutcomeFailed
		res.NotSent = true
		res.ResponseEvidence = []byte(fmt.Sprintf(`{"not_sent":true,"reason":%q}`, "outbound auth failed: "+err.Error()))
		return res, nil
	}
	resp, err := a.HTTPClient.Do(httpReq)
	if err != nil {
		// Conservative classification (INV-009): once Do begins, the request
		// MAY have reached the telco (timeout-after-success is exactly this
		// shape). Unknown — the resolver enquires; a never-landed request
		// resolves to NOT_FOUND there and is then safely failed.
		br.record(false) // R-P0-8b: no response = a health failure
		res.Outcome = OutcomeUnknown
		res.ResponseEvidence = []byte(fmt.Sprintf(`{"transport_error":%q}`, err.Error()))
		return res, nil
	}
	defer func() { _ = resp.Body.Close() }()
	// R-P0-8b: the telco RESPONDED (even a 4xx) unless it 5xx'd — a responsive
	// telco, business-FAILED included, is healthy and must not trip the breaker.
	br.record(resp.StatusCode < 500)
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		res.Outcome = OutcomeUnknown
		res.ResponseEvidence = []byte(fmt.Sprintf(`{"read_error":%q}`, err.Error()))
		return res, nil
	}
	res.ResponseEvidence = raw

	if resp.StatusCode != http.StatusOK {
		// M1B2-F2: "Failed" is an explicit allowlist of DEFINITIVE-rejection
		// codes. A 408/429 (or anything else) can come from an aggregator's
		// edge gateway while the telco backend is still processing — exactly
		// the double-credit shape this architecture guards against. Everything
		// not provably rejected is Unknown for the enquiry path.
		if definitiveRejection[resp.StatusCode] {
			res.Outcome = OutcomeFailed
		} else {
			res.Outcome = OutcomeUnknown
		}
		return res, nil
	}
	var w wireFulfilmentResponse
	if err := json.Unmarshal(raw, &w); err != nil {
		res.Outcome = OutcomeUnknown // answered 200 with garbage: treat as ambiguity, not failure
		return res, nil
	}
	res.TelcoReference = w.TelcoTransactionReference
	switch w.Status {
	case "SUCCESS":
		res.Outcome = OutcomeConfirmed
	case "FAILED":
		res.Outcome = OutcomeFailed
	default:
		// PENDING or unrecognised codes are quarantined as Unknown, never
		// silently mapped (V2-TEL-009).
		res.Outcome = OutcomeUnknown
	}
	return res, nil
}

var errMissingConfig = errors.New("mno: adapter config missing")

func (a *HTTPAdapter) EnquireStatus(ctx context.Context, telcoID, platformRequestID string) (Result, error) {
	cfg, err := a.cfgFor(ctx, telcoID)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %s", errMissingConfig, err)
	}
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.RequestTimeoutMs)*time.Millisecond)
	defer cancel()

	url := cfg.FulfilmentURL + "/v1/telcos/" + telcoID + "/fulfilments/" + platformRequestID
	httpReq, err := http.NewRequestWithContext(callCtx, http.MethodGet, url, nil)
	if err != nil {
		return Result{}, err
	}
	res := Result{}
	// Phase 1 S1: apply partner auth (fail-closed) before the enquiry too.
	if err := a.applyAuth(callCtx, telcoID, cfg, httpReq); err != nil {
		return Result{}, err
	}
	resp, err := a.HTTPClient.Do(httpReq)
	if err != nil {
		// Enquiry itself failed: still Unknown; the resolver retries enquiry
		// on its config backoff (enquiries are read-only and safe to repeat).
		res.Outcome = OutcomeUnknown
		res.ResponseEvidence = []byte(fmt.Sprintf(`{"transport_error":%q}`, err.Error()))
		return res, nil
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	res.ResponseEvidence = raw

	switch resp.StatusCode {
	case http.StatusNotFound:
		res.Outcome = OutcomeNotFound
	case http.StatusOK:
		var w wireFulfilmentResponse
		if err := json.Unmarshal(raw, &w); err != nil {
			res.Outcome = OutcomeUnknown
			return res, nil
		}
		res.TelcoReference = w.TelcoTransactionReference
		switch w.Status {
		case "SUCCESS":
			res.Outcome = OutcomeConfirmed
		case "FAILED":
			res.Outcome = OutcomeFailed
		default:
			res.Outcome = OutcomeUnknown
		}
	default:
		res.Outcome = OutcomeUnknown
	}
	return res, nil
}
