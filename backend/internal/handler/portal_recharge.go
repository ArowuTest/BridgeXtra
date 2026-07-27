package handler

// Phase 1 S2.3b — the portal surface for the HELD-recharge review queue
// (FINANCE workspace). List the open holds, maker-request a release, have a
// DISTINCT operator approve (four-eyes, enforced in the usecase + schema), or
// reject (safe single-actor). The acting identity is the session actor; the
// telco is explicit on every call and checked against the operator's scope
// (PermitsWrite — '*' or exactly that telco), mirroring the config-draft rule.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/rechargehold"
)

func (p *Portal) writeHoldErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, rechargehold.ErrSameActor):
		writeErr(w, http.StatusConflict, "RECHARGE_HOLD_MAKER_CHECKER", "a release must be approved by a different operator than its requester")
	case errors.Is(err, rechargehold.ErrNotActionable):
		writeErr(w, http.StatusConflict, "RECHARGE_HOLD_NOT_ACTIONABLE", err.Error())
	default:
		p.Log.Error("portal recharge hold", "err", err)
		writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
	}
}

// holdScope validates the explicit telco parameter against the operator's
// scope. Empty telco or an out-of-scope telco is refused.
func (p *Portal) holdScope(w http.ResponseWriter, r *http.Request, telco string) bool {
	if telco == "" {
		writeErr(w, http.StatusBadRequest, "PORTAL_BAD_REQUEST", "telco is required")
		return false
	}
	sess := sessionFrom(r.Context())
	if !sess.PermitsWrite("telco:" + telco) {
		p.Log.Warn("portal scope refusal (recharge hold)", "actor", sess.Actor, "session_scope", sess.Scope, "telco", telco)
		writeErr(w, http.StatusForbidden, "PORTAL_FORBIDDEN", "not permitted for this scope")
		return false
	}
	return true
}

// heldRechargesList — the reviewable queue across the operator's scope. A
// telco-scoped operator sees their telco; a '*' platform admin sees every
// telco's held queue (op_all policy, 0062). Read through the operator
// chokepoint (RLS by scope), so no telco parameter is required — the page loads
// by default instead of hanging on a telco a platform admin cannot supply. Each
// row carries its own telco_id for the (still per-telco, four-eyes) actions, and
// the MSISDN is masked at egress like every other money surface.
func (p *Portal) heldRechargesList(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	rows, err := operatorRead(r.Context(), p, sess.OperatorScope(), func(ctx context.Context, tx pgx.Tx) ([]repo.HeldRow, error) {
		return repo.HeldRecharge{}.ListOpen(ctx, tx, 200)
	})
	if err != nil {
		p.Log.Error("portal held recharges list", "err", err)
		writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, h := range rows {
		// Server-formatted money (same moneyView the risk/finance surfaces emit) so
		// the operator display string is computed server-side — the UI never does
		// money arithmetic. The amount was validated at ingest; a bad currency here
		// is a data-integrity fault, so fail loud rather than emit a broken row.
		m, err := entity.NewMoney(h.AmountMinor, entity.Currency(h.Currency))
		if err != nil {
			p.Log.Error("held recharge money", "err", err, "held_id", h.HeldID)
			writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
			return
		}
		out = append(out, map[string]any{
			"held_id": h.HeldID, "source_event_id": h.SourceEventID, "telco_id": h.TelcoID,
			"msisdn_masked": maskToken(h.MSISDNToken), "amount": toMoneyView(m),
			"occurred_at": h.OccurredAt, "reason": h.Reason, "requested_by": h.RequestedBy,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"held": out})
}

type holdActionRequest struct {
	Telco  string `json:"telco"`
	Reason string `json:"reason"`
}

func decodeHoldAction(w http.ResponseWriter, r *http.Request, needReason bool) (holdActionRequest, bool) {
	var req holdActionRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "PORTAL_BAD_REQUEST", "malformed JSON body")
		return req, false
	}
	if needReason && req.Reason == "" {
		writeErr(w, http.StatusBadRequest, "PORTAL_BAD_REQUEST", "reason is required")
		return req, false
	}
	return req, true
}

// heldRechargeRequestRelease — MAKER: nominate a hold for release.
func (p *Portal) heldRechargeRequestRelease(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeHoldAction(w, r, true)
	if !ok || !p.holdScope(w, r, req.Telco) {
		return
	}
	sess := sessionFrom(r.Context())
	if err := p.Held.RequestRelease(r.Context(), req.Telco, r.PathValue("id"), sess.Actor, req.Reason); err != nil {
		p.writeHoldErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"held_id": r.PathValue("id"), "state": "RELEASE_REQUESTED"})
}

// heldRechargeApproveRelease — CHECKER: a distinct operator approves; the held
// event is ingested into recovery and the hold closes RELEASED.
func (p *Portal) heldRechargeApproveRelease(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeHoldAction(w, r, false)
	if !ok || !p.holdScope(w, r, req.Telco) {
		return
	}
	sess := sessionFrom(r.Context())
	res, err := p.Held.ApproveRelease(r.Context(), req.Telco, r.PathValue("id"), sess.Actor)
	if err != nil {
		p.writeHoldErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"held_id": r.PathValue("id"), "state": "RELEASED",
		"recovery_event_id": res.RecoveryEventID, "replayed": res.Replayed,
	})
}

// heldRechargeReject — close a hold without ingesting (safe single-actor).
func (p *Portal) heldRechargeReject(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeHoldAction(w, r, true)
	if !ok || !p.holdScope(w, r, req.Telco) {
		return
	}
	sess := sessionFrom(r.Context())
	if err := p.Held.Reject(r.Context(), req.Telco, r.PathValue("id"), sess.Actor, req.Reason); err != nil {
		p.writeHoldErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"held_id": r.PathValue("id"), "state": "REJECTED"})
}
