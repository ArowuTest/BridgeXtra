package handler

// M4e ops workspace — the ambiguity queues. OPS (and FINANCE, read-only)
// see the fulfilments whose telco outcome is unresolved and the reversals
// parked by M3B-F1, each with its age and current blocker. The ONLY actions
// are deliberately narrow: enquire-now reschedules the resolver (the portal
// never talks to the telco or resolves attempt state — money moves only on
// telco evidence), and retry re-runs the exact guarded apply the ingest path
// uses (no second reversal path). Reads are scope-bounded by OperatorScope in
// SQL; actions load-scoped-then-act via the app-pool tenant tx.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/ops"
)

type ambiguousAttemptResponse struct {
	AttemptID     string    `json:"attempt_id"`
	AdvanceID     string    `json:"advance_id"`
	TelcoID       string    `json:"telco_id"`
	ProgrammeID   string    `json:"programme_id"`
	AdvanceState  string    `json:"advance_state"`
	FaceValue     moneyView `json:"face_value"`
	State         string    `json:"state"`
	AttemptNo     int       `json:"attempt_no"`
	EnquiryCount  int       `json:"enquiry_count"`
	SubmittedAt   string    `json:"submitted_at"`
	NextEnquiryAt string    `json:"next_enquiry_at,omitempty"`
}

func toAmbiguousResponse(a repo.AmbiguousAttempt) ambiguousAttemptResponse {
	return ambiguousAttemptResponse{
		AttemptID: a.AttemptID, AdvanceID: a.AdvanceID, TelcoID: a.TelcoID,
		ProgrammeID: a.ProgrammeID, AdvanceState: a.AdvanceState, FaceValue: toMoneyView(a.FaceValue),
		State: a.State, AttemptNo: a.AttemptNo, EnquiryCount: a.EnquiryCount,
		SubmittedAt: a.SubmittedAt, NextEnquiryAt: a.NextEnquiryAt,
	}
}

// opsFulfilments lists UNKNOWN and stale-SENT attempts in the operator's
// scope. The staleness threshold is governed config (ops.queues) and its
// absence REFUSES the read (C3) — an unbounded queue is not an empty one.
func (p *Portal) opsFulfilments(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	qc, err := p.Ops.QueuesConfig(r.Context())
	if err != nil {
		p.Log.Error("portal ops queues config", "err", err)
		writeErr(w, http.StatusInternalServerError, "OPS_QUEUES_UNCONFIGURED", "ops.queues config missing or invalid")
		return
	}
	limit := qc.MaxPageSize
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			writeErr(w, http.StatusBadRequest, "PORTAL_BAD_REQUEST", "limit must be a positive integer")
			return
		}
		if n < limit {
			limit = n
		}
	}
	staleBefore := time.Now().UTC().Add(-time.Duration(qc.StaleSentAfterSeconds) * time.Second)
	items, err := operatorRead(r.Context(), p, sess.OperatorScope(), func(ctx context.Context, tx pgx.Tx) ([]repo.AmbiguousAttempt, error) {
		return repo.ListAmbiguousAttempts(ctx, tx, sess.OperatorScope(), staleBefore.Format(time.RFC3339Nano), limit)
	})
	if err != nil {
		p.Log.Error("portal ops fulfilments", "err", err)
		writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
		return
	}
	out := make([]ambiguousAttemptResponse, 0, len(items))
	for _, a := range items {
		out = append(out, toAmbiguousResponse(a))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"attempts":                 out,
		"stale_sent_after_seconds": qc.StaleSentAfterSeconds,
	})
}

// opsEnquireNow reschedules one ambiguous attempt for immediate resolver
// enquiry. Load-scoped-then-act; the repo's state predicate refuses an
// attempt the resolver settled in the meantime (C2).
func (p *Portal) opsEnquireNow(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	at, err := operatorRead(r.Context(), p, sess.OperatorScope(), func(ctx context.Context, tx pgx.Tx) (repo.AmbiguousAttempt, error) {
		return repo.GetAmbiguousAttempt(ctx, tx, sess.OperatorScope(), r.PathValue("id"))
	})
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "ATTEMPT_NOT_FOUND", "ambiguous attempt not found")
			return
		}
		p.Log.Error("portal ops attempt load", "err", err)
		writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
		return
	}
	if err := p.Ops.EnquireNow(r.Context(), at.TelcoID, at.AttemptID, sess.Actor); err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			// Settled between read and write — honest conflict, not an error.
			writeErr(w, http.StatusConflict, "ATTEMPT_ALREADY_RESOLVED", "attempt is no longer ambiguous")
			return
		}
		p.Log.Error("portal ops enquire-now", "err", err)
		writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"attempt_id": at.AttemptID, "rescheduled": true})
}

type parkedReversalResponse struct {
	PendingReversalID     string    `json:"pending_reversal_id"`
	TelcoID               string    `json:"telco_id"`
	OriginalSourceEventID string    `json:"original_source_event_id"`
	ReversalSourceEventID string    `json:"reversal_source_event_id"`
	Amount                moneyView `json:"amount"`
	ParkReason            string    `json:"park_reason"`
	ReceivedAt            string    `json:"received_at"`
}

func toParkedResponse(pr repo.ParkedReversalRow) parkedReversalResponse {
	return parkedReversalResponse{
		PendingReversalID: pr.PendingReversalID, TelcoID: pr.TelcoID,
		OriginalSourceEventID: pr.OriginalSourceEventID, ReversalSourceEventID: pr.ReversalSourceEventID,
		Amount: toMoneyView(pr.Amount), ParkReason: pr.ParkReason, ReceivedAt: pr.ReceivedAt,
	}
}

// opsReversals lists PARKED reversals. pending_reversals is telco-grained, so
// the read takes the TelcoLevelBound: a programme-scoped operator sees an
// empty queue, never another telco's money events.
func (p *Portal) opsReversals(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			writeErr(w, http.StatusBadRequest, "PORTAL_BAD_REQUEST", "limit must be a positive integer")
			return
		}
		limit = n
	}
	items, err := operatorRead(r.Context(), p, sess.OperatorScope(), func(ctx context.Context, tx pgx.Tx) ([]repo.ParkedReversalRow, error) {
		return repo.ListParkedReversals(ctx, tx, sess.OperatorScope(), limit)
	})
	if err != nil {
		p.Log.Error("portal ops reversals", "err", err)
		writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
		return
	}
	out := make([]parkedReversalResponse, 0, len(items))
	for _, pr := range items {
		out = append(out, toParkedResponse(pr))
	}
	writeJSON(w, http.StatusOK, map[string]any{"reversals": out})
}

// opsReversalRetry re-attempts one parked reversal through the money core's
// own guarded apply. Applied -> the queue drains; still blocked -> the
// refreshed park_reason comes back; applied concurrently by the ingest path
// -> honest 409 (the FOR UPDATE claim makes double-apply impossible, C2).
func (p *Portal) opsReversalRetry(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	pr, err := operatorRead(r.Context(), p, sess.OperatorScope(), func(ctx context.Context, tx pgx.Tx) (repo.ParkedReversalRow, error) {
		return repo.GetParkedReversal(ctx, tx, sess.OperatorScope(), r.PathValue("id"))
	})
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "REVERSAL_NOT_FOUND", "parked reversal not found")
			return
		}
		p.Log.Error("portal ops reversal load", "err", err)
		writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
		return
	}
	res, err := p.Recovery.RetryParked(r.Context(), pr.TelcoID, pr.PendingReversalID, sess.Actor)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			writeErr(w, http.StatusConflict, "REVERSAL_ALREADY_APPLIED", "reversal is no longer parked")
			return
		}
		p.Log.Error("portal ops reversal retry", "err", err)
		writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"pending_reversal_id": pr.PendingReversalID,
		"applied":             res.Applied,
		"park_reason":         res.ParkReason,
	})
}

// --- subscriber status actions (M4e-2, VR-35-F1) ---------------------------

type statusActionResponse struct {
	ActionID            string `json:"action_id"`
	TelcoID             string `json:"telco_id"`
	SubscriberAccountID string `json:"subscriber_account_id"`
	MSISDNToken         string `json:"msisdn_token"`
	CurrentStatus       string `json:"current_status"`
	FromStatus          string `json:"from_status"`
	ToStatus            string `json:"to_status"`
	Reason              string `json:"reason"`
	RequestedBy         string `json:"requested_by"`
	ApprovedBy          string `json:"approved_by,omitempty"`
	State               string `json:"state"`
	RequestedAt         string `json:"requested_at"`
	DecidedAt           string `json:"decided_at,omitempty"`
}

func toStatusActionResponse(a repo.StatusActionRow) statusActionResponse {
	return statusActionResponse{
		ActionID: a.ActionID, TelcoID: a.TelcoID, SubscriberAccountID: a.SubscriberAccountID,
		MSISDNToken: a.MSISDNToken, CurrentStatus: a.CurrentStatus,
		FromStatus: a.FromStatus, ToStatus: a.ToStatus, Reason: a.Reason,
		RequestedBy: a.RequestedBy, ApprovedBy: a.ApprovedBy, State: a.State,
		RequestedAt: a.RequestedAt, DecidedAt: a.DecidedAt,
	}
}

// opsStatusActions lists status actions (telco-grained: TelcoLevelBound).
func (p *Portal) opsStatusActions(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	items, err := operatorRead(r.Context(), p, sess.OperatorScope(), func(ctx context.Context, tx pgx.Tx) ([]repo.StatusActionRow, error) {
		return repo.ListStatusActions(ctx, tx, sess.OperatorScope(), 0)
	})
	if err != nil {
		p.Log.Error("portal status actions list", "err", err)
		writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
		return
	}
	out := make([]statusActionResponse, 0, len(items))
	for _, a := range items {
		out = append(out, toStatusActionResponse(a))
	}
	writeJSON(w, http.StatusOK, map[string]any{"actions": out})
}

// opsStatusActionRequest opens a maker-checker status action. The telco is
// the operator's own scope bound — a '*' admin must name it explicitly.
func (p *Portal) opsStatusActionRequest(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	var req struct {
		TelcoID     string `json:"telco_id"`
		MSISDNToken string `json:"msisdn_token"`
		ToStatus    string `json:"to_status"`
		Reason      string `json:"reason"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "PORTAL_BAD_REQUEST", "malformed JSON body")
		return
	}
	if req.MSISDNToken == "" || req.ToStatus == "" || req.Reason == "" {
		writeErr(w, http.StatusBadRequest, "PORTAL_BAD_REQUEST", "msisdn_token, to_status and reason are required")
		return
	}
	// Resolve the tenant STRUCTURALLY: a telco-bounded operator acts in their
	// own telco regardless of the body; only a '*' admin names one.
	telco, ok := sess.OperatorScope().TelcoLevelBound()
	if !ok {
		writeErr(w, http.StatusForbidden, "PORTAL_SCOPE", "status actions need telco-level scope")
		return
	}
	if telco == "" { // '*' admin
		if req.TelcoID == "" {
			writeErr(w, http.StatusBadRequest, "PORTAL_BAD_REQUEST", "telco_id is required for all-scope sessions")
			return
		}
		telco = req.TelcoID
	}
	a, err := p.Ops.RequestStatusAction(r.Context(), telco, req.MSISDNToken, req.ToStatus, req.Reason, sess.Actor)
	if err != nil {
		p.writeStatusActionErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"action_id": a.ActionID, "state": a.State,
		"from_status": a.FromStatus, "to_status": a.ToStatus})
}

// opsStatusActionDecide is the checker step: approve applies via CAS, reject
// closes. Load-scoped-then-act; the two-actor rule refuses the requester.
func (p *Portal) opsStatusActionDecide(approve bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess := sessionFrom(r.Context())
		a, err := operatorRead(r.Context(), p, sess.OperatorScope(), func(ctx context.Context, tx pgx.Tx) (repo.StatusActionRow, error) {
			return repo.GetStatusAction(ctx, tx, sess.OperatorScope(), r.PathValue("id"))
		})
		if err != nil {
			if errors.Is(err, repo.ErrNotFound) {
				writeErr(w, http.StatusNotFound, "STATUS_ACTION_NOT_FOUND", "status action not found")
				return
			}
			p.Log.Error("portal status action load", "err", err)
			writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
			return
		}
		if err := p.Ops.DecideStatusAction(r.Context(), a.TelcoID, a.ActionID, sess.Actor, approve); err != nil {
			p.writeStatusActionErr(w, err)
			return
		}
		state := "REJECTED"
		if approve {
			state = "APPLIED"
		}
		writeJSON(w, http.StatusOK, map[string]string{"action_id": a.ActionID, "state": state})
	}
}

// --- fault demo (M4e-3) ----------------------------------------------------

// demoTelco resolves the demo tenant structurally: a telco-bounded operator
// runs in their own telco; a '*' admin names one. Programme-scoped operators
// have no telco-level authority and are refused.
func demoTelco(w http.ResponseWriter, r *http.Request, explicit string) (string, bool) {
	sess := sessionFrom(r.Context())
	telco, ok := sess.OperatorScope().TelcoLevelBound()
	if !ok {
		writeErr(w, http.StatusForbidden, "PORTAL_SCOPE", "the demo needs telco-level scope")
		return "", false
	}
	if telco == "" {
		if explicit == "" {
			writeErr(w, http.StatusBadRequest, "PORTAL_BAD_REQUEST", "telco_id is required for all-scope sessions")
			return "", false
		}
		telco = explicit
	}
	return telco, true
}

// opsDemoScenarios lists the governed catalogue for the operator's telco.
func (p *Portal) opsDemoScenarios(w http.ResponseWriter, r *http.Request) {
	telco, ok := demoTelco(w, r, r.URL.Query().Get("telco_id"))
	if !ok {
		return
	}
	scenarios, err := p.Demo.Scenarios(r.Context(), telco)
	if err != nil {
		p.writeDemoErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"telco_id": telco, "scenarios": scenarios})
}

// opsDemoRun starts one fault-demo run through the real origination path.
func (p *Portal) opsDemoRun(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	var req struct {
		TelcoID  string `json:"telco_id"`
		Scenario string `json:"scenario"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&req); err != nil || req.Scenario == "" {
		writeErr(w, http.StatusBadRequest, "PORTAL_BAD_REQUEST", "scenario is required")
		return
	}
	telco, ok := demoTelco(w, r, req.TelcoID)
	if !ok {
		return
	}
	run, err := p.Demo.Run(r.Context(), telco, req.Scenario, sess.Actor)
	if err != nil {
		p.writeDemoErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"run_id": run.RunID, "scenario": run.Scenario, "msisdn_token": run.MSISDNToken,
		"advance_id": run.AdvanceID, "correlation_id": run.CorrelationID,
	})
}

type demoRunResponse struct {
	RunID         string `json:"run_id"`
	TelcoID       string `json:"telco_id"`
	Scenario      string `json:"scenario"`
	MSISDNToken   string `json:"msisdn_token"`
	AdvanceID     string `json:"advance_id"`
	CorrelationID string `json:"correlation_id"`
	RequestedBy   string `json:"requested_by"`
	CreatedAt     string `json:"created_at"`
}

func toDemoRunResponse(r repo.DemoRunRow) demoRunResponse {
	return demoRunResponse{
		RunID: r.RunID, TelcoID: r.TelcoID, Scenario: r.Scenario, MSISDNToken: r.MSISDNToken,
		AdvanceID: r.AdvanceID, CorrelationID: r.CorrelationID, RequestedBy: r.RequestedBy,
		CreatedAt: r.CreatedAt,
	}
}

// opsDemoRuns lists recent runs (telco-grained: TelcoLevelBound).
func (p *Portal) opsDemoRuns(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	runs, err := operatorRead(r.Context(), p, sess.OperatorScope(), func(ctx context.Context, tx pgx.Tx) ([]repo.DemoRunRow, error) {
		return repo.ListDemoRuns(ctx, tx, sess.OperatorScope(), 0)
	})
	if err != nil {
		p.Log.Error("portal demo runs", "err", err)
		writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
		return
	}
	out := make([]demoRunResponse, 0, len(runs))
	for _, dr := range runs {
		out = append(out, toDemoRunResponse(dr))
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": out})
}

// opsDemoRunDetail returns the run's LIVE artifact chain — read fresh from
// the real tables every poll, so the resolver's progress shows as it happens.
func (p *Portal) opsDemoRunDetail(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	run, err := operatorRead(r.Context(), p, sess.OperatorScope(), func(ctx context.Context, tx pgx.Tx) (repo.DemoRunRow, error) {
		return repo.GetDemoRun(ctx, tx, sess.OperatorScope(), r.PathValue("id"))
	})
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "DEMO_RUN_NOT_FOUND", "demo run not found")
			return
		}
		p.Log.Error("portal demo run load", "err", err)
		writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
		return
	}
	var adv repo.DemoAdvanceView
	var attempts []repo.DemoAttemptView
	var notes []repo.DemoNotificationView
	err = p.Operator.Read(r.Context(), sess.OperatorScope(), func(ctx context.Context, tx pgx.Tx) error {
		var e error
		adv, attempts, notes, e = repo.GetDemoChain(ctx, tx, run)
		return e
	})
	if err != nil {
		p.Log.Error("portal demo chain", "err", err)
		writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
		return
	}
	journals, err := operatorRead(r.Context(), p, sess.OperatorScope(), func(ctx context.Context, tx pgx.Tx) ([]repo.JournalHeader, error) {
		return repo.ListJournals(ctx, tx, sess.OperatorScope(), "", run.CorrelationID, 50)
	})
	if err != nil {
		p.Log.Error("portal demo journals", "err", err)
		writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
		return
	}
	attemptViews := make([]map[string]any, 0, len(attempts))
	for _, a := range attempts {
		attemptViews = append(attemptViews, map[string]any{
			"attempt_id": a.AttemptID, "attempt_no": a.AttemptNo, "state": a.State,
			"telco_reference": a.TelcoRef, "enquiry_count": a.EnquiryCount,
			"submitted_at": a.SubmittedAt, "resolved_at": a.ResolvedAt, "next_enquiry_at": a.NextEnquiryAt,
		})
	}
	noteViews := make([]map[string]any, 0, len(notes))
	for _, n := range notes {
		noteViews = append(noteViews, map[string]any{
			"kind": n.Kind, "state": n.State, "created_at": n.CreatedAt, "sent_at": n.SentAt,
		})
	}
	journalViews := make([]journalHeaderResponse, 0, len(journals))
	for _, j := range journals {
		journalViews = append(journalViews, toJournalHeader(j))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"run": toDemoRunResponse(run),
		"advance": map[string]any{
			"advance_id": adv.AdvanceID, "state": adv.State,
			"face_value": toMoneyView(adv.FaceValue), "outstanding": toMoneyView(adv.Outstanding),
			"activated_at": adv.ActivatedAt, "closed_at": adv.ClosedAt,
		},
		"attempts":      attemptViews,
		"notifications": noteViews,
		"journals":      journalViews,
	})
}

func (p *Portal) writeDemoErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ops.ErrDemoPoolBusy):
		writeErr(w, http.StatusConflict, "DEMO_POOL_BUSY", err.Error())
	case errors.Is(err, ops.ErrDemoScenario):
		writeErr(w, http.StatusBadRequest, "DEMO_SCENARIO_UNKNOWN", "unknown demo scenario")
	case errors.Is(err, ops.ErrDemoUnavailable):
		writeErr(w, http.StatusForbidden, "DEMO_UNAVAILABLE", "fault demo not available for this telco (disabled or not allowlisted)")
	default:
		p.Log.Error("portal demo", "err", err)
		writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
	}
}

func (p *Portal) writeStatusActionErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ops.ErrSameActor):
		writeErr(w, http.StatusConflict, "STATUS_ACTION_TWO_PERSON", "approver must differ from the requester")
	case errors.Is(err, repo.ErrOpenActionExists):
		writeErr(w, http.StatusConflict, "STATUS_ACTION_OPEN_EXISTS", "subscriber already has an open status action")
	case errors.Is(err, repo.ErrStatusDrift):
		writeErr(w, http.StatusConflict, "STATUS_DRIFTED", "subscriber status changed since the request — re-check and re-request")
	case errors.Is(err, ops.ErrTransitionNotAllowed):
		writeErr(w, http.StatusBadRequest, "STATUS_TRANSITION_NOT_ALLOWED", "transition not allowed by governed config")
	case errors.Is(err, repo.ErrNotFound):
		writeErr(w, http.StatusNotFound, "STATUS_ACTION_NOT_FOUND", "status action or subscriber not found, or not open")
	default:
		p.Log.Error("portal status action", "err", err)
		writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
	}
}
