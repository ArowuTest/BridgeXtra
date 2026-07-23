package settlement_test

// Deferred fee recognition at the SETTLEMENT boundary (reviewer scope-close).
//
// PeriodAggregates now sources FEE_INCOME from the NET account movement across
// all event types in the window, so a settlement statement's fee income — and
// every line derived from it (telco/platform share, taxes) — follows
// RECOGNITION, not issuance. The seeded default policy is DEFERRED, so every
// advance below defers its fee: FEE_INCOME nets to 0 at issuance and is
// recognised only as recovery lands.
//
// These prove the COMPLEMENT the reviewer required:
//  (a) issue + full write-off, no recovery  -> fee income (and shares/taxes) 0,
//      both in the issuance period AND across the whole defaulted life;
//  (b) recovery in a LATER period           -> recognition appears there, not at
//      issuance;
//  (c) partial recovery                     -> proportional to the allocated
//      fee-portion.

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/ledger"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/collections"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/origination"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/recovery"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/settlement"
)

const (
	dfProgramme = "prg_sim_airtime01"
	dfToken     = "tok_sim_0001"
	dfTelco     = "SIM_NG"
)

// acctBal is an account's credit-normal balance (credit - debit) across the
// whole ledger — used to PROVE each advance actually deferred (FEE_INCOME 0,
// UNEARNED_FEE = fee at issuance) so the settlement assertions cannot pass
// vacuously under an accidental UPFRONT flip.
func acctBal(t *testing.T, db *testutil.DB, account string) int64 {
	t.Helper()
	var v int64
	if err := db.Admin.QueryRow(context.Background(),
		`SELECT COALESCE(SUM(credit_minor - debit_minor),0) FROM journal_entries WHERE account_code=$1`,
		account).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// confirmOne originates one advance for the seeded token and returns its id and
// fee — under whatever fee_recognition policy is active.
func confirmOne(t *testing.T, orig *origination.Service, db *testutil.DB, idem string) (advanceID string, fee int64) {
	t.Helper()
	ctx := tenantCtx()
	offers, err := orig.GetOffers(ctx, dfProgramme, dfToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := orig.Confirm(ctx, origination.ConfirmCmd{
		ProgrammeID: dfProgramme, OfferID: offers[0].Offer.OfferID, MSISDNToken: dfToken,
		IdemKey: idem, CorrelationID: "cor-" + idem,
		DisclosureRef: offers[0].Disclosure.DisclosureSnapshotID,
		Channel:       "USSD", SessionID: "sess-" + idem, AcceptedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Admin.QueryRow(context.Background(),
		`SELECT advance_id, fee_minor FROM advances
		 WHERE subscriber_account_id=(SELECT subscriber_account_id FROM subscriber_accounts WHERE msisdn_token=$1)
		 ORDER BY accepted_at DESC LIMIT 1`, dfToken).Scan(&advanceID, &fee); err != nil {
		t.Fatal(err)
	}
	if fee <= 0 {
		t.Fatalf("test needs a positive fee, got %d", fee)
	}
	return
}

// originateDeferred confirms one advance and asserts it genuinely DEFERRED (so
// the settlement assertions below cannot pass vacuously under an accidental
// UPFRONT flip: UPFRONT would recognise the fee at issuance instead).
func originateDeferred(t *testing.T, orig *origination.Service, db *testutil.DB, idem string) (advanceID string, fee int64) {
	t.Helper()
	advanceID, fee = confirmOne(t, orig, db, idem)
	if got := acctBal(t, db, "FEE_INCOME"); got != 0 {
		t.Fatalf("precondition: DEFERRED issuance must recognise 0 fee income, got %d (is the default UPFRONT?)", got)
	}
	if got := acctBal(t, db, "UNEARNED_FEE"); got != fee {
		t.Fatalf("precondition: DEFERRED issuance must book UNEARNED_FEE=%d, got %d", fee, got)
	}
	return
}

func stmtLines(st settlement.Statement) map[string]int64 {
	m := map[string]int64{}
	for _, l := range st.Lines {
		m[l.Code] = l.Amount.Amount()
	}
	return m
}

// assertFeeZero fails if the statement recognises ANY fee income or any line
// derived from it (share partition, taxes).
func assertFeeZero(t *testing.T, st settlement.Statement, where string) {
	t.Helper()
	for _, l := range st.Lines {
		switch {
		case l.Code == "FEE_INCOME_TOTAL", l.Code == "TELCO_SHARE", l.Code == "PLATFORM_SHARE",
			strings.HasPrefix(l.Code, "TAX_"):
			if l.Amount.Amount() != 0 {
				t.Errorf("%s: line %s = %d, want 0 (deferred fee must not be recognised without recovery)",
					where, l.Code, l.Amount.Amount())
			}
		}
	}
}

// (a) Issue + full write-off, no recovery: no fee is EVER recognised — not in
// the issuance period, not across the whole defaulted life. The write-off
// reverses the unearned liability against WRITE_OFF_EXPENSE, never FEE_INCOME.
func TestDeferredSettlement_FullWriteOff_NeverRecognisesFee(t *testing.T) {
	set, orig, _, db := setup(t, "set_df_wo")
	ctx := context.Background()
	periodStart := time.Now().UTC().Add(-1 * time.Hour)

	advID, _ := originateDeferred(t, orig, db, "set-df-wo-1")

	// Issuance period: fee deferred, so the statement recognises nothing.
	issuanceEnd := time.Now().UTC()
	stIssue, err := set.Generate(ctx, dfTelco, dfProgramme, periodStart, issuanceEnd)
	if err != nil {
		t.Fatal(err)
	}
	assertFeeZero(t, stIssue, "issuance period")

	// Default the advance and write it off (maker-checker), no recovery ever.
	appCfg := configsvc.New(db.App)
	col := collections.New(db.App, appCfg, ledger.New(appCfg), slog.Default())
	var minBucket string
	if err := db.Admin.QueryRow(ctx,
		`SELECT content->>'min_bucket' FROM config_versions
		 WHERE domain='writeoff.policy' AND state='ACTIVE'
		 ORDER BY (scope='programme:`+dfProgramme+`') DESC, version_no DESC LIMIT 1`).Scan(&minBucket); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Admin.Exec(ctx,
		`UPDATE advances SET delinquency_bucket=$2 WHERE advance_id=$1`, advID, minBucket); err != nil {
		t.Fatal(err)
	}
	wo, err := col.RequestWriteOff(ctx, dfTelco, advID, "maker", "default")
	if err != nil {
		t.Fatalf("request write-off: %v", err)
	}
	if err := col.ApproveWriteOff(ctx, dfTelco, wo.WriteOffID, "checker", "corr-wo"); err != nil {
		t.Fatalf("approve write-off: %v", err)
	}

	// Whole-life window (issuance + write-off): still zero fee income. This is
	// the headline — a defaulted deferred loan yields no phantom revenue.
	stWhole, err := set.Generate(ctx, dfTelco, dfProgramme, periodStart, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	assertFeeZero(t, stWhole, "whole defaulted life")

	// And the ledger agrees: FEE_INCOME never moved.
	if got := acctBal(t, db, "FEE_INCOME"); got != 0 {
		t.Fatalf("defaulted deferred loan must recognise 0 fee income in the ledger, got %d", got)
	}
	if got := acctBal(t, db, "UNEARNED_FEE"); got != 0 {
		t.Fatalf("write-off must fully reverse UNEARNED_FEE to 0, got %d", got)
	}
}

// (b) A full recovery in a LATER period is where the whole fee is recognised —
// the issuance period recognises nothing.
func TestDeferredSettlement_RecoveryPeriod_RecognisesFee(t *testing.T) {
	set, orig, rec, db := setup(t, "set_df_recog")
	ctx := context.Background()
	periodStart := time.Now().UTC().Add(-1 * time.Hour)

	_, fee := originateDeferred(t, orig, db, "set-df-recog-1")

	// Close the issuance period BEFORE any recovery posts.
	boundary := time.Now().UTC()
	stIssue, err := set.Generate(ctx, dfTelco, dfProgramme, periodStart, boundary)
	if err != nil {
		t.Fatal(err)
	}
	assertFeeZero(t, stIssue, "issuance period (recovery comes later)")

	// Recovery lands in the NEXT period.
	time.Sleep(10 * time.Millisecond) // guarantee posted_at strictly after the boundary
	if _, err := rec.Ingest(tenantCtx(), recovery.IngestCmd{
		SourceEventID: "set-df-recog-src", MSISDNToken: dfToken,
		Amount: entity.MustMoney(5_000, entity.NGN), OccurredAt: time.Now().UTC(),
		CorrelationID: "cor-set-df-recog-2",
	}); err != nil {
		t.Fatal(err)
	}

	stRecog, err := set.Generate(ctx, dfTelco, dfProgramme, boundary, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	got := stmtLines(stRecog)
	// Full fee recognised here; exact share partition and the single rounding site.
	if got["FEE_INCOME_TOTAL"] != fee {
		t.Errorf("recovery period must recognise the full fee=%d, got %d", fee, got["FEE_INCOME_TOTAL"])
	}
	if got["TELCO_SHARE"] != fee*2500/10000 { // 25%
		t.Errorf("TELCO_SHARE = %d, want %d", got["TELCO_SHARE"], fee*2500/10000)
	}
	if got["TELCO_SHARE"]+got["PLATFORM_SHARE"] != got["FEE_INCOME_TOTAL"] {
		t.Errorf("share partition lost money: telco %d + platform %d != fee %d",
			got["TELCO_SHARE"], got["PLATFORM_SHARE"], got["FEE_INCOME_TOTAL"])
	}
	if got["TAX_VAT"] == 0 {
		t.Errorf("VAT must be levied on the recognised fee, got 0")
	}
}

// (c) A partial recovery recognises EXACTLY the waterfall-allocated fee-portion
// (fee-first), and the settlement lines scale proportionally.
func TestDeferredSettlement_PartialRecovery_Proportional(t *testing.T) {
	set, orig, rec, db := setup(t, "set_df_partial")
	ctx := context.Background()
	periodStart := time.Now().UTC().Add(-1 * time.Hour)

	_, fee := originateDeferred(t, orig, db, "set-df-part-1")

	// Recover strictly less than the fee: fee-first allocates it all to fee, so
	// exactly `part` is recognised.
	part := int64(200)
	if part >= fee {
		part = fee / 2
	}
	if _, err := rec.Ingest(tenantCtx(), recovery.IngestCmd{
		SourceEventID: "set-df-part-src", MSISDNToken: dfToken,
		Amount: entity.MustMoney(part, entity.NGN), OccurredAt: time.Now().UTC(),
		CorrelationID: "cor-set-df-part-2",
	}); err != nil {
		t.Fatal(err)
	}

	st, err := set.Generate(ctx, dfTelco, dfProgramme, periodStart, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	got := stmtLines(st)
	if got["FEE_INCOME_TOTAL"] != part {
		t.Errorf("partial recovery must recognise the allocated fee-portion %d, got %d", part, got["FEE_INCOME_TOTAL"])
	}
	if got["TELCO_SHARE"] != part*2500/10000 {
		t.Errorf("TELCO_SHARE must scale with recognised fee: got %d, want %d", got["TELCO_SHARE"], part*2500/10000)
	}
	if got["TELCO_SHARE"]+got["PLATFORM_SHARE"] != got["FEE_INCOME_TOTAL"] {
		t.Errorf("share partition lost money on the partial recognition")
	}
	// The rest is still deferred, not yet recognised.
	if leftover := acctBal(t, db, "UNEARNED_FEE"); leftover != fee-part {
		t.Errorf("UNEARNED_FEE must retain the un-recovered fee %d, got %d", fee-part, leftover)
	}
}

// Safety property (byte-identical UPFRONT): the change only alters DEFERRED. An
// UPFRONT advance recognises its fee at issuance exactly as before, its
// settlement statement reports that fee, and — critically — a FINALISED UPFRONT
// statement still reproduces bit-exactly under the new query. This is what keeps
// any pre-existing FINAL statement valid after the recognition change lands.
func TestDeferredSettlement_Upfront_RecognisesAtIssuance_Reproduces(t *testing.T) {
	set, orig, _, db := setup(t, "set_df_upfront")
	ctx := context.Background()
	periodStart := time.Now().UTC().Add(-1 * time.Hour)

	// Flip the policy to UPFRONT globally through the governed lifecycle
	// (supersedes the seeded DEFERRED default), then originate.
	cfgW := configsvc.New(db.Worker)
	c, err := cfgW.CreateDraft(ctx, "fee_recognition", "global", "alice", "upfront", []byte(`{"policy":"UPFRONT"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfgW.Submit(ctx, c.ConfigVersionID, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := cfgW.Approve(ctx, c.ConfigVersionID, "bob"); err != nil {
		t.Fatal(err)
	}
	if err := cfgW.Activate(ctx, c.ConfigVersionID, "bob", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	_, fee := confirmOne(t, orig, db, "set-df-up-1")

	// UPFRONT recognises the fee at issuance; UNEARNED_FEE is never touched.
	if got := acctBal(t, db, "FEE_INCOME"); got != fee {
		t.Fatalf("UPFRONT issuance must recognise fee=%d immediately, got %d", fee, got)
	}
	if got := acctBal(t, db, "UNEARNED_FEE"); got != 0 {
		t.Fatalf("UPFRONT must never touch UNEARNED_FEE, got %d", got)
	}

	// Settlement over the issuance period recognises the full fee (legacy behaviour).
	st, err := set.Generate(ctx, dfTelco, dfProgramme, periodStart, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if got := stmtLines(st); got["FEE_INCOME_TOTAL"] != fee {
		t.Errorf("UPFRONT settlement must recognise the fee at issuance: got %d, want %d",
			got["FEE_INCOME_TOTAL"], fee)
	}

	// The finalised statement reproduces bit-exactly under the new net-signed
	// aggregation — proving the UPFRONT path is byte-identical.
	if err := set.Finalise(ctx, dfTelco, st.StatementID); err != nil {
		t.Fatal(err)
	}
	if err := set.VerifyReproducible(ctx, dfTelco, st.StatementID); err != nil {
		t.Fatalf("UPFRONT statement must reproduce after the recognition change: %v", err)
	}
}
