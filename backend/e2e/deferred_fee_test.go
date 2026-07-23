package e2e_test

// Deferred fee recognition — falsification tests (money core). These prove the
// COMPLEMENT, not just the happy path: recognise exactly the recovered fee, zero
// on write-off, symmetric reversal, byte-identical UPFRONT, fail-closed config.
// The BC-3 property test already runs the full lifecycle under DEFERRED and holds
// every invariant (incl. INV-018/019) across randomized histories; these add the
// exact-amount point assertions the reviewer required.

import (
	"context"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/ledger"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/collections"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/recovery"
)

// bal returns an account's credit-normal balance (credit - debit): the natural
// balance for FEE_INCOME (income) and UNEARNED_FEE (liability).
func bal(t *testing.T, s *stack, account string) int64 {
	t.Helper()
	var v int64
	if err := s.db.Admin.QueryRow(context.Background(),
		`SELECT COALESCE(SUM(credit_minor - debit_minor),0) FROM journal_entries WHERE account_code=$1`,
		account).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// originate confirms an offer for a normal (instant-fulfilment) token and returns
// the resulting advance's id, fee and outstanding.
func (s *stack) originate(t *testing.T, token string) (advanceID string, fee, outstanding int64) {
	t.Helper()
	offers := s.offersFor(t, token)
	code, body := s.http(t, http.MethodPost, "/v1/advances", "df-"+token,
		confirmBody(offers[0].OfferID, offers[0].DisclosureRef, token, "sess-"+token))
	if code != http.StatusCreated {
		t.Fatalf("confirm %s: %d %s", token, code, body)
	}
	if err := s.db.Admin.QueryRow(context.Background(),
		`SELECT advance_id, fee_minor, outstanding_minor FROM advances
		 WHERE subscriber_account_id = (SELECT subscriber_account_id FROM subscriber_accounts WHERE msisdn_token=$1)
		 ORDER BY accepted_at DESC LIMIT 1`, token).Scan(&advanceID, &fee, &outstanding); err != nil {
		t.Fatal(err)
	}
	if fee <= 0 {
		t.Fatalf("test needs a positive fee, got %d", fee)
	}
	return
}

func (s *stack) recover(t *testing.T, token, src string, amount int64) {
	t.Helper()
	code, body := s.http(t, http.MethodPost, "/v1/recovery/events", "", map[string]any{
		"source_event_id": src, "msisdn_token": token,
		"amount":      map[string]any{"amount_minor": amount, "currency": "NGN"},
		"occurred_at": time.Now().UTC().Format(time.RFC3339),
	})
	if code != http.StatusOK {
		t.Fatalf("recovery %s: %d %s", token, code, body)
	}
}

// Condition 1 + 5: a fully-recovered DEFERRED loan recognises EXACTLY the fee;
// nothing at issuance; ledger balanced and invariants clean throughout.
func TestDeferredFee_FullRecovery_RecognisesExactlyFee(t *testing.T) {
	s := newStack(t, "df_full", 2*time.Second, 300)
	s.seedSubscriber(t, "sub_df1", "tok_df_full")
	_, fee, outstanding := s.originate(t, "tok_df_full")

	// At issuance (DEFERRED default): fee is deferred, not recognised.
	if got := bal(t, s, "FEE_INCOME"); got != 0 {
		t.Fatalf("deferred issuance must recognise 0 fee income, got %d", got)
	}
	if got := bal(t, s, "UNEARNED_FEE"); got != fee {
		t.Fatalf("deferred issuance must book UNEARNED_FEE=%d, got %d", fee, got)
	}

	s.recover(t, "tok_df_full", "df-full-1", outstanding) // full

	if got := bal(t, s, "FEE_INCOME"); got != fee {
		t.Fatalf("full recovery must recognise exactly fee=%d, got %d", fee, got)
	}
	if got := bal(t, s, "UNEARNED_FEE"); got != 0 {
		t.Fatalf("full recovery must drain UNEARNED_FEE to 0, got %d", got)
	}
	s.assertClean(t, 1)
}

// Condition 2: a partial recovery recognises EXACTLY the waterfall-allocated
// fee-portion so far (fee-first seeded waterfall), tied to allocation not time.
func TestDeferredFee_PartialRecovery_RecognisesAllocatedFeePortion(t *testing.T) {
	s := newStack(t, "df_partial", 2*time.Second, 300)
	s.seedSubscriber(t, "sub_df2", "tok_df_part")
	_, fee, _ := s.originate(t, "tok_df_part")

	part := fee - 1 // strictly less than the fee, so fee-first allocates it all to FEE
	if part <= 0 {
		part = fee / 2
	}
	s.recover(t, "tok_df_part", "df-part-1", part)

	if got := bal(t, s, "FEE_INCOME"); got != part {
		t.Fatalf("partial recovery must recognise the allocated fee-portion %d, got %d", part, got)
	}
	if got := bal(t, s, "UNEARNED_FEE"); got != fee-part {
		t.Fatalf("UNEARNED_FEE must be fee-part=%d, got %d", fee-part, got)
	}
	s.assertClean(t, 1)
}

// Condition 3 (headline) + staleness guard: a defaulted + written-off DEFERRED
// loan recognises ZERO fee income beyond what was actually recovered, the
// unearned liability is fully reversed (never negative), and the reversal is
// recomputed FRESH at approval — a recovery landing across the maker-checker
// window does not over-reverse.
func TestDeferredFee_WrittenOff_ZeroPhantomFee_FreshSplit(t *testing.T) {
	s := newStack(t, "df_writeoff", 2*time.Second, 300)
	s.seedSubscriber(t, "sub_df3", "tok_df_wo")
	advID, fee, _ := s.originate(t, "tok_df_wo")
	ctx := context.Background()

	col := collections.New(s.db.App, configsvc.New(s.db.App), ledger.New(configsvc.New(s.db.App)), slog.Default())

	// Make the advance write-off eligible: set its delinquency bucket to the
	// policy's min_bucket (the classifier's column, set directly here).
	var minBucket string
	if err := s.db.Admin.QueryRow(ctx,
		`SELECT content->>'min_bucket' FROM config_versions
		 WHERE domain='writeoff.policy' AND state='ACTIVE'
		 ORDER BY (scope='programme:prg_sim_airtime01') DESC, version_no DESC LIMIT 1`).Scan(&minBucket); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Admin.Exec(ctx,
		`UPDATE advances SET delinquency_bucket=$2 WHERE advance_id=$1`, advID, minBucket); err != nil {
		t.Fatal(err)
	}

	// Maker requests (fee crystallised at request time = full fee, no recovery yet).
	wo, err := col.RequestWriteOff(ctx, "SIM_NG", advID, "maker", "default")
	if err != nil {
		t.Fatalf("request write-off: %v", err)
	}

	// A partial recovery lands ACROSS the maker-checker window: recognises part
	// of the fee, drawing down UNEARNED_FEE.
	part := fee - 1
	if part <= 0 {
		part = fee / 2
	}
	s.recover(t, "tok_df_wo", "df-wo-mid", part)
	if got := bal(t, s, "FEE_INCOME"); got != part {
		t.Fatalf("interim recovery must recognise %d, got %d", part, got)
	}

	// Checker approves: must reverse the FRESH remaining unearned (fee-part), not
	// the stale request-time full fee.
	if err := col.ApproveWriteOff(ctx, "SIM_NG", wo.WriteOffID, "checker", "corr-wo"); err != nil {
		t.Fatalf("approve write-off: %v", err)
	}

	// Zero phantom fee: FEE_INCOME stays at only the recovered part; UNEARNED_FEE
	// fully reversed to 0 (NOT negative); the write-off never credited FEE_INCOME.
	if got := bal(t, s, "FEE_INCOME"); got != part {
		t.Fatalf("write-off must recognise NO extra fee income (only the recovered %d), got %d", part, got)
	}
	if got := bal(t, s, "UNEARNED_FEE"); got != 0 {
		t.Fatalf("write-off must fully reverse UNEARNED_FEE to 0 (stale value would go negative), got %d", got)
	}
	var state string
	if err := s.db.Admin.QueryRow(ctx, `SELECT state FROM advances WHERE advance_id=$1`, advID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "WRITTEN_OFF" {
		t.Fatalf("advance must be WRITTEN_OFF, got %s", state)
	}
	// INV-018 (no negative UNEARNED_FEE) + all others clean.
	violations, err := s.checker.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range violations {
		t.Errorf("INVARIANT VIOLATION after write-off: %s", v)
	}
}

// Condition 4: recovery reversal de-recognises the fee SYMMETRICALLY — a
// fully-reversed fee recovery returns FEE_INCOME to 0 and UNEARNED_FEE to fee.
func TestDeferredFee_Reversal_SymmetricDeRecognition(t *testing.T) {
	s := newStack(t, "df_reversal", 2*time.Second, 300)
	s.seedSubscriber(t, "sub_df4", "tok_df_rev")
	_, fee, _ := s.originate(t, "tok_df_rev")
	ctx := context.Background()

	part := fee - 1
	if part <= 0 {
		part = fee / 2
	}
	s.recover(t, "tok_df_rev", "df-rev-src", part)
	if got := bal(t, s, "FEE_INCOME"); got != part {
		t.Fatalf("pre-reversal FEE_INCOME must be %d, got %d", part, got)
	}

	rec := recovery.New(s.db.App, configsvc.New(s.db.App), ledger.New(configsvc.New(s.db.App)), slog.Default())
	amt, err := entity.NewMoney(part, "NGN")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rec.Reverse(platform.WithTenant(ctx, "SIM_NG"), recovery.ReverseCmd{
		ReversalSourceEventID: "df-rev-rev", OriginalSourceEventID: "df-rev-src",
		Amount: amt, CorrelationID: "corr-df-rev",
	}); err != nil {
		t.Fatalf("reverse: %v", err)
	}

	if got := bal(t, s, "FEE_INCOME"); got != 0 {
		t.Fatalf("full reversal must de-recognise the fee back to 0, got %d", got)
	}
	if got := bal(t, s, "UNEARNED_FEE"); got != fee {
		t.Fatalf("full reversal must restore UNEARNED_FEE to fee=%d, got %d", fee, got)
	}
	s.assertClean(t, -1)
}

// Condition 6: an UPFRONT-pinned advance is byte-identical to legacy — the fee is
// recognised at issuance and UNEARNED_FEE is never touched.
func TestDeferredFee_UpfrontPolicy_RecognisesAtIssuance(t *testing.T) {
	s := newStack(t, "df_upfront", 2*time.Second, 300)
	ctx := context.Background()

	// Activate UPFRONT globally through the governed lifecycle, then originate.
	cfgW := configsvc.New(s.db.Worker)
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

	s.seedSubscriber(t, "sub_df6", "tok_df_up")
	_, fee, _ := s.originate(t, "tok_df_up")

	if got := bal(t, s, "FEE_INCOME"); got != fee {
		t.Fatalf("UPFRONT issuance must recognise fee=%d immediately, got %d", fee, got)
	}
	if got := bal(t, s, "UNEARNED_FEE"); got != 0 {
		t.Fatalf("UPFRONT must never touch UNEARNED_FEE, got %d", got)
	}
	s.assertClean(t, -1)
}

// Condition 7: the fee_recognition read is FAIL-CLOSED at the posting decision —
// with no ACTIVE policy, origination refuses to issue (never a silent default).
func TestDeferredFee_FailClosed_OriginationRefusesWithoutConfig(t *testing.T) {
	s := newStack(t, "df_failclosed", 2*time.Second, 300)
	ctx := context.Background()

	// Remove every ACTIVE fee_recognition version (leaves the domain with none).
	if _, err := s.db.Admin.Exec(ctx,
		`UPDATE config_versions SET state='SUPERSEDED' WHERE domain='fee_recognition' AND state='ACTIVE'`); err != nil {
		t.Fatal(err)
	}

	s.seedSubscriber(t, "sub_df7", "tok_df_fc")
	offers := s.offersFor(t, "tok_df_fc")
	code, _ := s.http(t, http.MethodPost, "/v1/advances", "df-fc",
		confirmBody(offers[0].OfferID, offers[0].DisclosureRef, "tok_df_fc", "sess-fc"))
	if code == http.StatusCreated {
		t.Fatalf("issuance must REFUSE when fee_recognition config is absent (fail-closed), got %d", code)
	}
	// And nothing was posted / no advance left ACTIVE.
	var n int
	if err := s.db.Admin.QueryRow(ctx,
		`SELECT count(*) FROM advances WHERE subscriber_account_id='sub_df7' AND state IN ('ACTIVE','PARTIALLY_RECOVERED')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("fail-closed issuance must leave no live advance, got %d", n)
	}
}
