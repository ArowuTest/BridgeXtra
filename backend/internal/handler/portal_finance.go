package handler

// M4d finance workspace — ledger browser. FINANCE (and ADMIN) read the
// journal ledger and tap through to a journal's balanced entries and its
// BC-6 correlation lineage. Every read is scope-bounded by the operator's
// OperatorScope in SQL (M4C-F1). No money arithmetic client-side: amounts are
// exact minor units plus a server-formatted display.

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/settlement"
)

type journalHeaderResponse struct {
	JournalID      string `json:"journal_id"`
	EventType      string `json:"event_type"`
	TelcoID        string `json:"telco_id"`
	ProgrammeID    string `json:"programme_id"`
	AdvanceID      string `json:"advance_id,omitempty"`
	CorrelationID  string `json:"correlation_id"`
	AccountingDate string `json:"accounting_date"`
	PostedAt       string `json:"posted_at"`
}

func toJournalHeader(h repo.JournalHeader) journalHeaderResponse {
	return journalHeaderResponse{
		JournalID: h.JournalID, EventType: h.EventType, TelcoID: h.TelcoID,
		ProgrammeID: h.ProgrammeID, AdvanceID: h.AdvanceID, CorrelationID: h.CorrelationID,
		AccountingDate: h.AccountingDate, PostedAt: h.PostedAt,
	}
}

type journalEntryResponse struct {
	EntryID     string    `json:"entry_id"`
	AccountCode string    `json:"account_code"`
	Debit       moneyView `json:"debit"`
	Credit      moneyView `json:"credit"`
}

// ledgerJournals lists journals in the operator's scope, optionally filtered
// by advance_id or correlation_id (the latter drives BC-6 lineage from a tap).
func (p *Portal) ledgerJournals(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	q := r.URL.Query()
	limit := 0
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			writeErr(w, http.StatusBadRequest, "PORTAL_BAD_REQUEST", "limit must be a positive integer")
			return
		}
		limit = n
	}
	journals, err := repo.ListJournals(r.Context(), p.ReadPool, sess.OperatorScope(),
		q.Get("advance_id"), q.Get("correlation_id"), limit)
	if err != nil {
		p.Log.Error("portal ledger journals", "err", err)
		writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
		return
	}
	out := make([]journalHeaderResponse, 0, len(journals))
	for _, h := range journals {
		out = append(out, toJournalHeader(h))
	}
	writeJSON(w, http.StatusOK, map[string]any{"journals": out})
}

// ledgerJournal returns one journal with its balanced entries (tap-to-journal).
func (p *Portal) ledgerJournal(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	d, err := repo.GetJournalWithEntries(r.Context(), p.ReadPool, sess.OperatorScope(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "JOURNAL_NOT_FOUND", "journal not found")
			return
		}
		p.Log.Error("portal ledger journal", "err", err)
		writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
		return
	}
	entries := make([]journalEntryResponse, 0, len(d.Entries))
	for _, e := range d.Entries {
		entries = append(entries, journalEntryResponse{
			EntryID: e.EntryID, AccountCode: e.AccountCode,
			Debit: toMoneyView(e.Debit), Credit: toMoneyView(e.Credit),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"journal": toJournalHeader(d.JournalHeader),
		"entries": entries,
	})
}

// --- reconciliation breaks queue (M4d part 2) ------------------------------

type breakResponse struct {
	ReconItemID string `json:"recon_item_id"`
	RunID       string `json:"run_id"`
	TelcoID     string `json:"telco_id"`
	ItemType    string `json:"item_type"`
	Status      string `json:"status"`
	PlatformRef string `json:"platform_ref,omitempty"`
	TelcoRef    string `json:"telco_ref,omitempty"`
	AssignedTo  string `json:"assigned_to,omitempty"`
	CreatedAt   string `json:"created_at"`
}

func toBreakResponse(b repo.BreakItem) breakResponse {
	return breakResponse{
		ReconItemID: b.ReconItemID, RunID: b.RunID, TelcoID: b.TelcoID, ItemType: b.ItemType,
		Status: b.Status, PlatformRef: b.PlatformRef, TelcoRef: b.TelcoRef,
		AssignedTo: b.AssignedTo, CreatedAt: b.CreatedAt,
	}
}

func (p *Portal) financeBreaks(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	breaks, err := repo.ListOpenBreaks(r.Context(), p.ReadPool, sess.OperatorScope())
	if err != nil {
		p.Log.Error("portal finance breaks", "err", err)
		writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
		return
	}
	out := make([]breakResponse, 0, len(breaks))
	for _, b := range breaks {
		out = append(out, toBreakResponse(b))
	}
	writeJSON(w, http.StatusOK, map[string]any{"breaks": out})
}

// Break resolution is a two-actor decision (R-P0-6 Slice E1): PROPOSE_RESOLVE
// (maker) then APPROVE_RESOLVE (distinct checker). Single-actor RESOLVE is
// retired — a money break is never cleared by one actor (auto_resolve=false).
var breakActions = map[string]bool{
	"ASSIGN": true, "ESCALATE": true, "NOTE": true,
	"PROPOSE_RESOLVE": true, "APPROVE_RESOLVE": true,
}

// financeBreakAction applies ASSIGN/RESOLVE/ESCALATE/NOTE to a break. The
// break is loaded within scope first (no-oracle 404); the action runs as the
// app role in a tenant tx via the ops service, with the session actor.
func (p *Portal) financeBreakAction(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	brk, err := repo.GetOpenBreak(r.Context(), p.ReadPool, sess.OperatorScope(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "BREAK_NOT_FOUND", "reconciliation break not found")
			return
		}
		p.Log.Error("portal break load", "err", err)
		writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
		return
	}
	var req struct {
		Action string `json:"action"`
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "PORTAL_BAD_REQUEST", "malformed JSON body")
		return
	}
	if !breakActions[req.Action] {
		writeErr(w, http.StatusBadRequest, "PORTAL_BAD_REQUEST", "action must be ASSIGN|ESCALATE|NOTE|PROPOSE_RESOLVE|APPROVE_RESOLVE")
		return
	}
	if req.Reason == "" {
		writeErr(w, http.StatusBadRequest, "PORTAL_BAD_REQUEST", "reason is required")
		return
	}
	if err := p.Ops.BreakAction(r.Context(), brk.TelcoID, brk.ReconItemID, req.Action, sess.Actor, req.Reason); err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "BREAK_NOT_FOUND", "reconciliation break not found or already resolved")
			return
		}
		if errors.Is(err, repo.ErrSelfApproveResolution) {
			writeErr(w, http.StatusConflict, "BREAK_FOUR_EYES", "the approver of a break resolution must differ from the proposer")
			return
		}
		if errors.Is(err, repo.ErrNoProposedResolution) {
			writeErr(w, http.StatusConflict, "BREAK_NO_PROPOSAL", "no proposed resolution to approve — PROPOSE_RESOLVE first")
			return
		}
		p.Log.Error("portal break action", "err", err)
		writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"recon_item_id": brk.ReconItemID, "action": req.Action})
}

// --- settlement statements + verification (M4d part 3) ---------------------

type settlementResponse struct {
	StatementID    string `json:"statement_id"`
	TelcoID        string `json:"telco_id"`
	ProgrammeID    string `json:"programme_id"`
	PeriodStart    string `json:"period_start"`
	PeriodEnd      string `json:"period_end"`
	State          string `json:"state"`
	Currency       string `json:"currency"`
	TermsVersionID string `json:"terms_version_id"`
	FinalisedAt    string `json:"finalised_at,omitempty"`
}

func toSettlement(s repo.SettlementSummary) settlementResponse {
	return settlementResponse{
		StatementID: s.StatementID, TelcoID: s.TelcoID, ProgrammeID: s.ProgrammeID,
		PeriodStart: s.PeriodStart, PeriodEnd: s.PeriodEnd, State: s.State,
		Currency: s.Currency, TermsVersionID: s.TermsVersionID, FinalisedAt: s.FinalisedAt,
	}
}

func (p *Portal) financeSettlements(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	list, err := repo.ListSettlements(r.Context(), p.ReadPool, sess.OperatorScope(), 0)
	if err != nil {
		p.Log.Error("portal settlements list", "err", err)
		writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
		return
	}
	out := make([]settlementResponse, 0, len(list))
	for _, s := range list {
		out = append(out, toSettlement(s))
	}
	writeJSON(w, http.StatusOK, map[string]any{"statements": out})
}

func (p *Portal) financeSettlement(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	d, err := repo.GetSettlementWithLines(r.Context(), p.ReadPool, sess.OperatorScope(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "SETTLEMENT_NOT_FOUND", "settlement not found")
			return
		}
		p.Log.Error("portal settlement get", "err", err)
		writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
		return
	}
	lines := make([]map[string]any, 0, len(d.Lines))
	for _, l := range d.Lines {
		lines = append(lines, map[string]any{"line_code": l.LineCode, "amount": toMoneyView(l.Amount)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"statement": toSettlement(d.SettlementSummary), "lines": lines})
}

// financeSettlementVerify recomputes a FINAL statement from the ledger against
// its pinned terms and reports whether it reproduces its content hash. A
// mismatch is a data-integrity FINDING reported as verified:false (a result,
// not a 500) — the whole point of the tool is to surface it.
func (p *Portal) financeSettlementVerify(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	d, err := repo.GetSettlementWithLines(r.Context(), p.ReadPool, sess.OperatorScope(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "SETTLEMENT_NOT_FOUND", "settlement not found")
			return
		}
		p.Log.Error("portal settlement verify load", "err", err)
		writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
		return
	}
	if d.State != "FINAL" {
		writeErr(w, http.StatusConflict, "SETTLEMENT_NOT_FINAL", "only FINAL statements can be verified")
		return
	}
	if err := p.Settlement.VerifyReproducible(r.Context(), d.TelcoID, d.StatementID); err != nil {
		if errors.Is(err, settlement.ErrNotReproducible) {
			// A GENUINE finding — the statement did not reproduce. Report it as
			// a result (the error message carries hashes, not amounts).
			p.Log.Warn("settlement verification FAILED — does not reproduce",
				"statement", d.StatementID, "actor", sess.Actor)
			writeJSON(w, http.StatusOK, map[string]any{"statement_id": d.StatementID, "verified": false})
			return
		}
		// An OPERATIONAL failure (DB, config) — NOT a verification result. Do not
		// mislabel it verified:false; surface a real error so a transient blip is
		// never mistaken for tampering (and vice versa).
		p.Log.Error("settlement verify error (operational)", "statement", d.StatementID, "err", err)
		writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "could not complete verification")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"statement_id": d.StatementID, "verified": true})
}
