package handler

// Channel + recovery HTTP surface (M1b-3 tail). BC-7: typed domain errors map
// to stable error-code families exactly once, here. BC-6: the correlation id
// is minted/accepted at this edge and rides every downstream artifact.
// V2-ADV-016: internal ambiguity is never exposed — customers see a safe
// status with a durable status route.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/origination"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/recovery"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/treasury"
)

const headerCorrelationID = "X-Correlation-Id"
const headerIdempotencyKey = "Idempotency-Key"

// Correlation wraps a handler with BC-6 correlation propagation: accept the
// caller's id or mint one; always echo it on the response.
//
// VR10-F1: the caller-supplied id is BOUNDED (<=64 chars, [A-Za-z0-9_.-])
// before it can persist into immutable journal rows and logs — anything
// outside the envelope is discarded and re-minted, never truncated (a
// truncated id is a lineage lie).
func Correlation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cor := r.Header.Get(headerCorrelationID)
		if !validCorrelationID(cor) {
			cor = platform.NewID("cor")
		}
		w.Header().Set(headerCorrelationID, cor)
		next.ServeHTTP(w, r.WithContext(platform.WithCorrelation(r.Context(), cor)))
	})
}

func validCorrelationID(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '_', r == '-', r == '.':
		default:
			return false
		}
	}
	return true
}

// Channel serves the customer-facing credit journey.
type Channel struct {
	Origination *origination.Service
	Recovery    *recovery.Service
	Log         *slog.Logger
}

// Mount registers routes; auth resolves the tenant, Correlation the lineage.
func (h *Channel) Mount(mux *http.ServeMux, auth *TenantAuth) {
	wrap := func(fn http.HandlerFunc) http.Handler { return auth.Wrap(Correlation(fn)) }
	mux.Handle("GET /v1/offers", wrap(h.getOffers))
	mux.Handle("POST /v1/advances", wrap(h.confirm))
	mux.Handle("GET /v1/advances/{id}", wrap(h.advanceStatus))
	mux.Handle("POST /v1/recovery/events", wrap(h.recoveryEvent))
}

// --- wire shapes -----------------------------------------------------------

type offerResponse struct {
	OfferID   string       `json:"offer_id"`
	FaceValue entity.Money `json:"face_value"`
	Fee       entity.Money `json:"fee"`
	Disbursed entity.Money `json:"disbursed"`
	Repayment entity.Money `json:"repayment"`
	FeeModel  string       `json:"fee_model"`
	ExpiresAt time.Time    `json:"expires_at"`
}

type confirmRequest struct {
	ProgrammeID string `json:"programme_id"`
	OfferID     string `json:"offer_id"`
	MSISDNToken string `json:"msisdn_token"`
}

type advanceResponse struct {
	AdvanceID   string       `json:"advance_id"`
	Status      string       `json:"status"` // customer-safe (V2-ADV-016)
	FaceValue   entity.Money `json:"face_value"`
	Disbursed   entity.Money `json:"disbursed"`
	Outstanding entity.Money `json:"outstanding"`
	StatusRoute string       `json:"status_route"` // EDG-004: durable status URL
}

type recoveryEventRequest struct {
	SourceEventID string       `json:"source_event_id"`
	MSISDNToken   string       `json:"msisdn_token"`
	Amount        entity.Money `json:"amount"`
	OccurredAt    time.Time    `json:"occurred_at"`
}

type recoveryEventResponse struct {
	RecoveryEventID string        `json:"recovery_event_id"`
	State           string        `json:"state"`
	Applied         *entity.Money `json:"applied,omitempty"`
	Excess          *entity.Money `json:"excess,omitempty"`
	AdvanceClosed   bool          `json:"advance_closed"`
	Replayed        bool          `json:"replayed"`
}

// customerStatus maps internal FSM states to the customer-safe vocabulary
// (V2-ADV-016): ambiguity and pre-fulfilment states read as PROCESSING.
func customerStatus(s entity.AdvanceState) string {
	switch s {
	case entity.AdvRequested, entity.AdvValidated, entity.AdvExposureReserved,
		entity.AdvPendingFulfilment, entity.AdvFulfilmentUnknown:
		return "PROCESSING"
	case entity.AdvFulfilmentFailed, entity.AdvDeclined:
		return "FAILED"
	default:
		return string(s) // ACTIVE, PARTIALLY_RECOVERED, CLOSED
	}
}

func toAdvanceResponse(a entity.Advance) advanceResponse {
	return advanceResponse{
		AdvanceID:   a.AdvanceID,
		Status:      customerStatus(a.State),
		FaceValue:   a.FaceValue,
		Disbursed:   a.Disbursed,
		Outstanding: a.Outstanding,
		StatusRoute: "/v1/advances/" + a.AdvanceID,
	}
}

// --- handlers --------------------------------------------------------------

func (h *Channel) getOffers(w http.ResponseWriter, r *http.Request) {
	programmeID := r.URL.Query().Get("programme_id")
	token := r.URL.Query().Get("msisdn_token")
	if programmeID == "" || token == "" {
		writeErr(w, http.StatusBadRequest, "OFFER_BAD_REQUEST", "programme_id and msisdn_token are required")
		return
	}
	offers, err := h.Origination.GetOffers(r.Context(), programmeID, token)
	if err != nil {
		h.writeDomainErr(w, r, err)
		return
	}
	out := make([]offerResponse, 0, len(offers))
	for _, o := range offers {
		out = append(out, offerResponse{
			OfferID: o.OfferID, FaceValue: o.FaceValue, Fee: o.Fee,
			Disbursed: o.Disbursed, Repayment: o.Repayment,
			FeeModel: string(o.FeeModel), ExpiresAt: o.ExpiresAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Channel) confirm(w http.ResponseWriter, r *http.Request) {
	idemKey := r.Header.Get(headerIdempotencyKey)
	if len(idemKey) < 8 || len(idemKey) > 128 {
		// V2-API-002: mutating commands REQUIRE a usable idempotency key.
		writeErr(w, http.StatusBadRequest, "ADVANCE_IDEMPOTENCY_KEY_REQUIRED",
			"Idempotency-Key header (8..128 chars) is required")
		return
	}
	var req confirmRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "ADVANCE_BAD_REQUEST", "malformed JSON body")
		return
	}
	res, err := h.Origination.Confirm(r.Context(), origination.ConfirmCmd{
		ProgrammeID:   req.ProgrammeID,
		OfferID:       req.OfferID,
		MSISDNToken:   req.MSISDNToken,
		IdemKey:       idemKey,
		CorrelationID: platform.CorrelationFrom(r.Context()),
	})
	if err != nil {
		h.writeDomainErr(w, r, err)
		return
	}
	status := http.StatusCreated
	switch {
	case res.Replayed:
		status = http.StatusOK // EDG-001: replay returns the original outcome
	case res.Advance.State == entity.AdvFulfilmentUnknown:
		status = http.StatusAccepted // EDG-004: safe pending + status route
	case res.Advance.State == entity.AdvFulfilmentFailed:
		status = http.StatusUnprocessableEntity
	}
	writeJSON(w, status, toAdvanceResponse(res.Advance))
}

func (h *Channel) advanceStatus(w http.ResponseWriter, r *http.Request) {
	adv, err := h.Origination.GetAdvance(r.Context(), r.PathValue("id"))
	if errors.Is(err, repo.ErrNotFound) {
		// VR10-F2: this route's 404 is about the ADVANCE, not an offer.
		writeErr(w, http.StatusNotFound, "ADVANCE_NOT_FOUND", "advance not found")
		return
	}
	if err != nil {
		h.writeDomainErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toAdvanceResponse(adv))
}

func (h *Channel) recoveryEvent(w http.ResponseWriter, r *http.Request) {
	var req recoveryEventRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "RECOVERY_BAD_REQUEST", "malformed JSON body")
		return
	}
	out, err := h.Recovery.Ingest(r.Context(), recovery.IngestCmd{
		SourceEventID: req.SourceEventID,
		MSISDNToken:   req.MSISDNToken,
		Amount:        req.Amount,
		OccurredAt:    req.OccurredAt,
		CorrelationID: platform.CorrelationFrom(r.Context()),
	})
	if err != nil {
		h.writeDomainErr(w, r, err)
		return
	}
	resp := recoveryEventResponse{
		RecoveryEventID: out.RecoveryEventID,
		State:           string(out.State),
		AdvanceClosed:   out.AdvanceClosed,
		Replayed:        out.Replayed,
	}
	if out.Applied.IsSet() {
		resp.Applied = &out.Applied
	}
	if out.Excess.IsSet() {
		resp.Excess = &out.Excess
	}
	writeJSON(w, http.StatusOK, resp)
}

// writeDomainErr is THE typed-error boundary (BC-7): every domain error family
// renders its documented envelope; anything unmapped is a 500 with no detail
// leaked (V2-API-011).
func (h *Channel) writeDomainErr(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, origination.ErrOfferNotFound), errors.Is(err, repo.ErrNotFound):
		writeErr(w, http.StatusNotFound, "OFFER_NOT_FOUND", "offer or resource not found")
	case errors.Is(err, origination.ErrOfferExpired):
		writeErr(w, http.StatusConflict, "OFFER_EXPIRED", "offer expired; request fresh offers")
	case errors.Is(err, origination.ErrOfferNotAcceptable):
		writeErr(w, http.StatusConflict, "OFFER_SNAPSHOT_MISMATCH", "offer no longer acceptable; request fresh offers")
	case errors.Is(err, origination.ErrDivergentDuplicate):
		// R-P0-1: same idempotency key, different request. Not a replay —
		// the client must use a fresh key for a new command.
		writeErr(w, http.StatusConflict, "DIVERGENT_DUPLICATE", "idempotency key already used for a different request; use a fresh key")
	case errors.Is(err, recovery.ErrDivergentRecovery):
		// R-P0-2: same source_event_id, different payload — refused, audited.
		writeErr(w, http.StatusConflict, "DIVERGENT_DUPLICATE", "source event id already used for a different payload")
	case errors.Is(err, origination.ErrSubscriberIneligible):
		writeErr(w, http.StatusForbidden, "SUBSCRIBER_INACTIVE", "subscriber not eligible")
	case errors.Is(err, repo.ErrConcurrentAdvanceBlocked):
		writeErr(w, http.StatusConflict, "CONCURRENT_ADVANCE_BLOCK", "an advance is already open for this subscriber")
	case errors.Is(err, repo.ErrOfferAlreadyUsed):
		writeErr(w, http.StatusConflict, "OFFER_SNAPSHOT_MISMATCH", "offer already used")
	case errors.Is(err, repo.ErrNoFundingCapacity):
		// V2 §6.2 FUNDING_*: retry only after treasury state changes.
		writeErr(w, http.StatusConflict, "FUNDING_POOL_EXHAUSTED", "no funding capacity available")
	case errors.Is(err, origination.ErrDecisionUnavailable):
		// EDG-014 boundary: stale/absent decision is a customer-safe no-offer.
		writeErr(w, http.StatusConflict, "NO_OFFER_AVAILABLE", "no offer available at this time")
	case errors.Is(err, origination.ErrOverlayBlocked):
		// V2-SCR-015: which flag fired is logged upstream, never disclosed.
		writeErr(w, http.StatusForbidden, "SERVICE_RESTRICTED", "service not available for this subscriber right now")
	case errors.Is(err, treasury.ErrProgrammeSuspended):
		// M3d fail-closed: lending stopped (guardrail trip or operator).
		writeErr(w, http.StatusServiceUnavailable, "SERVICE_TEMPORARILY_LIMITED", "service temporarily limited; try again later")
	case errors.Is(err, context.DeadlineExceeded):
		writeErr(w, http.StatusGatewayTimeout, "SYSTEM_TEMPORARILY_UNAVAILABLE", "timeout")
	default:
		h.Log.Error("unmapped domain error", "err", err, "path", r.URL.Path)
		writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
	}
}
