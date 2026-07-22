package handler

// Governed operator provisioning (v1) — ADMIN-only surface. CREATE is four-eyes
// (propose -> a DISTINCT admin approves), REVOKE is single-actor. The acting
// admin identity is the session actor (sess.Actor), used as maker/checker.
// Write-once is enforced beneath this by the DB grants (migration 0047): there
// is no route, here or anywhere, that mutates an operator's role or scope in
// place — a privilege change is revoke-and-recreate, which fires the kill-switch.

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/operatormgmt"
)

func (p *Portal) writeOperatorErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, operatormgmt.ErrSelfApprove), errors.Is(err, repo.ErrSelfApproveOperator):
		writeErr(w, http.StatusConflict, "OPERATOR_MAKER_CHECKER", "a create request must be approved by a different admin than its proposer")
	case errors.Is(err, repo.ErrOpenRequestExists):
		writeErr(w, http.StatusConflict, "OPERATOR_OPEN_REQUEST", "that operator already has an open create request")
	case errors.Is(err, operatormgmt.ErrBadRequest):
		writeErr(w, http.StatusBadRequest, "OPERATOR_BAD_REQUEST", err.Error())
	case errors.Is(err, operatormgmt.ErrNotActive):
		writeErr(w, http.StatusConflict, "OPERATOR_NOT_ACTIVE", "operator not found or already revoked")
	case errors.Is(err, repo.ErrNotFound):
		writeErr(w, http.StatusNotFound, "OPERATOR_REQUEST_NOT_FOUND", "operator create request not found")
	default:
		p.Log.Error("portal operator mgmt", "err", err)
		writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
	}
}

// operatorsList — the console view of every provisioned operator.
func (p *Portal) operatorsList(w http.ResponseWriter, r *http.Request) {
	ops, err := p.Operators.ListOperators(r.Context())
	if err != nil {
		p.writeOperatorErr(w, err)
		return
	}
	out := make([]map[string]string, 0, len(ops))
	for _, o := range ops {
		out = append(out, map[string]string{"actor": o.Actor, "role": o.Role, "scope": o.Scope, "status": o.Status})
	}
	writeJSON(w, http.StatusOK, map[string]any{"operators": out})
}

// operatorRequestsList — pending create requests awaiting a second admin.
func (p *Portal) operatorRequestsList(w http.ResponseWriter, r *http.Request) {
	reqs, err := p.Operators.ListOpenRequests(r.Context())
	if err != nil {
		p.writeOperatorErr(w, err)
		return
	}
	out := make([]map[string]string, 0, len(reqs))
	for _, rq := range reqs {
		out = append(out, map[string]string{
			"request_id": rq.RequestID, "actor": rq.Actor, "role": rq.Role,
			"scope": rq.Scope, "reason": rq.Reason, "requested_by": rq.RequestedBy})
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": out})
}

// operatorPropose — MAKER: record a create for a distinct admin to approve.
func (p *Portal) operatorPropose(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	var req struct {
		Actor  string `json:"actor"`
		Role   string `json:"role"`
		Scope  string `json:"scope"`
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "PORTAL_BAD_REQUEST", "malformed JSON body")
		return
	}
	rq, err := p.Operators.ProposeCreate(r.Context(), req.Actor, req.Role, req.Scope, req.Reason, sess.Actor)
	if err != nil {
		p.writeOperatorErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"request_id": rq.RequestID, "actor": rq.Actor, "state": "REQUESTED"})
}

// operatorApprove — CHECKER: a distinct admin approves; returns the one-time key.
func (p *Portal) operatorApprove(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	key, err := p.Operators.ApproveCreate(r.Context(), r.PathValue("id"), sess.Actor)
	if err != nil {
		p.writeOperatorErr(w, err)
		return
	}
	// The ONLY time the plaintext key is ever returned — it is stored hash-only
	// and cannot be retrieved again.
	writeJSON(w, http.StatusOK, map[string]string{
		"request_id": r.PathValue("id"),
		"access_key": key,
		"note":       "Store this key now — it is shown once and cannot be retrieved again.",
	})
}

// operatorReject — CHECKER: close a pending request without provisioning.
func (p *Portal) operatorReject(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	var req struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "PORTAL_BAD_REQUEST", "malformed JSON body")
		return
	}
	if req.Reason == "" {
		writeErr(w, http.StatusBadRequest, "PORTAL_BAD_REQUEST", "reason is required")
		return
	}
	if err := p.Operators.RejectCreate(r.Context(), r.PathValue("id"), sess.Actor, req.Reason); err != nil {
		p.writeOperatorErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"request_id": r.PathValue("id"), "state": "REJECTED"})
}

// operatorRevoke — single-actor deactivation (reducing access is never gated).
func (p *Portal) operatorRevoke(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	var req struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "PORTAL_BAD_REQUEST", "malformed JSON body")
		return
	}
	if req.Reason == "" {
		writeErr(w, http.StatusBadRequest, "PORTAL_BAD_REQUEST", "reason is required")
		return
	}
	if err := p.Operators.Revoke(r.Context(), r.PathValue("actor"), sess.Actor, req.Reason); err != nil {
		p.writeOperatorErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"actor": r.PathValue("actor"), "status": "REVOKED"})
}
