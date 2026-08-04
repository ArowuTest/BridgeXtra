package handler

// Wave B.3 Phase B — the write-off MONEY DOOR. Request / approve / reject a write-off, and the
// approvals inbox. The approve step is the ONLY path that crystallises a loss (posts the
// WRITE_OFF ledger movement). Discipline:
//   - Writes run on the tcp_app tenant tx inside the collections usecase (WithTenantTx), NEVER
//     the operator read pool. The operator pool is used only to SCOPE-CHECK + resolve the telco.
//   - The actor (maker) and approver (checker) come from the authenticated SESSION, never the
//     request body. Four-eyes is schema-enforced (write_offs CHECK approver <> requester).
//   - The DISPUTE GATE (owner decision): a write-off on a subscriber with an OPEN debt dispute
//     is soft-blocked; the checker may proceed only with an explicit, audited override (reason +
//     complaint id). The gating categories + whether an override is allowed are GOVERNED
//     (collections.policy) and scoped to debt disputes — a service complaint does not block.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/collections"
)

func newCorrelationID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "corr_" + hex.EncodeToString(b)
}

// writeOffErr maps the collections usecase's typed errors to HTTP.
func writeOffErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, collections.ErrAlreadyExists):
		writeErr(w, http.StatusConflict, "WRITEOFF_EXISTS", "a write-off is already open for this advance")
	case errors.Is(err, collections.ErrNotEligible):
		writeErr(w, http.StatusUnprocessableEntity, "WRITEOFF_NOT_ELIGIBLE", "advance bucket is below the governed write-off floor")
	case errors.Is(err, collections.ErrNotWritable):
		writeErr(w, http.StatusConflict, "WRITEOFF_NOT_WRITABLE", "advance is not in a write-off-able state")
	case errors.Is(err, collections.ErrSelfApproval):
		writeErr(w, http.StatusConflict, "WRITEOFF_SELF_APPROVAL", "the approver must differ from the requester (two-person rule)")
	case errors.Is(err, repo.ErrNotFound):
		writeErr(w, http.StatusNotFound, "PORTAL_NOT_FOUND", "not found")
	default:
		writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
	}
}

// opsWriteOffRequest — maker. Opens a REQUESTED write-off for an advance the operator can see.
// Crystallises nothing. The response flags an open dispute so the UI warns the maker (the
// override itself is exercised by the checker at approval).
func (p *Portal) opsWriteOffRequest(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	scope := sess.OperatorScope()
	advanceID := r.PathValue("id")
	var req struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "PORTAL_BAD_REQUEST", "invalid body")
		return
	}
	if req.Reason == "" {
		writeErr(w, http.StatusBadRequest, "PORTAL_BAD_REQUEST", "reason is required")
		return
	}
	// Scope-check + resolve the telco via the advance (never from the body).
	adv, err := operatorRead(r.Context(), p, scope, func(ctx context.Context, tx pgx.Tx) (repo.AdvanceRow, error) {
		return repo.GetAdvanceByID(ctx, tx, scope, advanceID)
	})
	if err != nil {
		writeOffErr(w, err)
		return
	}
	wo, err := p.Collections.RequestWriteOff(r.Context(), adv.TelcoID, advanceID, sess.Actor, req.Reason)
	if err != nil {
		writeOffErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"write_off_id": wo.WriteOffID,
		"state":        "REQUESTED",
		"disputed":     p.advanceHasOpenDebtDispute(r.Context(), scope, advanceID, adv.ProgrammeID),
	})
}

// opsWriteOffApprove — checker. The loss crystallises here. Enforces the DISPUTE GATE before
// approving: an open debt dispute requires an explicit audited override (when the governed
// policy allows one), recorded on the checker.
func (p *Portal) opsWriteOffApprove(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	scope := sess.OperatorScope()
	writeOffID := r.PathValue("id")
	var req struct {
		OverrideReason string `json:"override_reason"`
		ComplaintID    string `json:"complaint_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req) // body optional (override only when disputed)

	// Resolve the write-off (scope-check) + its advance (for the programme + dispute check).
	type ctxT struct {
		wo  repo.WriteOff
		adv repo.AdvanceRow
	}
	c, err := operatorRead(r.Context(), p, scope, func(ctx context.Context, tx pgx.Tx) (ctxT, error) {
		wo, e := (repo.WriteOffs{}).Get(ctx, tx, writeOffID)
		if e != nil {
			return ctxT{}, e
		}
		adv, e := repo.GetAdvanceByID(ctx, tx, scope, wo.AdvanceID)
		return ctxT{wo, adv}, e
	})
	if err != nil {
		writeOffErr(w, err)
		return
	}

	// DISPUTE GATE (governed collections.policy).
	cats, allowOverride := p.disputePolicy(r.Context(), c.adv.ProgrammeID)
	disputed, derr := operatorRead(r.Context(), p, scope, func(ctx context.Context, tx pgx.Tx) (bool, error) {
		return repo.HasOpenDebtDispute(ctx, tx, scope, c.wo.AdvanceID, cats)
	})
	if derr != nil {
		writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
		return
	}
	if disputed {
		if !allowOverride {
			writeErr(w, http.StatusConflict, "WRITEOFF_DISPUTE_BLOCKED", "subscriber has an open debt dispute; write-off is blocked by policy")
			return
		}
		if req.OverrideReason == "" || req.ComplaintID == "" {
			writeErr(w, http.StatusConflict, "WRITEOFF_DISPUTE_OVERRIDE_REQUIRED", "open debt dispute: an override reason and the complaint id are required to proceed")
			return
		}
		// Record the deliberate override (who / why / which complaint) BEFORE crystallising —
		// the accountability record the gate exists to produce, on the checker.
		if err := p.auditDisputeOverride(r.Context(), c.wo.TelcoID, sess.Actor, c.wo.AdvanceID, req.OverrideReason, req.ComplaintID); err != nil {
			writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
			return
		}
	}

	if err := p.Collections.ApproveWriteOff(r.Context(), c.wo.TelcoID, writeOffID, sess.Actor, newCorrelationID()); err != nil {
		writeOffErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"write_off_id": writeOffID, "state": "POSTED"})
}

// opsWriteOffReject — checker refusal (distinct actor still enforced).
func (p *Portal) opsWriteOffReject(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	scope := sess.OperatorScope()
	writeOffID := r.PathValue("id")
	var req struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Reason == "" {
		writeErr(w, http.StatusBadRequest, "PORTAL_BAD_REQUEST", "reason is required")
		return
	}
	wo, err := operatorRead(r.Context(), p, scope, func(ctx context.Context, tx pgx.Tx) (repo.WriteOff, error) {
		return (repo.WriteOffs{}).Get(ctx, tx, writeOffID)
	})
	if err != nil {
		writeOffErr(w, err)
		return
	}
	if err := p.Collections.RejectWriteOff(r.Context(), wo.TelcoID, writeOffID, sess.Actor, req.Reason); err != nil {
		writeOffErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"write_off_id": writeOffID, "state": "REJECTED"})
}

// opsWriteOffInbox — the checker's pending-approvals queue (write_offs granted 0071).
func (p *Portal) opsWriteOffInbox(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	scope := sess.OperatorScope()
	telcoFilter := r.URL.Query().Get("telco")
	list, err := operatorRead(r.Context(), p, scope, func(ctx context.Context, tx pgx.Tx) ([]repo.WriteOff, error) {
		return (repo.WriteOffs{}).ListPending(ctx, tx, scope, telcoFilter)
	})
	if err != nil {
		p.Log.Error("writeoff inbox", "err", err)
		writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, wo := range list {
		out = append(out, map[string]any{
			"write_off_id": wo.WriteOffID,
			"advance_id":   wo.AdvanceID,
			"principal":    toMoneyView(wo.Principal),
			"fee":          toMoneyView(wo.Fee),
			"reason":       wo.Reason,
			"requested_by": wo.RequestedBy, // the maker — the UI disables Approve for this actor
			"requested_at": wo.RequestedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"pending": out})
}

// disputePolicy resolves the governed collections.policy for a programme: the debt-dispute
// categories that gate a write-off and whether an override is permitted. Fail-SAFE: if the
// policy is unresolvable it returns the strict defaults (the standard debt-dispute categories,
// override DISALLOWED) so a config gap can never silently open the gate.
func (p *Portal) disputePolicy(ctx context.Context, programmeID string) (categories []string, allowOverride bool) {
	cv, err := p.Config.ActiveAt(ctx, "collections.policy", "programme:"+programmeID, time.Now().UTC())
	if err != nil {
		return []string{"DISPUTED_ADVANCE", "DISPUTED_RECOVERY"}, false
	}
	var c struct {
		DebtDisputeCategories  []string `json:"debt_dispute_categories"`
		AllowOverrideOnDispute *bool    `json:"allow_override_on_dispute"`
	}
	if json.Unmarshal(cv.Content, &c) != nil || len(c.DebtDisputeCategories) == 0 {
		return []string{"DISPUTED_ADVANCE", "DISPUTED_RECOVERY"}, false
	}
	allow := c.AllowOverrideOnDispute != nil && *c.AllowOverrideOnDispute
	return c.DebtDisputeCategories, allow
}

// advanceHasOpenDebtDispute is the informational dispute check for the request response (the UI
// warning). It uses the governed categories.
func (p *Portal) advanceHasOpenDebtDispute(ctx context.Context, scope repo.OperatorScope, advanceID, programmeID string) bool {
	cats, _ := p.disputePolicy(ctx, programmeID)
	disputed, err := operatorRead(ctx, p, scope, func(ctx context.Context, tx pgx.Tx) (bool, error) {
		return repo.HasOpenDebtDispute(ctx, tx, scope, advanceID, cats)
	})
	return err == nil && disputed
}

// auditDisputeOverride appends the tenant-scoped audit record for a checker's deliberate
// override of the dispute gate, on the app pool (the write path's role).
func (p *Portal) auditDisputeOverride(ctx context.Context, telcoID, actor, advanceID, reason, complaintID string) error {
	tctx := platform.WithTenant(ctx, telcoID)
	return repo.WithTenantTx(tctx, p.Pool, func(tx pgx.Tx) error {
		return repo.Audit{}.Insert(ctx, tx, entity.AuditEvent{
			ID: platform.NewID("aud"), TelcoID: telcoID, Actor: actor,
			Action: "writeoff.dispute_override", TargetType: "advance", TargetID: advanceID,
			Reason: "override on open dispute (complaint " + complaintID + "): " + reason,
		})
	})
}
