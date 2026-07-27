package handler

// Wave B.1 — loan-book (ops) surface. GET /ops/advances (keyset-paginated list +
// server-computed summary) and GET /ops/advances/{id} (detail + ledger paydown
// history). Both run through the operatorRead chokepoint (RLS-scoped tcp_operator
// pool), so a telco operator sees only their telco's rows AND aggregates. Money is
// server-formatted (toMoneyView); the MSISDN token is masked at egress.

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
)

// loanBookPage bundles the list page + its SQL summary, computed in one scoped tx.
type loanBookPage struct {
	rows    []repo.AdvanceRow
	summary repo.LoanBookSummaryResult
}

func (p *Portal) opsAdvances(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	q := r.URL.Query()
	limit := clampInt(q.Get("limit"), 50, 200)
	f := repo.AdvanceFilter{
		Telco:       q.Get("telco"),
		ProgrammeID: q.Get("programme"),
		State:       q.Get("state"),
		Bucket:      q.Get("bucket"),
		MSISDNToken: q.Get("msisdn_token"),
		DateFrom:    q.Get("from"),
		DateTo:      q.Get("to"),
		Cursor:      q.Get("cursor"),
		Limit:       limit,
	}
	page, err := operatorRead(r.Context(), p, sess.OperatorScope(), func(ctx context.Context, tx pgx.Tx) (loanBookPage, error) {
		rows, e := repo.ListAdvances(ctx, tx, sess.OperatorScope(), f)
		if e != nil {
			return loanBookPage{}, e
		}
		sum, e := repo.LoanBookSummary(ctx, tx, sess.OperatorScope(), f)
		return loanBookPage{rows: rows, summary: sum}, e
	})
	if err != nil {
		p.Log.Error("ops advances", "err", err)
		writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
		return
	}
	out := make([]map[string]any, 0, len(page.rows))
	for _, a := range page.rows {
		out = append(out, advanceRowJSON(a))
	}
	// Keyset: a full page implies there may be more; hand back the last id as the cursor.
	next := ""
	if len(page.rows) == limit {
		next = page.rows[len(page.rows)-1].AdvanceID
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"advances":    out,
		"summary":     loanBookSummaryJSON(page.summary),
		"next_cursor": next,
	})
}

type advanceDetail struct {
	advance repo.AdvanceRow
	history []repo.AdvanceEvent
}

func (p *Portal) opsAdvance(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	id := r.PathValue("id")
	d, err := operatorRead(r.Context(), p, sess.OperatorScope(), func(ctx context.Context, tx pgx.Tx) (advanceDetail, error) {
		a, e := repo.GetAdvanceByID(ctx, tx, sess.OperatorScope(), id)
		if e != nil {
			return advanceDetail{}, e
		}
		h, e := repo.AdvanceLedgerHistory(ctx, tx, sess.OperatorScope(), id)
		return advanceDetail{advance: a, history: h}, e
	})
	if errors.Is(err, repo.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "PORTAL_NOT_FOUND", "advance not found")
		return
	}
	if err != nil {
		p.Log.Error("ops advance", "err", err)
		writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
		return
	}
	cur := d.advance.Outstanding.Currency()
	events := make([]map[string]any, 0, len(d.history))
	for _, ev := range d.history {
		move, _ := entity.NewMoney(ev.ReceivableMovement, cur)
		run, _ := entity.NewMoney(ev.RunningOutstanding, cur)
		events = append(events, map[string]any{
			"event_type":          ev.EventType,
			"posted_at":           ev.PostedAt,
			"receivable_movement": toMoneyView(move),
			"running_outstanding": toMoneyView(run),
		})
	}
	resp := advanceRowJSON(d.advance)
	resp["events"] = events
	writeJSON(w, http.StatusOK, resp)
}

func advanceRowJSON(a repo.AdvanceRow) map[string]any {
	m := map[string]any{
		"advance_id":         a.AdvanceID,
		"telco_id":           a.TelcoID,
		"programme_id":       a.ProgrammeID,
		"state":              a.State,
		"delinquency_bucket": a.DelinquencyBucket, // "" when unclassified
		"fee_recognition":    a.FeeRecognition,
		"msisdn_masked":      maskToken(a.MSISDNToken),
		"face_value":         toMoneyView(a.FaceValue),
		"fee":                toMoneyView(a.Fee),
		"disbursed":          toMoneyView(a.Disbursed),
		"outstanding":        toMoneyView(a.Outstanding),
		"recovered":          toMoneyView(a.Recovered),
		"accepted_at":        a.AcceptedAt,
	}
	if a.BucketAsOf != nil {
		m["bucket_as_of"] = *a.BucketAsOf // when the delinquency bucket was last classified
	}
	if a.ActivatedAt != nil {
		m["activated_at"] = *a.ActivatedAt
	}
	if a.ClosedAt != nil {
		m["closed_at"] = *a.ClosedAt
	}
	return m
}

func loanBookSummaryJSON(s repo.LoanBookSummaryResult) map[string]any {
	return map[string]any{
		"total_count":       s.TotalCount,
		"by_status":         s.ByStatus,
		"by_bucket":         s.ByBucket,
		"disbursed":         toMoneyView(s.Disbursed),
		"recovered":         toMoneyView(s.Recovered),
		"open_outstanding":  toMoneyView(s.OpenOutstanding),
		"ledger_receivable": toMoneyView(s.LedgerReceivable),
		// INV-016: the loan-book outstanding must equal the ledger SUBSCRIBER_RECEIVABLE.
		"reconciled": s.OpenOutstanding.Amount() == s.LedgerReceivable.Amount(),
	}
}

func clampInt(s string, def, max int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}
