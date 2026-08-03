package handler_test

// Wave B MI Overview (dashboard) read-model. Adversarial assertions, one per way the
// dashboard could quietly lie:
//   1. B3 collected-today reads the RECOVERY_APPLIED SUBSCRIBER_RECEIVABLE reduction
//      dated today — not lifetime, not the wrong account.
//   2. B4 is an HONEST paydown ratio recovered/(recovered+open+written-off) — the
//      write-off LOWERS it (a bad loan can't flatter the number), and it is NOT a
//      due-based rate (no schedule exists).
//   3. A9 ₦-arrears-by-bucket carries real money per bucket, via toMoneyView.
//   4. C1 today-disbursed is the guardrail's OWN measure (accepted_at-based, excludes
//      DECLINED/FULFILMENT_FAILED) — reusing summary.Disbursed (lifetime, all-states)
//      would over-count; a seeded DECLINED advance proves the exclusion.
//   5. A10 exposure headroom = committed×governed-bps − (reserved+utilised), with the
//      bps read from config (not hardcoded); the '*' admin path exercises the 0067
//      op_all funding_pools/programmes policies (absent them it would fall to empty).
//   6. C3 returns the MOST RECENT breach (ORDER BY tripped_at DESC).
//   7. Scope fail-closed: a no-authority operator sees zeros and no programmes.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
)

type ovMoney struct {
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
	Display     string `json:"display"`
}

type ovProgramme struct {
	ProgrammeID    string  `json:"programme_id"`
	TelcoID        string  `json:"telco_id"`
	Status         string  `json:"status"`
	TodayDisbursed ovMoney `json:"today_disbursed"`
	CapKnown       bool    `json:"cap_known"`
	DailyCap       ovMoney `json:"daily_cap"`
	DailyHeadroom  ovMoney `json:"daily_headroom"`
	Pool           *struct {
		Committed        ovMoney `json:"committed"`
		Exposure         ovMoney `json:"exposure"`
		ExposureLimit    ovMoney `json:"exposure_limit"`
		ExposureHeadroom ovMoney `json:"exposure_headroom"`
		ExposureBps      int64   `json:"exposure_bps"`
	} `json:"pool"`
	LastBreach *struct {
		Guardrail string  `json:"guardrail"`
		Measured  ovMoney `json:"measured"`
		Limit     ovMoney `json:"limit"`
		State     string  `json:"state"`
	} `json:"last_breach"`
}

type ovResponse struct {
	Summary struct {
		TotalCount      int64   `json:"total_count"`
		OpenOutstanding ovMoney `json:"open_outstanding"`
		Recovered       ovMoney `json:"recovered"`
		Reconciled      bool    `json:"reconciled"`
	} `json:"summary"`
	ByBucketValue       map[string]ovMoney `json:"by_bucket_value"`
	CollectedToday      ovMoney            `json:"collected_today"`
	WrittenOffPrincipal ovMoney            `json:"written_off_principal"`
	PaydownRatio        float64            `json:"paydown_ratio"`
	Programmes          []ovProgramme      `json:"programmes"`
}

// seedReceivableCredit posts a RECOVERY_APPLIED journal dated today crediting
// SUBSCRIBER_RECEIVABLE (money collected today reduces the receivable) so B3 has a
// real, dated figure to read.
func seedReceivableCredit(t *testing.T, f *portalFixture, n int, creditMinor int64) {
	t.Helper()
	ctx := context.Background()
	jid := fmt.Sprintf("jr_rcv_%d", n)
	stmts := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO journals (journal_id, business_event_key, event_type, telco_id, programme_id, correlation_id)
		  VALUES ($1,$2,'RECOVERY_APPLIED','SIM_NG','prg_sim_airtime01',$3)`,
			[]any{jid, fmt.Sprintf("bek_rcv_%d", n), fmt.Sprintf("corr_rcv_%d", n)}},
		{`INSERT INTO journal_entries (entry_id, journal_id, account_code, credit_minor, currency)
		  VALUES ($1,$2,'SUBSCRIBER_RECEIVABLE',$3,'NGN')`, []any{jid + "_c", jid, creditMinor}},
	}
	for _, q := range stmts {
		if _, err := f.db.Admin.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatalf("seed receivable credit: %v", err)
		}
	}
}

// seedWriteOff posts a WRITE_OFF_EXPENSE debit (a crystallised loss). B4's query
// filters only on the account code, so any journal carrying the entry qualifies.
func seedWriteOff(t *testing.T, f *portalFixture, n int, debitMinor int64) {
	t.Helper()
	ctx := context.Background()
	jid := fmt.Sprintf("jr_wo_%d", n)
	stmts := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO journals (journal_id, business_event_key, event_type, telco_id, programme_id, correlation_id)
		  VALUES ($1,$2,'WRITE_OFF','SIM_NG','prg_sim_airtime01',$3)`,
			[]any{jid, fmt.Sprintf("bek_wo_%d", n), fmt.Sprintf("corr_wo_%d", n)}},
		{`INSERT INTO journal_entries (entry_id, journal_id, account_code, debit_minor, currency)
		  VALUES ($1,$2,'WRITE_OFF_EXPENSE',$3,'NGN')`, []any{jid + "_d", jid, debitMinor}},
	}
	for _, q := range stmts {
		if _, err := f.db.Admin.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatalf("seed write-off: %v", err)
		}
	}
}

func overviewGET(t *testing.T, f *portalFixture, s *session) ovResponse {
	t.Helper()
	code, body := f.callBody(t, s, "GET", "/v1/portal/ops/overview", "")
	if code != http.StatusOK {
		t.Fatalf("overview: %d %s", code, body)
	}
	var resp ovResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal overview: %v — %s", err, body)
	}
	return resp
}

func TestOverview_AddTiles_B3_B4_A9_AndScopeFailClosed(t *testing.T) {
	f := newPortalFixture(t, "ov_addtiles")
	ops := f.login(t, roleKeys["OPS"])

	// One ACTIVE advance: outstanding 8000, recovered 2000. Bucket is NULL → UNCLASSIFIED.
	seedLoanBookAdvance(t, f, 1, "ACTIVE", 10000, 1000, 9000, 8000, 2000)
	seedReceivableCredit(t, f, 1, 1500) // B3: collected today = 1500
	seedWriteOff(t, f, 1, 3000)         // B4 denominator: written off = 3000

	r := overviewGET(t, f, &ops)

	// B3 — collected today is the dated RECOVERY_APPLIED receivable reduction, not lifetime.
	if r.CollectedToday.AmountMinor != 1500 {
		t.Fatalf("collected_today must be today's SUBSCRIBER_RECEIVABLE credit (1500), got %d", r.CollectedToday.AmountMinor)
	}
	// The money is rendered through toMoneyView (governed decimals → major units with ₦).
	if r.CollectedToday.Display == "" || r.CollectedToday.Currency != "NGN" {
		t.Fatalf("collected_today must be server-formatted NGN money, got %+v", r.CollectedToday)
	}
	// B4 written-off crystallised loss.
	if r.WrittenOffPrincipal.AmountMinor != 3000 {
		t.Fatalf("written_off_principal must be the WRITE_OFF_EXPENSE balance (3000), got %d", r.WrittenOffPrincipal.AmountMinor)
	}
	// B4 paydown ratio = recovered / (recovered + open + written-off) = 2000/(2000+8000+3000).
	want := 2000.0 / 13000.0
	if diff := r.PaydownRatio - want; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("paydown_ratio must be recovered/(recovered+open+written-off) = %.6f, got %.6f — the write-off must LOWER it, not be omitted", want, r.PaydownRatio)
	}
	// A9 — ₦ arrears in the (unclassified) bucket equals that advance's outstanding, as money.
	b, ok := r.ByBucketValue["UNCLASSIFIED"]
	if !ok || b.AmountMinor != 8000 {
		t.Fatalf("by_bucket_value[UNCLASSIFIED] must carry ₦8000 outstanding, got %+v (present=%v)", b, ok)
	}
	if b.Display == "" || b.Currency != "NGN" {
		t.Fatalf("A9 bucket money must be server-formatted, got %+v", b)
	}

	// Scope fail-closed: a no-authority (global-scoped) operator sees zeros and no programmes.
	ctx := context.Background()
	if err := (&repo.Admins{Pool: f.db.Admin}).CreateWithRole(ctx, "adm_ov_g", "ops_global_ov", "portal-key-ops-global-ov", "OPS", "global"); err != nil {
		t.Fatal(err)
	}
	g := f.login(t, "portal-key-ops-global-ov")
	gr := overviewGET(t, f, &g)
	if gr.Summary.TotalCount != 0 || gr.CollectedToday.AmountMinor != 0 || gr.WrittenOffPrincipal.AmountMinor != 0 {
		t.Fatalf("no-authority operator must see zeros, got count=%d collected=%d written=%d",
			gr.Summary.TotalCount, gr.CollectedToday.AmountMinor, gr.WrittenOffPrincipal.AmountMinor)
	}
	if gr.PaydownRatio != 0 {
		t.Fatalf("no-authority paydown_ratio must be 0 (denominator zero), got %.6f", gr.PaydownRatio)
	}
	if len(gr.Programmes) != 0 {
		t.Fatalf("no-authority operator must see NO programmes, got %d", len(gr.Programmes))
	}
	// Fail-closed money must still carry a currency (the early-return sets it), never "".
	if gr.CollectedToday.Currency != "NGN" {
		t.Fatalf("fail-closed collected_today must still be NGN, got %q", gr.CollectedToday.Currency)
	}
}

// insertGuardrailTrip inserts a trip row directly (bypassing the usecase) so C3 has a
// breach history to read. Admin pool → RLS bypassed.
func insertGuardrailTrip(t *testing.T, f *portalFixture, tripID, guardrail, state string, measured, limit int64, ageExpr string) {
	t.Helper()
	ctx := context.Background()
	if state == "REARMED" {
		if _, err := f.db.Admin.Exec(ctx, `
			INSERT INTO guardrail_trips (trip_id, telco_id, programme_id, guardrail, measured_minor, limit_minor,
			  currency, state, tripped_at, rearm_requested_by, rearm_approved_by, rearmed_at)
			VALUES ($1,'SIM_NG','prg_sim_airtime01',$2,$3,$4,'NGN','REARMED', now() - `+ageExpr+`, 'maker','checker', now())`,
			tripID, guardrail, measured, limit); err != nil {
			t.Fatalf("insert rearmed trip: %v", err)
		}
		return
	}
	if _, err := f.db.Admin.Exec(ctx, `
		INSERT INTO guardrail_trips (trip_id, telco_id, programme_id, guardrail, measured_minor, limit_minor,
		  currency, state, tripped_at)
		VALUES ($1,'SIM_NG','prg_sim_airtime01',$2,$3,$4,'NGN',$5, now() - `+ageExpr+`)`,
		tripID, guardrail, measured, limit, state); err != nil {
		t.Fatalf("insert trip: %v", err)
	}
}

func TestOverview_ProgrammeHealth_C1Excludes_A10Headroom_C3Latest(t *testing.T) {
	f := newPortalFixture(t, "ov_health")
	ops := f.login(t, roleKeys["OPS"]) // '*' scope → exercises 0067 op_all programmes/funding_pools

	// C1: an ACTIVE advance disbursed 9000 today, and a DECLINED advance disbursed 5000
	// (deliberately >0 to prove the state filter EXCLUDES it — a DECLINED advance never
	// really disburses, but the guardrail measure must skip it regardless).
	seedLoanBookAdvance(t, f, 20, "ACTIVE", 10000, 1000, 9000, 8000, 0)
	seedLoanBookAdvance(t, f, 21, "DECLINED", 6000, 1000, 5000, 0, 0) // face = fee + disbursed (offer_money_identity)

	// C3: an older REARMED OPEN_EXPOSURE breach and a newer TRIPPED DAILY_DISBURSED breach.
	insertGuardrailTrip(t, f, "trp_ov_old", "OPEN_EXPOSURE", "REARMED", 100, 90, "interval '2 days'")
	insertGuardrailTrip(t, f, "trp_ov_new", "DAILY_DISBURSED", "TRIPPED", 600000000, 500000000, "interval '1 hour'")

	r := overviewGET(t, f, &ops)

	var prog *ovProgramme
	for i := range r.Programmes {
		if r.Programmes[i].ProgrammeID == "prg_sim_airtime01" {
			prog = &r.Programmes[i]
			break
		}
	}
	if prog == nil {
		t.Fatalf("prg_sim_airtime01 must appear in the health section (0067 op_all path); programmes=%+v", r.Programmes)
	}

	// C2 — a fresh programme is ACTIVE.
	if prog.Status != "ACTIVE" {
		t.Fatalf("programme status must be ACTIVE, got %q", prog.Status)
	}

	// C1 — today-disbursed is the guardrail measure: 9000 only (DECLINED 5000 excluded).
	// If this were wired to summary.Disbursed (lifetime, all-states) it would read 14000.
	if prog.TodayDisbursed.AmountMinor != 9000 {
		t.Fatalf("today_disbursed must exclude DECLINED (expect 9000), got %d — C1 must not reuse summary.Disbursed", prog.TodayDisbursed.AmountMinor)
	}
	if !prog.CapKnown {
		t.Fatalf("cap_known must be true (treasury.guardrails is seeded for prg_sim_airtime01)")
	}
	// C1 headroom = cap − today (relational — robust to the governed cap's value).
	if prog.DailyHeadroom.AmountMinor != prog.DailyCap.AmountMinor-prog.TodayDisbursed.AmountMinor {
		t.Fatalf("daily_headroom must equal cap − today (%d − %d), got %d",
			prog.DailyCap.AmountMinor, prog.TodayDisbursed.AmountMinor, prog.DailyHeadroom.AmountMinor)
	}

	// A10 — pool present for the '*' admin (proves 0067 op_all_funding_pools), and the
	// exposure ceiling is committed × governed bps with headroom = limit − exposure.
	if prog.Pool == nil {
		t.Fatalf("pool must be present for the '*' admin — 0067 op_all_funding_pools missing?")
	}
	if prog.Pool.ExposureBps <= 0 || prog.Pool.ExposureBps > 10000 {
		t.Fatalf("exposure_bps must be the governed ceiling in (0,10000], got %d", prog.Pool.ExposureBps)
	}
	wantLimit := prog.Pool.Committed.AmountMinor * prog.Pool.ExposureBps / 10000
	if prog.Pool.ExposureLimit.AmountMinor != wantLimit {
		t.Fatalf("exposure_limit must be committed×bps/10000 = %d, got %d (bps not read from config?)", wantLimit, prog.Pool.ExposureLimit.AmountMinor)
	}
	if prog.Pool.ExposureHeadroom.AmountMinor != prog.Pool.ExposureLimit.AmountMinor-prog.Pool.Exposure.AmountMinor {
		t.Fatalf("exposure_headroom must equal limit − exposure (%d − %d), got %d",
			prog.Pool.ExposureLimit.AmountMinor, prog.Pool.Exposure.AmountMinor, prog.Pool.ExposureHeadroom.AmountMinor)
	}

	// C3 — the MOST RECENT breach wins (the TRIPPED DAILY_DISBURSED from 1h ago, not the
	// 2-day-old REARMED one).
	if prog.LastBreach == nil {
		t.Fatalf("last_breach must be present (two trips seeded)")
	}
	if prog.LastBreach.Guardrail != "DAILY_DISBURSED" || prog.LastBreach.State != "TRIPPED" {
		t.Fatalf("last_breach must be the most recent trip (DAILY_DISBURSED/TRIPPED), got %s/%s", prog.LastBreach.Guardrail, prog.LastBreach.State)
	}
	if prog.LastBreach.Measured.AmountMinor != 600000000 || prog.LastBreach.Limit.AmountMinor != 500000000 {
		t.Fatalf("last_breach money mismatch: measured=%d limit=%d", prog.LastBreach.Measured.AmountMinor, prog.LastBreach.Limit.AmountMinor)
	}
}
