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
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/jackc/pgx/v5"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
)

// moneyView is the portal's money shape. amount_minor is serialised as a STRING (`,string`) —
// BX-MED-008: it is an int64 (kobo), and a portfolio-level aggregate (loan-book totals, ledger
// receivable) can exceed JavaScript's 2^53 safe-integer limit, where a JSON number silently loses
// precision. A string round-trips the exact integer to the browser (parse as needed). `display` is
// the exact server-formatted value the UI renders; amount_minor is for the rare programmatic use.
// The channel API's entity.Money keeps a numeric amount_minor: its consumers are telcos parsing
// int64 natively, not JavaScript.
type moneyView struct {
	AmountMinor int64  `json:"amount_minor,string"`
	Currency    string `json:"currency"`
	Display     string `json:"display"` // server-formatted; the UI never computes money
	// Sign is the server's answer to "is this positive / zero / negative" (-1, 0, 1). BX-MED-008:
	// without it, a client asking "is there anything here?" must convert amount_minor to a JS
	// Number, which is monetary arithmetic on a value that can exceed 2^53. Comparing an integer
	// -1/0/1 is exact and needs no money in the browser at all.
	Sign int `json:"sign"`
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
	amt := m.Amount()
	sign := 0
	switch {
	case amt > 0:
		sign = 1
	case amt < 0:
		sign = -1
	}
	return moneyView{
		AmountMinor: amt,
		Currency:    string(m.Currency()),
		Display:     formatMoney(amt, string(m.Currency())),
		Sign:        sign,
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

// riskTrips lists open guardrail trips bounded to the operator's scope. The
// scope is structural — repo.ListOpenTrips requires an OperatorScope derived
// only from the session (M4C-F1), so this handler cannot issue an unscoped
// read even by mistake.
func (p *Portal) riskTrips(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	trips, err := operatorRead(r.Context(), p, sess.OperatorScope(), func(ctx context.Context, tx pgx.Tx) ([]repo.GuardrailTrip, error) {
		return repo.ListOpenTrips(ctx, tx, sess.OperatorScope())
	})
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

// loadTripScoped loads a trip WITHIN the operator's scope: GetTripByID applies
// the scope in the query, so an out-of-scope or absent id both surface as the
// same no-oracle 404 — no handler-side convention check to forget (M4C-F1).
func (p *Portal) loadTripScoped(w http.ResponseWriter, r *http.Request) (repo.GuardrailTrip, bool) {
	sess := sessionFrom(r.Context())
	trip, err := operatorRead(r.Context(), p, sess.OperatorScope(), func(ctx context.Context, tx pgx.Tx) (repo.GuardrailTrip, error) {
		return repo.GetTripByID(ctx, tx, sess.OperatorScope(), r.PathValue("id"))
	})
	if err != nil {
		p.writeRiskErr(w, err)
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
