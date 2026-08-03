package handler

// Wave B.2a — Subscriber-360. The MSISDN is the account number: a scope-bound
// directory (search by full number or trailing digits) and the whole per-subscriber
// picture (identity, credit limit + tier HISTORY, all loans reconciling to the ledger,
// recharges in, repayments). The MSISDN is MASKED in every list and on the 360 header;
// the full number is exposed only through the separate audited reveal endpoint. All
// reads run on the operator-read chokepoint (RLS by scope) — a telco operator sees only
// their subscribers, a '*' admin the estate, a no-authority operator nothing.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
)

// auditSubscriberMSISDNRevealed is the audit action recorded when an operator reveals
// a subscriber's full MSISDN. The row records who/subject/when — never the number.
const auditSubscriberMSISDNRevealed = "SUBSCRIBER_MSISDN_REVEALED"

// opsSubscribers — the MSISDN directory. `q` matches the full MSISDN or a trailing
// suffix (the last digits an admin sees masked). Returns the opaque account id +
// MASKED MSISDN + the money rollup; the full number never leaves here.
func (p *Portal) opsSubscribers(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	limit := clampInt(r.URL.Query().Get("limit"), 50, 200)
	rows, err := operatorRead(r.Context(), p, sess.OperatorScope(), func(ctx context.Context, tx pgx.Tx) ([]repo.SubscriberDirectoryRow, error) {
		return repo.SearchSubscribers(ctx, tx, sess.OperatorScope(), q, limit)
	})
	if err != nil {
		p.Log.Error("portal subscriber search", "err", err)
		writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, s := range rows {
		out = append(out, map[string]any{
			"subscriber_account_id": s.SubscriberAccountID,
			"telco_id":              s.TelcoID,
			"msisdn_masked":         maskToken(s.MSISDNToken),
			"status":                s.Status,
			"open_loans":            s.OpenLoans,
			"total_outstanding":     toMoneyView(s.TotalOutstanding),
			"ever_borrowed":         toMoneyView(s.EverBorrowed),
			"ever_repaid":           toMoneyView(s.EverRepaid),
			"worst_bucket":          s.WorstBucket,
			"last_recharge_at":      s.LastRechargeAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"subscribers": out})
}

// opsSubscriberReveal — the audited full-MSISDN reveal (locked condition #4). It is
// deliberately FAIL-CLOSED: the number is disclosed only after a real audit_events row
// is written (who revealed it, which subject, when). Scope is proven by the same
// operator-read chokepoint — an out-of-scope or unknown id returns 404 with no oracle,
// so the reveal can never be used to probe another tenant's estate. The audit row
// records THAT a reveal happened and the masked tail — never the number itself.
func (p *Portal) opsSubscriberReveal(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	acct := r.PathValue("id")
	prof, err := operatorRead(r.Context(), p, sess.OperatorScope(), func(ctx context.Context, tx pgx.Tx) (repo.SubscriberProfileResult, error) {
		return repo.GetSubscriberProfile(ctx, tx, sess.OperatorScope(), acct)
	})
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "SUBSCRIBER_NOT_FOUND", "no subscriber for that id in your scope")
			return
		}
		p.Log.Error("portal subscriber reveal lookup", "err", err)
		writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
		return
	}
	full := prof.Identity.MSISDNToken
	detail, _ := json.Marshal(map[string]string{
		"telco_id": prof.Identity.TelcoID,
		"masked":   maskToken(full),
	})
	// Fail closed: no audit written => nothing disclosed. The audit IS the point.
	if err := p.Audit.InsertPlatform(r.Context(), p.Pool, entity.AuditEvent{
		ID:         platform.NewID("aud"),
		Actor:      sess.Actor,
		Action:     auditSubscriberMSISDNRevealed,
		TargetType: "subscriber_account",
		TargetID:   acct,
		Detail:     detail,
		SourceIP:   clientIP(r, p.TrustedProxyCount),
	}); err != nil {
		p.Log.Error("portal subscriber reveal audit", "err", err)
		writeErr(w, http.StatusInternalServerError, "REVEAL_AUDIT_FAILED", "could not record the reveal; not disclosing")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"subscriber_account_id": acct,
		"msisdn":                full,
	})
}

// opsSubscriber — the full 360 for one subscriber (by opaque account id). MSISDN
// masked; the full number is behind the audited reveal endpoint.
func (p *Portal) opsSubscriber(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	acct := r.PathValue("id")
	prof, err := operatorRead(r.Context(), p, sess.OperatorScope(), func(ctx context.Context, tx pgx.Tx) (repo.SubscriberProfileResult, error) {
		return repo.GetSubscriberProfile(ctx, tx, sess.OperatorScope(), acct)
	})
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "SUBSCRIBER_NOT_FOUND", "no subscriber for that id in your scope")
			return
		}
		p.Log.Error("portal subscriber profile", "err", err)
		writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
		return
	}
	writeJSON(w, http.StatusOK, subscriberProfileJSON(prof))
}

func limitPointJSON(h repo.LimitPoint) map[string]any {
	return map[string]any{
		"limit":          toMoneyView(h.Limit),
		"tier":           h.TierCode,
		"prior_tier":     h.PriorTier,
		"config_version": h.ConfigVer,
		"is_current":     h.IsCurrent,
		"scored_at":      h.ScoredAt,
		"valid_until":    h.ValidUntil,
	}
}

func subscriberProfileJSON(prof repo.SubscriberProfileResult) map[string]any {
	id := prof.Identity

	hist := make([]map[string]any, 0, len(prof.History))
	for _, h := range prof.History {
		hist = append(hist, limitPointJSON(h))
	}
	var current map[string]any
	if prof.Current != nil {
		current = limitPointJSON(*prof.Current)
	}

	// Loans. The per-subscriber "still owed" total is computed in the read-model on
	// the loan-book basis (Σ outstanding over ACTIVE/PARTIALLY_RECOVERED), so it
	// reconciles to this subscriber's slice of SUBSCRIBER_RECEIVABLE.
	loans := make([]map[string]any, 0, len(prof.Loans))
	for _, l := range prof.Loans {
		loans = append(loans, map[string]any{
			"advance_id":         l.AdvanceID,
			"programme_id":       l.ProgrammeID,
			"state":              l.State,
			"delinquency_bucket": l.DelinquencyBucket,
			"disbursed":          toMoneyView(l.Disbursed),
			"outstanding":        toMoneyView(l.Outstanding),
			"recovered":          toMoneyView(l.Recovered),
			"accepted_at":        l.AcceptedAt,
			"activated_at":       l.ActivatedAt,
			"closed_at":          l.ClosedAt,
		})
	}

	recharges := make([]map[string]any, 0, len(prof.Recharges))
	for _, rc := range prof.Recharges {
		recharges = append(recharges, map[string]any{
			"amount":      toMoneyView(rc.Amount),
			"state":       rc.State,
			"applied":     toMoneyView(rc.Applied),
			"occurred_at": rc.OccurredAt,
		})
	}
	repayments := make([]map[string]any, 0, len(prof.Repayments))
	for _, rp := range prof.Repayments {
		repayments = append(repayments, map[string]any{
			"advance_id": rp.AdvanceID,
			"component":  rp.Component,
			"amount":     toMoneyView(rp.Amount),
			"applied_at": rp.AppliedAt,
		})
	}

	// B.2b repayment outlook — an honest projection over the SAME rows already loaded
	// (no extra query). time.Now() lives here in the handler layer; the computation
	// itself is a pure, deterministic function of (owed, repayments, recharges, asOf).
	outlook := repo.RepaymentOutlookFrom(prof.TotalOutstanding, prof.Repayments, prof.Recharges, time.Now())

	return map[string]any{
		"subscriber": map[string]any{
			"subscriber_account_id": id.SubscriberAccountID,
			"telco_id":              id.TelcoID,
			"msisdn_masked":         maskToken(id.MSISDNToken),
			"status":                id.Status,
			"effective_from":        id.EffectiveFrom,
		},
		"current_limit":     current,
		"limit_history":     hist,
		"loans":             loans,
		"total_outstanding": toMoneyView(prof.TotalOutstanding),
		"recharges":         recharges,
		"repayments":        repayments,
		"outlook":           outlookJSON(outlook),
	}
}

// outlookJSON renders the repayment outlook. All money is server-formatted; the client
// only displays these numbers, never computes them.
func outlookJSON(o repo.RepaymentOutlook) map[string]any {
	return map[string]any{
		"status":               o.Status,
		"window_days":          o.WindowDays,
		"owed":                 toMoneyView(o.Owed),
		"recovered_in_window":  toMoneyView(o.RecoveredInWindow),
		"active_weeks":         o.ActiveWeeks,
		"typical_weekly":       toMoneyView(o.TypicalWeekly),
		"optimistic_weeks":     o.OptimisticWeeks,
		"pessimistic_weeks":    o.PessimisticWeeks,
		"recharge_count":       o.RechargeCount,
		"typical_recharge":     toMoneyView(o.TypicalRecharge),
		"median_interval_days": o.MedianIntervalDays,
		"note":                 o.Note,
	}
}
