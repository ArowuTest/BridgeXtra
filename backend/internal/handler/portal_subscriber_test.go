package handler_test

// Wave B.2a Subscriber-360 — adversarial pass. The MSISDN is the account number, so the
// load-bearing properties are: (1) "still owed" is Σ outstanding over OPEN loans only
// and reconciles to the ledger SUBSCRIBER_RECEIVABLE (a CLOSED loan must not inflate it);
// (2) the full MSISDN never leaves the platform in a list or on the 360 — only via the
// reveal; (3) the reveal is FAIL-CLOSED and AUDITED (a real audit_events row, who/subject/
// when, and NOT the number itself); (4) scope is fail-closed — a no-authority operator
// sees nothing, gets 404 on a direct id, and a denied reveal writes NO audit row (so the
// reveal can't be used to probe another tenant's estate).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
)

// seedExtraAdvance adds another advance to an EXISTING subscriber (reusing its decision
// snapshot), so a subscriber can hold both an open and a closed loan — the case that
// proves "still owed" counts only the open one.
func seedExtraAdvance(t *testing.T, f *portalFixture, sub, snap, suffix, state string, disb, out int64) {
	t.Helper()
	ctx := context.Background()
	offer := "ofr_" + suffix
	adv := "adv_" + suffix
	stmts := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO offers (offer_id, telco_id, programme_id, subscriber_account_id,
		    decision_snapshot_id, face_value_minor, fee_minor, disbursed_minor, repayment_minor,
		    currency, fee_model, product_config_version_id, state, expires_at)
		  VALUES ($1,'SIM_NG','prg_sim_airtime01',$2,$3,$4,0,$4,$4,'NGN',
		    'DEDUCTED_UPFRONT','cfg_seed_product_airtime_v1','ACCEPTED', now() + interval '1 day')`,
			[]any{offer, sub, snap, disb}},
		{`INSERT INTO advances (advance_id, telco_id, programme_id, subscriber_account_id, offer_id,
		    funding_pool_id, idempotency_key, correlation_id, state, face_value_minor, fee_minor,
		    disbursed_minor, outstanding_minor, currency, activated_at, closed_at)
		  VALUES ($1,'SIM_NG','prg_sim_airtime01',$2,$3,'pool_sim_01',$1,$1,$4,$5,0,$5,$6,'NGN', now(),
		    CASE WHEN $4 = 'CLOSED' THEN now() ELSE NULL END)`,
			[]any{adv, sub, offer, state, disb, out}},
	}
	for _, q := range stmts {
		if _, err := f.db.Admin.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatalf("seed extra advance: %v", err)
		}
	}
}

// seedReceivable posts a SUBSCRIBER_RECEIVABLE ledger entry for SIM_NG so the per-
// subscriber "still owed" can be reconciled to the ledger balance (debit − credit).
func seedReceivable(t *testing.T, f *portalFixture, n int, debitMinor int64) {
	t.Helper()
	ctx := context.Background()
	jid := fmt.Sprintf("jr_recv_%d", n)
	stmts := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO journals (journal_id, business_event_key, event_type, telco_id, programme_id, correlation_id)
		  VALUES ($1,$2,'ADVANCE_ISSUED','SIM_NG','prg_sim_airtime01',$3)`,
			[]any{jid, fmt.Sprintf("bek_recv_%d", n), fmt.Sprintf("corr_recv_%d", n)}},
		{`INSERT INTO journal_entries (entry_id, journal_id, account_code, debit_minor, currency)
		  VALUES ($1,$2,'SUBSCRIBER_RECEIVABLE',$3,'NGN')`, []any{jid + "_d", jid, debitMinor}},
	}
	for _, q := range stmts {
		if _, err := f.db.Admin.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatalf("seed receivable: %v", err)
		}
	}
}

type money struct {
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
	Display     string `json:"display"`
}

func TestB2_Subscriber360_ReconcileMaskAndReveal(t *testing.T) {
	f := newPortalFixture(t, "b2_sub360")
	ops := f.login(t, roleKeys["OPS"])
	ctx := context.Background()

	// Subscriber sub_b1_1: one OPEN advance (out 8000, disbursed 9000, recovered 2000,
	// limit 50000 via the snapshot) plus one CLOSED advance (disbursed 5000, out 0).
	// "Still owed" must be 8000 (open only); "ever borrowed" 14000 (both).
	_, token := seedLoanBookAdvance(t, f, 1, "ACTIVE", 10000, 1000, 9000, 8000, 2000)
	sub := "sub_b1_1"
	seedExtraAdvance(t, f, sub, "dsn_b1_1", "b2_closed", "CLOSED", 5000, 0)
	// Ledger receivable of 8000 for SIM_NG — the profile total must reconcile to it.
	seedReceivable(t, f, 1, 8000)

	// --- directory: masked, open-only total, distinct rollups ---
	code, body := f.callBody(t, &ops, "GET", "/v1/portal/ops/subscribers", "")
	if code != http.StatusOK {
		t.Fatalf("directory: %d %s", code, body)
	}
	if strings.Contains(string(body), token) {
		t.Fatalf("directory must never carry the full msisdn token: %s", body)
	}
	var dir struct {
		Subscribers []struct {
			SubscriberAccountID string `json:"subscriber_account_id"`
			MSISDNMasked        string `json:"msisdn_masked"`
			OpenLoans           int64  `json:"open_loans"`
			TotalOutstanding    money  `json:"total_outstanding"`
			EverBorrowed        money  `json:"ever_borrowed"`
		} `json:"subscribers"`
	}
	if err := json.Unmarshal(body, &dir); err != nil {
		t.Fatalf("unmarshal directory: %v — %s", err, body)
	}
	var row *struct {
		SubscriberAccountID string `json:"subscriber_account_id"`
		MSISDNMasked        string `json:"msisdn_masked"`
		OpenLoans           int64  `json:"open_loans"`
		TotalOutstanding    money  `json:"total_outstanding"`
		EverBorrowed        money  `json:"ever_borrowed"`
	}
	for i := range dir.Subscribers {
		if dir.Subscribers[i].SubscriberAccountID == sub {
			row = &dir.Subscribers[i]
		}
	}
	if row == nil {
		t.Fatalf("subscriber %s missing from directory: %s", sub, body)
	}
	if !strings.HasPrefix(row.MSISDNMasked, "…") || !strings.HasSuffix(row.MSISDNMasked, token[len(token)-4:]) {
		t.Fatalf("directory row must carry a masked-tail token, got %q", row.MSISDNMasked)
	}
	if row.TotalOutstanding.AmountMinor != 8000 {
		t.Fatalf("still owed must be Σ open outstanding (8000), got %d — a CLOSED loan leaked in", row.TotalOutstanding.AmountMinor)
	}
	if row.EverBorrowed.AmountMinor != 14000 {
		t.Fatalf("ever borrowed must be Σ disbursed of BOTH loans (14000), got %d", row.EverBorrowed.AmountMinor)
	}
	if row.OpenLoans != 1 {
		t.Fatalf("open loans must be 1 (CLOSED excluded), got %d", row.OpenLoans)
	}

	// --- 360: masked, reconciles to ledger, history + recharges + repayments present ---
	code, body = f.callBody(t, &ops, "GET", "/v1/portal/ops/subscribers/"+sub, "")
	if code != http.StatusOK {
		t.Fatalf("profile: %d %s", code, body)
	}
	if strings.Contains(string(body), token) {
		t.Fatalf("360 must never carry the full msisdn token: %s", body)
	}
	var prof struct {
		Subscriber struct {
			MSISDNMasked string `json:"msisdn_masked"`
		} `json:"subscriber"`
		CurrentLimit *struct {
			Limit money  `json:"limit"`
			Tier  string `json:"tier"`
		} `json:"current_limit"`
		LimitHistory     []json.RawMessage `json:"limit_history"`
		Loans            []json.RawMessage `json:"loans"`
		TotalOutstanding money             `json:"total_outstanding"`
		Recharges        []struct {
			Amount  money `json:"amount"`
			Applied money `json:"applied"`
		} `json:"recharges"`
		Repayments []struct {
			Component string `json:"component"`
			Amount    money  `json:"amount"`
		} `json:"repayments"`
		Outlook struct {
			Status            string `json:"status"`
			RecoveredInWindow money  `json:"recovered_in_window"`
			OptimisticWeeks   int    `json:"optimistic_weeks"`
		} `json:"outlook"`
	}
	if err := json.Unmarshal(body, &prof); err != nil {
		t.Fatalf("unmarshal profile: %v — %s", err, body)
	}
	// B.2b outlook is wired into the 360. This subscriber has ONE repayment (~now), so
	// the projection must HONESTLY degrade to INSUFFICIENT — never invent a date/range.
	if prof.Outlook.Status != "INSUFFICIENT" {
		t.Fatalf("one repayment must degrade to INSUFFICIENT (no fabricated date), got %q", prof.Outlook.Status)
	}
	if prof.Outlook.OptimisticWeeks != 0 {
		t.Fatalf("a non-projected outlook must not carry a week range, got %d", prof.Outlook.OptimisticWeeks)
	}
	if prof.Outlook.RecoveredInWindow.AmountMinor != 2000 {
		t.Fatalf("outlook must still report the facts (2000 repaid in window), got %d", prof.Outlook.RecoveredInWindow.AmountMinor)
	}
	if prof.TotalOutstanding.AmountMinor != 8000 {
		t.Fatalf("360 still-owed must be 8000 and reconcile to the ledger receivable (8000), got %d", prof.TotalOutstanding.AmountMinor)
	}
	// Governed money format end-to-end: 8000 minor NGN (governed decimals=2) renders as
	// the major amount with the symbol — not "NGN 8,000 (minor)".
	if prof.TotalOutstanding.Display != "₦80.00" {
		t.Fatalf("money must render as governed major units (₦80.00), got %q", prof.TotalOutstanding.Display)
	}
	if len(prof.Loans) != 2 {
		t.Fatalf("360 must list BOTH loans (open + closed), got %d", len(prof.Loans))
	}
	if prof.CurrentLimit == nil || prof.CurrentLimit.Limit.AmountMinor != 50000 {
		t.Fatalf("360 must surface the current credit limit (50000) from decision_snapshots, got %+v", prof.CurrentLimit)
	}
	if len(prof.LimitHistory) < 1 {
		t.Fatalf("360 must surface the limit/tier history, got %d points", len(prof.LimitHistory))
	}
	if len(prof.Recharges) < 1 || prof.Recharges[0].Amount.AmountMinor != 2000 {
		t.Fatalf("360 must surface the recharge (2000 in), got %+v", prof.Recharges)
	}
	if len(prof.Repayments) < 1 || prof.Repayments[0].Amount.AmountMinor != 2000 {
		t.Fatalf("360 must surface the repayment (2000 toward the loan), got %+v", prof.Repayments)
	}

	// Reconcile to the ledger cross-foot explicitly: the loan book (same read-model) must
	// report ledger_receivable == 8000 == the profile total.
	_, lb := f.callBody(t, &ops, "GET", "/v1/portal/ops/advances?msisdn_token="+token, "")
	var lbr struct {
		Summary struct {
			LedgerReceivable money `json:"ledger_receivable"`
			OpenOutstanding  money `json:"open_outstanding"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(lb, &lbr); err != nil {
		t.Fatalf("unmarshal loan book: %v — %s", err, lb)
	}
	if lbr.Summary.LedgerReceivable.AmountMinor != 8000 || prof.TotalOutstanding.AmountMinor != lbr.Summary.LedgerReceivable.AmountMinor {
		t.Fatalf("per-subscriber still-owed (%d) must reconcile to the ledger SUBSCRIBER_RECEIVABLE (%d)",
			prof.TotalOutstanding.AmountMinor, lbr.Summary.LedgerReceivable.AmountMinor)
	}

	// --- reveal: fail-closed + audited, and the number is NOT written into the audit ---
	code, body = f.callBody(t, &ops, "POST", "/v1/portal/ops/subscribers/"+sub+"/reveal", "")
	if code != http.StatusOK {
		t.Fatalf("reveal: %d %s", code, body)
	}
	var rev struct {
		MSISDN string `json:"msisdn"`
	}
	if err := json.Unmarshal(body, &rev); err != nil {
		t.Fatalf("unmarshal reveal: %v — %s", err, body)
	}
	if rev.MSISDN != token {
		t.Fatalf("reveal must return the FULL number %q, got %q", token, rev.MSISDN)
	}
	var cnt int
	var actor, detail string
	if err := f.db.Admin.QueryRow(ctx,
		`SELECT count(*), coalesce(max(actor),''), coalesce(max(detail::text),'')
		 FROM audit_events WHERE action='SUBSCRIBER_MSISDN_REVEALED' AND target_id=$1`, sub).
		Scan(&cnt, &actor, &detail); err != nil {
		t.Fatalf("read reveal audit: %v", err)
	}
	if cnt != 1 {
		t.Fatalf("a reveal must write exactly one audit event, got %d", cnt)
	}
	if actor != ops.actor {
		t.Fatalf("the reveal audit must record the actor %q, got %q", ops.actor, actor)
	}
	if strings.Contains(detail, token) {
		t.Fatalf("the audit must NOT store the full number: %s", detail)
	}
	if !strings.Contains(detail, "masked") {
		t.Fatalf("the audit detail should record the masked tail and telco: %s", detail)
	}

	// --- fail-closed scope: a no-authority (global) operator sees nothing, 404s on the id,
	// and a DENIED reveal writes NO audit row (reveal can't probe another tenant). ---
	if err := (&repo.Admins{Pool: f.db.Admin}).CreateWithRole(ctx, "adm_b2_g", "ops_global_b2", "portal-key-ops-global-b2", "OPS", "global"); err != nil {
		t.Fatal(err)
	}
	g := f.login(t, "portal-key-ops-global-b2")

	_, gBody := f.callBody(t, &g, "GET", "/v1/portal/ops/subscribers", "")
	var gd struct {
		Subscribers []json.RawMessage `json:"subscribers"`
	}
	if err := json.Unmarshal(gBody, &gd); err != nil {
		t.Fatalf("unmarshal global directory: %v — %s", err, gBody)
	}
	if len(gd.Subscribers) != 0 {
		t.Fatalf("a no-authority operator must see NO subscribers (fail-closed), got %d", len(gd.Subscribers))
	}
	if pc, _ := f.callBody(t, &g, "GET", "/v1/portal/ops/subscribers/"+sub, ""); pc != http.StatusNotFound {
		t.Fatalf("a no-authority operator must get 404 on a direct subscriber id, got %d", pc)
	}
	if rc, _ := f.callBody(t, &g, "POST", "/v1/portal/ops/subscribers/"+sub+"/reveal", ""); rc != http.StatusNotFound {
		t.Fatalf("a no-authority reveal must be 404 (no oracle), got %d", rc)
	}
	// The denied reveal must NOT have written an audit row — count stays 1.
	if err := f.db.Admin.QueryRow(ctx,
		`SELECT count(*) FROM audit_events WHERE action='SUBSCRIBER_MSISDN_REVEALED' AND target_id=$1`, sub).Scan(&cnt); err != nil {
		t.Fatalf("recount reveal audit: %v", err)
	}
	if cnt != 1 {
		t.Fatalf("a DENIED reveal must not write an audit row — count must stay 1, got %d", cnt)
	}
}
