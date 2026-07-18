package handler

// M4c risk workspace: guardrail-trip visibility and the two-person re-arm
// (request -> approve, distinct actor schema-enforced) through the portal.
//
// This is the portal's FIRST tenant-data surface. Guardrail trips are
// telco-scoped, so authorization is the OPERATOR'S SCOPE, applied as a
// mandatory filter on cross-tenant reads and a per-trip check on every action
// (a cross-scope lookup returns a no-oracle 404, never a 403 that leaks
// existence). No money arithmetic happens client-side — amounts are sent as
// minor units plus a server-formatted display string.

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
)

type moneyView struct {
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
	Display     string `json:"display"` // server-formatted; the UI never computes money
}

// groupMinor renders an exact minor-unit integer with thousands separators —
// pure grouping of the canonical value, no division and no per-currency
// decimals assumption (the platform has no governed currency-decimals source
// yet; a major-unit operator display is a follow-up that must read that source
// rather than hardcode an exponent). Exact and legible.
func groupMinor(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := strconv.FormatInt(n, 10)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

func toMoneyView(m entity.Money) moneyView {
	return moneyView{
		AmountMinor: m.Amount(),
		Currency:    string(m.Currency()),
		Display:     string(m.Currency()) + " " + groupMinor(m.Amount()) + " (minor)",
	}
}

type tripResponse struct {
	TripID           string    `json:"trip_id"`
	TelcoID          string    `json:"telco_id"`
	ProgrammeID      string    `json:"programme_id"`
	Guardrail        string    `json:"guardrail"`
	Measured         moneyView `json:"measured"`
	Limit            moneyView `json:"limit"`
	State            string    `json:"state"`
	TrippedAt        time.Time `json:"tripped_at"`
	RearmRequestedBy string    `json:"rearm_requested_by,omitempty"`
	RearmApprovedBy  string    `json:"rearm_approved_by,omitempty"`
}

func toTripResponse(t repo.GuardrailTrip) tripResponse {
	return tripResponse{
		TripID: t.TripID, TelcoID: t.TelcoID, ProgrammeID: t.ProgrammeID,
		Guardrail: t.Guardrail,
		Measured:  toMoneyView(t.Measured),
		Limit:     toMoneyView(t.Limit),
		State:     t.State, TrippedAt: t.TrippedAt,
		RearmRequestedBy: t.RearmRequestedBy, RearmApprovedBy: t.RearmApprovedBy,
	}
}

// riskTrips lists open guardrail trips bounded to the operator's scope.
func (p *Portal) riskTrips(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	telco, programme, authority := sess.TenantFilter()
	if !authority {
		// A 'global'-only operator has no tenant authority — empty, not an error.
		writeJSON(w, http.StatusOK, map[string]any{"trips": []tripResponse{}})
		return
	}
	trips, err := repo.ListOpenTrips(r.Context(), p.ReadPool, telco, programme)
	if err != nil {
		p.Log.Error("portal risk trips list", "err", err)
		writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
		return
	}
	out := make([]tripResponse, 0, len(trips))
	for _, t := range trips {
		out = append(out, toTripResponse(t))
	}
	writeJSON(w, http.StatusOK, map[string]any{"trips": out})
}

// loadTripScoped loads a trip and authorizes it against the session scope,
// writing a no-oracle 404 and returning ok=false when out of scope or absent.
func (p *Portal) loadTripScoped(w http.ResponseWriter, r *http.Request) (repo.GuardrailTrip, bool) {
	sess := sessionFrom(r.Context())
	trip, err := repo.GetTripByID(r.Context(), p.ReadPool, r.PathValue("id"))
	if err != nil {
		p.writeRiskErr(w, err)
		return repo.GuardrailTrip{}, false
	}
	if !sess.PermitsTenant(trip.TelcoID, trip.ProgrammeID) {
		p.Log.Warn("portal scope refusal (trip)", "actor", sess.Actor, "session_scope", sess.Scope,
			"trip", trip.TripID, "telco", trip.TelcoID, "programme", trip.ProgrammeID)
		writeErr(w, http.StatusNotFound, "TRIP_NOT_FOUND", "guardrail trip not found")
		return repo.GuardrailTrip{}, false
	}
	return trip, true
}

// riskRequestRearm is the maker step (TRIPPED -> REARM_REQUESTED).
func (p *Portal) riskRequestRearm(w http.ResponseWriter, r *http.Request) {
	trip, ok := p.loadTripScoped(w, r)
	if !ok {
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&req); err != nil || req.Reason == "" {
		writeErr(w, http.StatusBadRequest, "PORTAL_BAD_REQUEST", "reason is required")
		return
	}
	sess := sessionFrom(r.Context())
	if err := p.Treasury.RequestRearm(r.Context(), trip.TelcoID, trip.TripID, sess.Actor, req.Reason); err != nil {
		p.writeRiskErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"trip_id": trip.TripID, "state": "REARM_REQUESTED"})
}

// riskApproveRearm is the checker step (REARM_REQUESTED -> REARMED). The
// distinct-actor rule is schema-enforced; a same-actor approval is 409.
func (p *Portal) riskApproveRearm(w http.ResponseWriter, r *http.Request) {
	trip, ok := p.loadTripScoped(w, r)
	if !ok {
		return
	}
	sess := sessionFrom(r.Context())
	if err := p.Treasury.ApproveRearm(r.Context(), trip.TelcoID, trip.TripID, sess.Actor); err != nil {
		p.writeRiskErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"trip_id": trip.TripID, "state": "REARMED"})
}

func (p *Portal) writeRiskErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, repo.ErrSelfRearm):
		writeErr(w, http.StatusConflict, "GUARDRAIL_TWO_PERSON", "re-arm approver must differ from the requester")
	case errors.Is(err, repo.ErrNotFound):
		// Absent id, out-of-state trip, or (from loadTripScoped) out-of-scope —
		// all indistinguishable, no existence oracle.
		writeErr(w, http.StatusNotFound, "TRIP_NOT_FOUND", "guardrail trip not found, or not in the required state")
	default:
		p.Log.Error("portal risk error", "err", err)
		writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
	}
}
