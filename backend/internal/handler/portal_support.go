package handler

// M4f support workspace — the masked subscriber timeline (V2-SUB-008,
// UI-004) and the complaints workflow surface over the M3f usecases.
// Masking is enforced HERE, where data leaves the platform: no response ever
// carries a full subscriber token (the operator typed it to search; the
// platform never echoes it back whole). SUPPORT's entire write surface is
// the complaint workflow — it holds no route that can touch financial truth
// (V3-ORG-005), which the RBAC pack proves from the production map.

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
)

// maskToken renders a subscriber token masked-by-default (UI-004): the last
// four characters only. Short or empty tokens mask entirely.
func maskToken(tok string) string {
	if tok == "" {
		return ""
	}
	if len(tok) <= 4 {
		return "…"
	}
	return "…" + tok[len(tok)-4:]
}

// supportTimeline resolves the case view for one subscriber by FULL token.
func (p *Portal) supportTimeline(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	token := r.URL.Query().Get("token")
	if token == "" {
		writeErr(w, http.StatusBadRequest, "PORTAL_BAD_REQUEST", "token is required")
		return
	}
	sub, advances, notes, complaints, actions, err := repo.SubscriberTimeline(
		r.Context(), p.ReadPool, sess.OperatorScope(), token)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "SUBSCRIBER_NOT_FOUND", "no live subscriber for that token in your scope")
			return
		}
		p.Log.Error("portal support timeline", "err", err)
		writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
		return
	}

	advViews := make([]map[string]any, 0, len(advances))
	for _, a := range advances {
		advViews = append(advViews, map[string]any{
			"advance_id": a.AdvanceID, "programme_id": a.ProgrammeID, "state": a.State,
			"face_value": toMoneyView(a.FaceValue), "outstanding": toMoneyView(a.Outstanding),
			"accepted_at": a.AcceptedAt, "closed_at": a.ClosedAt,
		})
	}
	noteViews := make([]map[string]any, 0, len(notes))
	for _, n := range notes {
		noteViews = append(noteViews, map[string]any{
			"kind": n.Kind, "state": n.State, "created_at": n.CreatedAt, "sent_at": n.SentAt,
		})
	}
	cmpViews := make([]map[string]any, 0, len(complaints))
	for _, c := range complaints {
		cmpViews = append(cmpViews, map[string]any{
			"complaint_id": c.ComplaintID, "advance_id": c.AdvanceID, "channel": c.Channel,
			"category": c.Category, "narrative": c.Narrative, "state": c.State,
			"resolution": c.Resolution, "opened_at": c.OpenedAt,
		})
	}
	actViews := make([]map[string]any, 0, len(actions))
	for _, a := range actions {
		actViews = append(actViews, map[string]any{
			"action_id": a.ActionID, "from_status": a.FromStatus, "to_status": a.ToStatus,
			"reason": a.Reason, "state": a.State, "requested_at": a.RequestedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"subscriber": map[string]any{
			"subscriber_account_id": sub.SubscriberAccountID,
			"telco_id":              sub.TelcoID,
			"msisdn_token_masked":   maskToken(sub.MSISDNToken), // UI-004: masked by default
			"status":                sub.Status,
			"effective_from":        sub.EffectiveFrom,
		},
		"advances":       advViews,
		"notifications":  noteViews,
		"complaints":     cmpViews,
		"status_actions": actViews,
	})
}

type complaintRowResponse struct {
	ComplaintID       string `json:"complaint_id"`
	TelcoID           string `json:"telco_id"`
	MSISDNTokenMasked string `json:"msisdn_token_masked,omitempty"`
	AdvanceID         string `json:"advance_id,omitempty"`
	Channel           string `json:"channel"`
	Category          string `json:"category"`
	Narrative         string `json:"narrative"`
	State             string `json:"state"`
	Resolution        string `json:"resolution,omitempty"`
	OpenedAt          string `json:"opened_at"`
}

func toComplaintResponse(c repo.ComplaintRow) complaintRowResponse {
	return complaintRowResponse{
		ComplaintID: c.ComplaintID, TelcoID: c.TelcoID,
		MSISDNTokenMasked: maskToken(c.MSISDNToken),
		AdvanceID:         c.AdvanceID, Channel: c.Channel, Category: c.Category,
		Narrative: c.Narrative, State: c.State, Resolution: c.Resolution, OpenedAt: c.OpenedAt,
	}
}

// supportComplaints lists complaints in the operator's telco bound.
func (p *Portal) supportComplaints(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	items, err := repo.ListComplaints(r.Context(), p.ReadPool, sess.OperatorScope(), 0)
	if err != nil {
		p.Log.Error("portal support complaints", "err", err)
		writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
		return
	}
	out := make([]complaintRowResponse, 0, len(items))
	for _, c := range items {
		out = append(out, toComplaintResponse(c))
	}
	writeJSON(w, http.StatusOK, map[string]any{"complaints": out})
}

// supportComplaintOpen registers a complaint through the M3f usecase. The
// tenant is the operator's own telco bound ('*' admin names it).
func (p *Portal) supportComplaintOpen(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	var req struct {
		TelcoID     string `json:"telco_id"`
		MSISDNToken string `json:"msisdn_token"`
		AdvanceID   string `json:"advance_id"`
		Channel     string `json:"channel"`
		Category    string `json:"category"`
		Narrative   string `json:"narrative"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<15)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "PORTAL_BAD_REQUEST", "malformed JSON body")
		return
	}
	telco, ok := sess.OperatorScope().TelcoLevelBound()
	if !ok {
		writeErr(w, http.StatusForbidden, "PORTAL_SCOPE", "complaints need telco-level scope")
		return
	}
	if telco == "" {
		if req.TelcoID == "" {
			writeErr(w, http.StatusBadRequest, "PORTAL_BAD_REQUEST", "telco_id is required for all-scope sessions")
			return
		}
		telco = req.TelcoID
	}
	c, err := p.Ops.OpenComplaint(r.Context(), telco, req.MSISDNToken, req.AdvanceID,
		req.Channel, req.Category, req.Narrative)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "SUBSCRIBER_NOT_FOUND", "no live subscriber for that token")
			return
		}
		// The M3f usecase validates channel/category/narrative presence and
		// the schema enforces the category enum — surface as bad request.
		p.Log.Warn("portal complaint open refused", "err", err)
		writeErr(w, http.StatusBadRequest, "PORTAL_BAD_REQUEST", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"complaint_id": c.ComplaintID, "state": c.State})
}

// supportComplaintProgress advances one complaint through its workflow
// (OPEN -> IN_REVIEW -> RESOLVED/REJECTED; resolution mandatory at close —
// schema-enforced). Load-scoped-then-act; the from-state CAS refuses stale
// transitions loudly.
func (p *Portal) supportComplaintProgress(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	cmp, err := repo.GetComplaintScoped(r.Context(), p.ReadPool, sess.OperatorScope(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "COMPLAINT_NOT_FOUND", "complaint not found")
			return
		}
		p.Log.Error("portal complaint load", "err", err)
		writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
		return
	}
	if cmp.State == "RESOLVED" || cmp.State == "REJECTED" {
		// A closed complaint is worked-to-a-reason evidence — the portal
		// never resurrects it. A governed reopen would be its own designed
		// action with its own audit trail, not a state edit.
		writeErr(w, http.StatusConflict, "COMPLAINT_CLOSED", "complaint is closed; open a new one to continue the case")
		return
	}
	var req struct {
		To         string `json:"to"`
		Resolution string `json:"resolution"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "PORTAL_BAD_REQUEST", "malformed JSON body")
		return
	}
	switch req.To {
	case "IN_REVIEW", "RESOLVED", "REJECTED":
	default:
		writeErr(w, http.StatusBadRequest, "PORTAL_BAD_REQUEST", "to must be IN_REVIEW|RESOLVED|REJECTED")
		return
	}
	if (req.To == "RESOLVED" || req.To == "REJECTED") && req.Resolution == "" {
		writeErr(w, http.StatusBadRequest, "PORTAL_BAD_REQUEST", "resolution is required to close a complaint")
		return
	}
	if err := p.Ops.ProgressComplaint(r.Context(), cmp.TelcoID, cmp.ComplaintID,
		cmp.State, req.To, sess.Actor, req.Resolution); err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			// The from-state CAS lost a race — honest conflict.
			writeErr(w, http.StatusConflict, "COMPLAINT_STATE_CHANGED", "complaint state changed — reload and retry")
			return
		}
		p.Log.Error("portal complaint progress", "err", err)
		writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"complaint_id": cmp.ComplaintID, "state": req.To})
}
