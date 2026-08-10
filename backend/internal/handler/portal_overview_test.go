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
	AmountMinor int64  `json:"amount_minor,string"`
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
	ByBucketValue  map[string]ovMoney `json:"by_bucket_value"`
	CollectedToday ovMoney            `json:"collected_today"`
	WrittenOff     ovMoney            `json:"written_off"`
	PaydownBps     int64              `json:"paydown_bps"`
	Programmes     []ovProgramme      `json:"programmes"`
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

// seedWriteOff posts a WRITE_OFF journal mirroring template 0049: DR WRITE_OFF_EXPENSE =
// faceMinor (the full outstanding written off), and — under DEFERRED — a CR
// WRITE_OFF_EXPENSE = unearnedFeeMinor that nets the unearned fee back out of the *expense*.
// The paydown denominator must read the DR leg (full face), NOT the net (debit − credit), so
// a DEFERRED write-off lowers the ratio by its full owed amount (D1). Pass unearnedFeeMinor=0
// for the UPFRONT / no-deferred-fee case.
func seedWriteOff(t *testing.T, f *portalFixture, n int, faceMinor, unearnedFeeMinor int64) {
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
		  VALUES ($1,$2,'WRITE_OFF_EXPENSE',$3,'NGN')`, []any{jid + "_d", jid, faceMinor}},
	}
	if unearnedFeeMinor > 0 {
		stmts = append(stmts, struct {
			sql  string
			args []any
		}{`INSERT INTO journal_entries (entry_id, journal_id, account_code, credit_minor, currency)
		  VALUES ($1,$2,'WRITE_OFF_EXPENSE',$3,'NGN')`, []any{jid + "_c", jid, unearnedFeeMinor}})
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
	seedReceivableCredit(t, f, 1, 1500) // B3: collected today = 1500 minor = ₦15.00
	seedWriteOff(t, f, 1, 3000, 0)      // B4 denominator: written off face = 3000 minor = ₦30.00

	r := overviewGET(t, f, &ops)

	// B3 — collected today is the dated RECOVERY_APPLIED receivable reduction, not lifetime.
	if r.CollectedToday.AmountMinor != 1500 {
		t.Fatalf("collected_today must be today's SUBSCRIBER_RECEIVABLE credit (1500), got %d", r.CollectedToday.AmountMinor)
	}
	// The money is rendered through toMoneyView (governed decimals → major units with ₦). Pin
	// the exact string: a decimal-scale regression (minor-as-major, or a stray /1000) would
	// render ₦1,500.00 or ₦0.15 here and ship green against an amount_minor-only assertion —
	// exactly the class the loan-scale fix was about.
	if r.CollectedToday.Display != "₦15.00" {
		t.Fatalf("collected_today must render ₦15.00 (1500 minor, NGN=2dp), got %q", r.CollectedToday.Display)
	}
	if r.CollectedToday.Currency != "NGN" {
		t.Fatalf("collected_today currency must be NGN, got %q", r.CollectedToday.Currency)
	}
	// B4 written-off face (full outstanding written off, fee-inclusive).
	if r.WrittenOff.AmountMinor != 3000 || r.WrittenOff.Display != "₦30.00" {
		t.Fatalf("written_off must be the WRITE_OFF face (3000 minor = ₦30.00), got %d / %q", r.WrittenOff.AmountMinor, r.WrittenOff.Display)
	}
	// B4 paydown ratio = recovered / (recovered + open + written-off) = 2000/(2000+8000+3000).
	want := 2000.0 / 13000.0
	if wantBps := int64(want*10000 + 0.5); r.PaydownBps != wantBps {
		t.Fatalf("paydown_bps must be recovered/(recovered+open+written-off) = %d bps, got %d — the write-off must LOWER it, not be omitted", wantBps, r.PaydownBps)
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
	if gr.Summary.TotalCount != 0 || gr.CollectedToday.AmountMinor != 0 || gr.WrittenOff.AmountMinor != 0 {
		t.Fatalf("no-authority operator must see zeros, got count=%d collected=%d written=%d",
			gr.Summary.TotalCount, gr.CollectedToday.AmountMinor, gr.WrittenOff.AmountMinor)
	}
	if gr.PaydownBps != 0 {
		t.Fatalf("no-authority paydown_bps must be 0 (denominator zero), got %d", gr.PaydownBps)
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
	// exposure_bps must come from the GOVERNED config, not a hardcoded constant. Read the
	// effective treasury.guardrails ceiling (programme scope, else global — the same fallback
	// configsvc.ActiveAt uses) and assert the response echoes it. A hardcoded 8000 would drift
	// the instant the config is superseded to a new bps.
	cfgBps := activeGuardrailBps(t, f, "prg_sim_airtime01")
	if prog.Pool.ExposureBps != cfgBps {
		t.Fatalf("exposure_bps must equal the governed config value %d, got %d (bps not read from config?)", cfgBps, prog.Pool.ExposureBps)
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

// End-to-end telco isolation for the overview: a telco-SIM_NG-scoped operator (not '*',
// not global) must see its own programme in the health section but NEVER another telco's.
// The handler tests otherwise only drive the '*' and no-authority extremes; this exercises
// the scope-derivation → OperatorReader(app.telco_id) → ProgrammesInScope(RLS) path that
// 0067's op_all_programmes must NOT widen for a telco operator.
func TestOverview_TelcoScopedOperator_ExcludesOtherTelco(t *testing.T) {
	f := newPortalFixture(t, "ov_telcoscope")
	ctx := context.Background()
	for _, s := range []string{
		`INSERT INTO telcos (telco_id, name, country, status) VALUES ('OTHER_NG','Other','NG','ACTIVE') ON CONFLICT DO NOTHING`,
		`INSERT INTO programmes (programme_id, telco_id, code, name, status)
		   VALUES ('prg_other','OTHER_NG','OTHER_AIRTIME','Other Airtime','ACTIVE')`,
	} {
		if _, err := f.db.Admin.Exec(ctx, s); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	if err := (&repo.Admins{Pool: f.db.Admin}).CreateWithRole(ctx, "adm_ov_ts", "ops_sim_ts", "portal-key-ops-sim-ts", "OPS", "telco:SIM_NG"); err != nil {
		t.Fatal(err)
	}
	sess := f.login(t, "portal-key-ops-sim-ts")
	r := overviewGET(t, f, &sess)

	var sawSim, sawOther bool
	for _, p := range r.Programmes {
		switch p.ProgrammeID {
		case "prg_sim_airtime01":
			sawSim = true
			if p.TelcoID != "SIM_NG" {
				t.Fatalf("scoped programme must be SIM_NG, got %q", p.TelcoID)
			}
		case "prg_other":
			sawOther = true
		}
	}
	if !sawSim {
		t.Fatalf("a SIM_NG-scoped operator must see its own programme; programmes=%+v", r.Programmes)
	}
	if sawOther {
		t.Fatalf("a SIM_NG-scoped operator must NOT see OTHER_NG's programme (end-to-end telco isolation); programmes=%+v", r.Programmes)
	}
}

// activeGuardrailBps reads the effective treasury.guardrails max_open_exposure_bps for a
// programme (programme scope preferred, else global — mirroring configsvc.ActiveAt's
// fallback), so the exposure_bps assertion's expected value comes from the governed config
// itself, not a literal baked into the test.
func activeGuardrailBps(t *testing.T, f *portalFixture, programmeID string) int64 {
	t.Helper()
	var bps int64
	if err := f.db.Admin.QueryRow(context.Background(), `
		SELECT (content->>'max_open_exposure_bps_of_committed')::bigint
		FROM config_versions
		WHERE domain = 'treasury.guardrails' AND state = 'ACTIVE'
		  AND scope IN ('programme:' || $1, 'global')
		ORDER BY (scope = 'programme:' || $1) DESC
		LIMIT 1`, programmeID).Scan(&bps); err != nil {
		t.Fatalf("read active guardrail bps: %v", err)
	}
	return bps
}

// D1 — the paydown denominator uses the FULL FACE written off, not the net WRITE_OFF_EXPENSE.
// Under DEFERRED, a write-off posts DR WRITE_OFF_EXPENSE = face and CR WRITE_OFF_EXPENSE =
// unearned fee, so the net (debit − credit) is principal-only while recovered/open are
// fee-inclusive — the old net basis would UNDERSTATE the denominator and OVERSTATE the ratio.
// This seeds a DEFERRED write-off (face 1000, unearned fee 300) and asserts written_off reads
// the full 1000 and the ratio uses it.
func TestOverview_PaydownDenominator_UsesFullFaceWrittenOff(t *testing.T) {
	f := newPortalFixture(t, "ov_paydown_face")
	ops := f.login(t, roleKeys["OPS"])

	// An ACTIVE advance: open 4000, recovered 1000 (both fee-inclusive).
	seedLoanBookAdvance(t, f, 30, "ACTIVE", 5000, 500, 4500, 4000, 1000)
	// A DEFERRED write-off: full face 1000, of which 300 was unearned fee reversed out. The
	// net expense (debit − credit) is 700; the face (debit) is 1000.
	seedWriteOff(t, f, 30, 1000, 300)

	r := overviewGET(t, f, &ops)

	// written_off must be the full face (1000), NOT the net expense (700).
	if r.WrittenOff.AmountMinor != 1000 {
		t.Fatalf("written_off must be the FULL FACE written off (1000), got %d — a net (debit−credit) basis would read 700 and overstate the ratio", r.WrittenOff.AmountMinor)
	}
	// Denominator = recovered + open + written-off face = 1000 + 4000 + 1000 = 6000.
	// The old net basis would give 1000/(1000+4000+700) = 0.1724 — higher (overstated).
	want := 1000.0 / 6000.0
	if wantBps := int64(want*10000 + 0.5); r.PaydownBps != wantBps {
		t.Fatalf("paydown_bps must use the full face in the denominator (= %d bps), got %d", wantBps, r.PaydownBps)
	}
}
