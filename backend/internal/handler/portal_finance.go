package handler

// M4d finance workspace — ledger browser. FINANCE (and ADMIN) read the
// journal ledger and tap through to a journal's balanced entries and its
// BC-6 correlation lineage. Every read is scope-bounded by the operator's
// OperatorScope in SQL (M4C-F1). No money arithmetic client-side: amounts are
// exact minor units plus a server-formatted display.

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
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
