package handler_test

// Wave B.1 loan-book read-model. Load-bearing assertions: (1) the summary maps each
// aggregate to the right field — disbursed / recovered / open-outstanding are distinct
// so a column/variable swap (the bug this test was written to lock down) is caught;
// (2) masking — the full msisdn token never leaves the platform; (3) scope is
// fail-closed — a global/unauthorised operator sees nothing.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
)

// seedLoanBookAdvance seeds the subscriber→snapshot→offer→advance chain (as origination
// would leave it) plus, when recovered>0, a recovery event + allocation, all via the
// admin pool. Amounts are caller-chosen so the summary aggregates are distinguishable.
func seedLoanBookAdvance(t *testing.T, f *portalFixture, n int, state string, face, fee, disb, out, recovered int64) (advID, token string) {
	t.Helper()
	ctx := context.Background()
	sub := fmt.Sprintf("sub_b1_%d", n)
	snap := fmt.Sprintf("dsn_b1_%d", n)
	offer := fmt.Sprintf("ofr_b1_%d", n)
	advID = fmt.Sprintf("adv_b1_%d", n)
	token = fmt.Sprintf("tok_b1_%d", n)
	ev := fmt.Sprintf("ev_b1_%d", n)
	alloc := fmt.Sprintf("all_b1_%d", n)

	stmts := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO subscriber_accounts (subscriber_account_id, telco_id, msisdn_token, status)
		  VALUES ($1,'SIM_NG',$2,'ACTIVE')`, []any{sub, token}},
		{`INSERT INTO decision_snapshots (decision_snapshot_id, telco_id, subscriber_account_id,
		    max_face_value_minor, currency, config_version_id)
		  VALUES ($1,'SIM_NG',$2,50000,'NGN','cfg_seed_scoring_policy_v3')`, []any{snap, sub}},
		{`INSERT INTO offers (offer_id, telco_id, programme_id, subscriber_account_id,
		    decision_snapshot_id, face_value_minor, fee_minor, disbursed_minor, repayment_minor,
		    currency, fee_model, product_config_version_id, state, expires_at)
		  VALUES ($1,'SIM_NG','prg_sim_airtime01',$2,$3,$4,$5,$6,$4,'NGN',
		    'DEDUCTED_UPFRONT','cfg_seed_product_airtime_v1','ACCEPTED', now() + interval '1 day')`,
			[]any{offer, sub, snap, face, fee, disb}},
		{`INSERT INTO advances (advance_id, telco_id, programme_id, subscriber_account_id, offer_id,
		    funding_pool_id, idempotency_key, correlation_id, state, face_value_minor, fee_minor,
		    disbursed_minor, outstanding_minor, currency, activated_at)
		  VALUES ($1,'SIM_NG','prg_sim_airtime01',$2,$3,'pool_sim_01',$1,$1,$4,$5,$6,$7,$8,'NGN', now())`,
			[]any{advID, sub, offer, state, face, fee, disb, out}},
	}
	if recovered > 0 {
		stmts = append(stmts,
			struct {
				sql  string
				args []any
			}{`INSERT INTO recovery_events (recovery_event_id, telco_id, source_event_id, msisdn_token,
			    amount_minor, currency, state, occurred_at)
			  VALUES ($1,'SIM_NG',$2,$3,$4,'NGN','ALLOCATED', now())`, []any{ev, "wh:" + ev, token, recovered}},
			struct {
				sql  string
				args []any
			}{`INSERT INTO recovery_allocations (allocation_id, recovery_event_id, advance_id, component, amount_minor, currency)
			  VALUES ($1,$2,$3,'PRINCIPAL',$4,'NGN')`, []any{alloc, ev, advID, recovered}},
		)
	}
	for _, q := range stmts {
		if _, err := f.db.Admin.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatalf("seed loan-book advance: %v", err)
		}
	}
	return advID, token
}

func TestB1_LoanBook_SummaryFieldMapping_MaskingAndScope(t *testing.T) {
	f := newPortalFixture(t, "b1_loanbook")
	ops := f.login(t, roleKeys["OPS"])

	// Distinct amounts so a field swap is detectable: disbursed 9000, outstanding 8000,
	// recovered 2000 (an active advance the loan book must report exactly).
	_, token := seedLoanBookAdvance(t, f, 1, "ACTIVE", 10000, 1000, 9000, 8000, 2000)

	code, body := f.callBody(t, &ops, "GET", "/v1/portal/ops/advances", "")
	if code != http.StatusOK {
		t.Fatalf("loan book: %d %s", code, body)
	}
	// Masking, strong form: the FULL token appears nowhere in the response.
	if bytes.Contains(body, []byte(token)) {
		t.Fatalf("loan book must never carry the full msisdn token: %s", body)
	}

	var resp struct {
		Advances []struct {
			MSISDNMasked string `json:"msisdn_masked"`
			Outstanding  struct {
				AmountMinor int64 `json:"amount_minor,string"`
			} `json:"outstanding"`
		} `json:"advances"`
		Summary struct {
			Disbursed struct {
				AmountMinor int64 `json:"amount_minor,string"`
			} `json:"disbursed"`
			Recovered struct {
				AmountMinor int64 `json:"amount_minor,string"`
			} `json:"recovered"`
			OpenOutstanding struct {
				AmountMinor int64 `json:"amount_minor,string"`
			} `json:"open_outstanding"`
			TotalCount int64            `json:"total_count"`
			ByStatus   map[string]int64 `json:"by_status"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal: %v — %s", err, body)
	}

	// Field mapping — the swapped-variable bug this test locks down would have put the
	// recovered total (2000) into open_outstanding.
	if resp.Summary.OpenOutstanding.AmountMinor != 8000 {
		t.Fatalf("open_outstanding must be Σ outstanding of ACTIVE advances (8000), got %d — summary field mapping regressed", resp.Summary.OpenOutstanding.AmountMinor)
	}
	if resp.Summary.Recovered.AmountMinor != 2000 {
		t.Fatalf("recovered must be Σ recovery_allocations (2000), got %d", resp.Summary.Recovered.AmountMinor)
	}
	if resp.Summary.Disbursed.AmountMinor != 9000 {
		t.Fatalf("disbursed must be Σ disbursed_minor (9000), got %d", resp.Summary.Disbursed.AmountMinor)
	}
	if resp.Summary.TotalCount != 1 || resp.Summary.ByStatus["ACTIVE"] != 1 {
		t.Fatalf("summary counts wrong: total=%d by_status=%v", resp.Summary.TotalCount, resp.Summary.ByStatus)
	}
	if len(resp.Advances) != 1 || !strings.HasPrefix(resp.Advances[0].MSISDNMasked, "…") {
		t.Fatalf("row must carry a masked token: %s", body)
	}
	if resp.Advances[0].Outstanding.AmountMinor != 8000 {
		t.Fatalf("row outstanding must be 8000, got %d", resp.Advances[0].Outstanding.AmountMinor)
	}

	// Scope fail-closed: a global-scoped operator (no telco authority) sees nothing.
	ctx := context.Background()
	if err := (&repo.Admins{Pool: f.db.Admin}).CreateWithRole(ctx, "adm_b1_g", "ops_global_b1", "portal-key-ops-global-b1", "OPS", "global"); err != nil {
		t.Fatal(err)
	}
	gSess := f.login(t, "portal-key-ops-global-b1")
	_, gBody := f.callBody(t, &gSess, "GET", "/v1/portal/ops/advances", "")
	var gr struct {
		Advances []json.RawMessage `json:"advances"`
		Summary  struct {
			TotalCount int64 `json:"total_count"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(gBody, &gr); err != nil {
		t.Fatalf("unmarshal global: %v — %s", err, gBody)
	}
	if len(gr.Advances) != 0 || gr.Summary.TotalCount != 0 {
		t.Fatalf("global-scoped operator must see NO advances (fail-closed), got %d rows / count %d", len(gr.Advances), gr.Summary.TotalCount)
	}
}

// seedFeeIncome posts raw FEE_INCOME ledger entries for SIM_NG so the summary's
// revenue-recognized cross-foot has something distinct to read: a credit (fee earned)
// and a debit (fee de-recognized on a reversal) — the balance is credit − debit.
// entry_single_side (0004) forbids a two-sided row, so each leg is its own entry.
func seedFeeIncome(t *testing.T, f *portalFixture, n int, creditMinor, debitMinor int64) {
	t.Helper()
	ctx := context.Background()
	jid := fmt.Sprintf("jr_fee_%d", n)
	stmts := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO journals (journal_id, business_event_key, event_type, telco_id, programme_id, correlation_id)
		  VALUES ($1,$2,'RECOVERY_APPLIED','SIM_NG','prg_sim_airtime01',$3)`,
			[]any{jid, fmt.Sprintf("bek_fee_%d", n), fmt.Sprintf("corr_fee_%d", n)}},
		{`INSERT INTO journal_entries (entry_id, journal_id, account_code, credit_minor, currency)
		  VALUES ($1,$2,'FEE_INCOME',$3,'NGN')`, []any{jid + "_c", jid, creditMinor}},
	}
	if debitMinor > 0 {
		stmts = append(stmts, struct {
			sql  string
			args []any
		}{`INSERT INTO journal_entries (entry_id, journal_id, account_code, debit_minor, currency)
		  VALUES ($1,$2,'FEE_INCOME',$3,'NGN')`, []any{jid + "_d", jid, debitMinor}})
	}
	for _, q := range stmts {
		if _, err := f.db.Admin.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatalf("seed fee income: %v", err)
		}
	}
}

// Revenue recognized is the ledger FEE_INCOME balance (credit − debit) — reported
// BESIDE outstanding, never inside it — and it must reconcile to the same ledger and
// obey the same scope as the receivable cross-foot. The chosen value (3000 − 500 =
// 2500) is DISTINCT from every loan-book money aggregate, so reading the wrong account
// (e.g. SUBSCRIBER_RECEIVABLE) or the wrong sign (crediting to a debit) is caught; and a
// no-authority operator must see 0, not the real figure.
func TestB1_LoanBook_RevenueRecognized_FromFeeIncomeLedger(t *testing.T) {
	f := newPortalFixture(t, "b1_revenue")
	ops := f.login(t, roleKeys["OPS"])

	seedLoanBookAdvance(t, f, 7, "ACTIVE", 10000, 1000, 9000, 8000, 0)
	seedFeeIncome(t, f, 7, 3000, 500) // earned 3000, reversed 500 → recognized 2500

	code, body := f.callBody(t, &ops, "GET", "/v1/portal/ops/advances", "")
	if code != http.StatusOK {
		t.Fatalf("loan book: %d %s", code, body)
	}
	var resp struct {
		Summary struct {
			RevenueRecognized struct {
				AmountMinor int64  `json:"amount_minor,string"`
				Currency    string `json:"currency"`
			} `json:"revenue_recognized"`
			OpenOutstanding struct {
				AmountMinor int64 `json:"amount_minor,string"`
			} `json:"open_outstanding"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal: %v — %s", err, body)
	}
	if resp.Summary.RevenueRecognized.AmountMinor != 2500 {
		t.Fatalf("revenue_recognized must be Σ(credit-debit) over FEE_INCOME (2500), got %d — wrong account or wrong sign", resp.Summary.RevenueRecognized.AmountMinor)
	}
	// Revenue is BESIDE outstanding, not mixed into it.
	if resp.Summary.OpenOutstanding.AmountMinor != 8000 {
		t.Fatalf("open_outstanding must stay 8000 (revenue must not leak into it), got %d", resp.Summary.OpenOutstanding.AmountMinor)
	}

	// Scope fail-closed: a no-authority (global-scoped) operator sees revenue 0 with a
	// proper currency (the early-return must set every money field), not the real 2500.
	ctx := context.Background()
	if err := (&repo.Admins{Pool: f.db.Admin}).CreateWithRole(ctx, "adm_rev_g", "ops_global_rev", "portal-key-ops-global-rev", "OPS", "global"); err != nil {
		t.Fatal(err)
	}
	gSess := f.login(t, "portal-key-ops-global-rev")
	_, gBody := f.callBody(t, &gSess, "GET", "/v1/portal/ops/advances", "")
	var gr struct {
		Summary struct {
			RevenueRecognized struct {
				AmountMinor int64  `json:"amount_minor,string"`
				Currency    string `json:"currency"`
			} `json:"revenue_recognized"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(gBody, &gr); err != nil {
		t.Fatalf("unmarshal global: %v — %s", err, gBody)
	}
	if gr.Summary.RevenueRecognized.AmountMinor != 0 || gr.Summary.RevenueRecognized.Currency != "NGN" {
		t.Fatalf("no-authority operator must see revenue 0 NGN (fail-closed, currency set), got %d %q", gr.Summary.RevenueRecognized.AmountMinor, gr.Summary.RevenueRecognized.Currency)
	}
}
