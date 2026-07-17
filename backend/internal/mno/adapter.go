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
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
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
}

func NewHTTPAdapter(cfg *configsvc.Service) *HTTPAdapter {
	return &HTTPAdapter{Config: cfg, HTTPClient: &http.Client{}}
}

type adapterCfg struct {
	FulfilmentURL    string `json:"fulfilment_url"`
	RequestTimeoutMs int    `json:"request_timeout_ms"`
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
		return Result{}, err
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
		return Result{}, err
	}

	callCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.RequestTimeoutMs)*time.Millisecond)
	defer cancel()

	url := cfg.FulfilmentURL + "/v1/telcos/" + telcoID + "/fulfilments"
	httpReq, err := http.NewRequestWithContext(callCtx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Idempotency-Key", telcoIdempotencyKey)

	res := Result{RequestEvidence: body}
	resp, err := a.HTTPClient.Do(httpReq)
	if err != nil {
		// Conservative classification (INV-009): once Do begins, the request
		// MAY have reached the telco (timeout-after-success is exactly this
		// shape). Unknown — the resolver enquires; a never-landed request
		// resolves to NOT_FOUND there and is then safely failed.
		res.Outcome = OutcomeUnknown
		res.ResponseEvidence = []byte(fmt.Sprintf(`{"transport_error":%q}`, err.Error()))
		return res, nil
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		res.Outcome = OutcomeUnknown
		res.ResponseEvidence = []byte(fmt.Sprintf(`{"read_error":%q}`, err.Error()))
		return res, nil
	}
	res.ResponseEvidence = raw

	if resp.StatusCode != http.StatusOK {
		// Non-2xx: the telco answered but did not accept. 4xx = definitively
		// rejected (Failed); 5xx = state unknowable (Unknown).
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
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
