package ledger_test

// Ledger core tests (V2-LED-001..004, INV-003/004): balance rejection before
// any write, posting idempotency with the DB as arbiter, unknown-account
// fail-closed, append-only grants, and rebuild.

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/ledger"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
)

func issuedJournal(key string, face, disbursed, fee int64) ledger.Journal {
	return ledger.Journal{
		BusinessEventKey: key,
		EventType:        ledger.EventAdvanceIssued,
		TelcoID:          "SIM_NG",
		ProgrammeID:      "prg_sim_airtime01",
		AdvanceID:        "adv_test_1",
		CorrelationID:    "cor_test_1",
		Lines: []ledger.Line{
			{Account: "SUBSCRIBER_RECEIVABLE", Side: ledger.Debit, Amount: entity.MustMoney(face, entity.NGN)},
			{Account: "AIRTIME_FUNDING_CLEARING", Side: ledger.Credit, Amount: entity.MustMoney(disbursed, entity.NGN)},
			{Account: "FEE_INCOME", Side: ledger.Credit, Amount: entity.MustMoney(fee, entity.NGN)},
		},
	}
}

func withTenant(db *testutil.DB) (context.Context, func(fn func(tx pgx.Tx) error) error) {
	ctx := platform.WithTenant(context.Background(), "SIM_NG")
	return ctx, func(fn func(tx pgx.Tx) error) error {
		return repo.WithTenantTx(ctx, db.App, fn)
	}
}

func TestV2_LED_001_UnbalancedJournalRejectedBeforeAnyWrite(t *testing.T) {
	db := testutil.MustSetup(t, "ledger_balance")
	svc := ledger.New(configsvc.New(db.App))
	_, inTx := withTenant(db)

	err := inTx(func(tx pgx.Tx) error {
		// 10000 debit vs 9000+900 credit: off by 100.
		_, _, err := svc.Post(context.Background(), tx, issuedJournal("k1", 10_000, 9_000, 900))
		return err
	})
	if !errors.Is(err, ledger.ErrUnbalanced) {
		t.Fatalf("want ErrUnbalanced, got %v", err)
	}
	var n int
	if err := db.Admin.QueryRow(context.Background(), `SELECT count(*) FROM journals`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("unbalanced journal must write NOTHING, found %d journals", n)
	}
}

func TestINV_003_PostingIsIdempotent_DBArbiter(t *testing.T) {
	db := testutil.MustSetup(t, "ledger_idem")
	svc := ledger.New(configsvc.New(db.App))
	_, inTx := withTenant(db)

	var firstID string
	if err := inTx(func(tx pgx.Tx) error {
		posted, id, err := svc.Post(context.Background(), tx, issuedJournal("adv1/issued", 10_000, 9_000, 1_000))
		if err != nil {
			return err
		}
		if !posted {
			t.Error("first post must post")
		}
		firstID = id
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Same business event again (replay/crash-retry): no new rows, same id.
	if err := inTx(func(tx pgx.Tx) error {
		posted, id, err := svc.Post(context.Background(), tx, issuedJournal("adv1/issued", 10_000, 9_000, 1_000))
		if err != nil {
			return err
		}
		if posted {
			t.Error("duplicate post must not post")
		}
		if id != firstID {
			t.Errorf("duplicate must return original journal id: %s vs %s", id, firstID)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var journals, entries int
	if err := db.Admin.QueryRow(context.Background(),
		`SELECT (SELECT count(*) FROM journals), (SELECT count(*) FROM journal_entries)`).
		Scan(&journals, &entries); err != nil {
		t.Fatal(err)
	}
	if journals != 1 || entries != 3 {
		t.Fatalf("want exactly 1 journal / 3 entries, got %d / %d", journals, entries)
	}
}

func TestM1B_F2_DivergentDuplicateIsLoud_HonestRetryIsQuiet(t *testing.T) {
	db := testutil.MustSetup(t, "ledger_divergent")
	svc := ledger.New(configsvc.New(db.App))
	_, inTx := withTenant(db)

	if err := inTx(func(tx pgx.Tx) error {
		_, _, err := svc.Post(context.Background(), tx, issuedJournal("advX/issued", 10_000, 9_000, 1_000))
		return err
	}); err != nil {
		t.Fatal(err)
	}

	// Honest retry: identical lines in a DIFFERENT construction order — quiet.
	reordered := issuedJournal("advX/issued", 10_000, 9_000, 1_000)
	reordered.Lines[1], reordered.Lines[2] = reordered.Lines[2], reordered.Lines[1]
	if err := inTx(func(tx pgx.Tx) error {
		posted, _, err := svc.Post(context.Background(), tx, reordered)
		if err != nil {
			return err
		}
		if posted {
			t.Error("retry must not re-post")
		}
		return nil
	}); err != nil {
		t.Fatalf("order-independent honest retry must be quiet: %v", err)
	}

	// Amount drift on the same business event: LOUD typed error.
	err := inTx(func(tx pgx.Tx) error {
		_, _, err := svc.Post(context.Background(), tx, issuedJournal("advX/issued", 10_000, 8_999, 1_001))
		return err
	})
	if !errors.Is(err, ledger.ErrDivergentDuplicate) {
		t.Fatalf("drifted duplicate must be ErrDivergentDuplicate, got %v", err)
	}
	// And nothing extra was written.
	var journals int
	if err := db.Admin.QueryRow(context.Background(), `SELECT count(*) FROM journals`).Scan(&journals); err != nil {
		t.Fatal(err)
	}
	if journals != 1 {
		t.Fatalf("divergent duplicate must write nothing: %d journals", journals)
	}
}

func TestLedger_UnknownAccountFailsClosed(t *testing.T) {
	db := testutil.MustSetup(t, "ledger_account")
	svc := ledger.New(configsvc.New(db.App))
	_, inTx := withTenant(db)

	j := issuedJournal("k2", 10_000, 9_000, 1_000)
	j.Lines[2].Account = "CREATIVE_NEW_ACCOUNT" // not in governed chart
	err := inTx(func(tx pgx.Tx) error {
		_, _, err := svc.Post(context.Background(), tx, j)
		return err
	})
	if !errors.Is(err, ledger.ErrUnknownAccount) {
		t.Fatalf("posting to an account outside the governed chart must fail closed, got %v", err)
	}
}

func TestV2_LED_003_AppendOnly_NoUpdateDeleteGrants(t *testing.T) {
	db := testutil.MustSetup(t, "ledger_appendonly")
	svc := ledger.New(configsvc.New(db.App))
	ctx, inTx := withTenant(db)

	if err := inTx(func(tx pgx.Tx) error {
		_, _, err := svc.Post(context.Background(), tx, issuedJournal("k3", 10_000, 9_000, 1_000))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// The app role has NO UPDATE/DELETE on journals or entries: mutation is a
	// permission error at the database, not a convention.
	if err := inTx(func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE journal_entries SET debit_minor = debit_minor + 1`)
		return err
	}); err == nil {
		t.Fatal("UPDATE on journal_entries must be denied by grants (V2-LED-015)")
	}
	if err := inTx(func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM journals`)
		return err
	}); err == nil {
		t.Fatal("DELETE on journals must be denied by grants (V2-LED-015)")
	}
}

func TestV2_LED_008_RebuildAndInvariantSweep(t *testing.T) {
	db := testutil.MustSetup(t, "ledger_rebuild")
	svc := ledger.New(configsvc.New(db.App))
	_, inTx := withTenant(db)

	if err := inTx(func(tx pgx.Tx) error {
		if _, _, err := svc.Post(context.Background(), tx, issuedJournal("a/issued", 10_000, 9_000, 1_000)); err != nil {
			return err
		}
		_, _, err := svc.Post(context.Background(), tx, ledger.Journal{
			BusinessEventKey: "a/recovery1", EventType: ledger.EventRecoveryApplied,
			TelcoID: "SIM_NG", ProgrammeID: "prg_sim_airtime01", AdvanceID: "adv_test_1",
			CorrelationID: "cor_test_2",
			Lines: []ledger.Line{
				{Account: "TELCO_SETTLEMENT_RECEIVABLE", Side: ledger.Debit, Amount: entity.MustMoney(4_000, entity.NGN)},
				{Account: "SUBSCRIBER_RECEIVABLE", Side: ledger.Credit, Amount: entity.MustMoney(4_000, entity.NGN)},
			},
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	recv, err := svc.AccountBalance(context.Background(), db.Worker, "SUBSCRIBER_RECEIVABLE", entity.NGN)
	if err != nil {
		t.Fatal(err)
	}
	if recv.Amount() != 6_000 { // 10000 issued - 4000 recovered
		t.Fatalf("SUBSCRIBER_RECEIVABLE rebuild = %d, want 6000", recv.Amount())
	}
	bad, err := svc.CheckAllJournalsBalanced(context.Background(), db.Worker)
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) != 0 {
		t.Fatalf("INV-004 sweep found unbalanced journals: %v", bad)
	}
}
