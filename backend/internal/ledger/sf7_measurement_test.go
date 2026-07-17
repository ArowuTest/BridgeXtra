package ledger_test

// SF-7 measurement (REVIEW_GATES): does a DEFERRABLE constraint trigger
// asserting per-journal balance at COMMIT cost <10% posting throughput?
// This test measures BOTH configurations on the same database and logs the
// ratio — the adopt/decline decision is recorded from these numbers, not
// assumed. Run verbosely: go test -run TestSF7 -v ./backend/internal/ledger/
//
// The trigger is the STRUCTURAL backstop: the app-layer balance assertion in
// ledger.Post stays either way; the trigger catches any writer that bypasses
// the package (e.g. a future bug, a manual migration, an operator script).

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/ledger"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
)

func tenantCtxFor(telcoID string) context.Context {
	return platform.WithTenant(context.Background(), telcoID)
}

const sf7TriggerSQL = `
CREATE OR REPLACE FUNCTION assert_journal_balanced() RETURNS trigger AS $$
DECLARE bad int;
BEGIN
  SELECT count(*) INTO bad FROM (
    SELECT 1 FROM journal_entries
    WHERE journal_id = NEW.journal_id
    GROUP BY currency
    HAVING SUM(debit_minor) <> SUM(credit_minor)) x;
  IF bad > 0 THEN
    RAISE EXCEPTION 'unbalanced journal % (SF-7 structural backstop)', NEW.journal_id;
  END IF;
  RETURN NULL;
END $$ LANGUAGE plpgsql;

CREATE CONSTRAINT TRIGGER journal_balanced_tg
  AFTER INSERT ON journal_entries
  DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW EXECUTE FUNCTION assert_journal_balanced();`

func postBatch(t *testing.T, db *testutil.DB, svc *ledger.Service, prefix string, n int) time.Duration {
	t.Helper()
	start := time.Now()
	for i := 0; i < n; i++ {
		if err := repo.WithTenantTx(tenantCtxFor("SIM_NG"), db.App, func(tx pgx.Tx) error {
			_, _, err := svc.Post(context.Background(), tx,
				issuedJournal(fmt.Sprintf("%s-%04d", prefix, i), 10_000, 9_000, 1_000))
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	return time.Since(start)
}

func TestSF7_DeferredTriggerCostMeasurement(t *testing.T) {
	db := testutil.MustSetup(t, "ledger_sf7")
	svc := ledger.New(configsvc.New(db.App))
	ctx := context.Background()
	const batch = 300

	// Warm-up (connection pools, plan caches) excluded from measurement.
	_ = postBatch(t, db, svc, "warm", 30)

	without := postBatch(t, db, svc, "base", batch)

	if _, err := db.Admin.Exec(ctx, sf7TriggerSQL); err != nil {
		t.Fatal(err)
	}
	with := postBatch(t, db, svc, "trig", batch)

	// Integer permille — this package is inside the BC-1 float-ban perimeter,
	// and the measurement doesn't need floating point either.
	permille := (with - without).Milliseconds() * 1000 / without.Milliseconds()
	sign := "+"
	if permille < 0 {
		sign, permille = "-", -permille
	}
	t.Logf("SF-7 measurement: %d journals — without trigger: %v; with trigger: %v; overhead: %s%d.%d%%",
		batch, without, with, sign, permille/10, permille%10)

	// The trigger must actually FIRE: a raw unbalanced insert (bypassing the
	// ledger package entirely, as admin) must fail at commit.
	err := func() error {
		tx, err := db.Admin.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `
			INSERT INTO journals (journal_id, business_event_key, event_type, telco_id, programme_id, correlation_id, lines_hash)
			VALUES ('jrn_sf7_bad','sf7-bad','ADVANCE_ISSUED','SIM_NG','prg_sim_airtime01','cor-sf7','x')`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO journal_entries (entry_id, journal_id, account_code, debit_minor, credit_minor, currency)
			VALUES ('je_sf7_bad','jrn_sf7_bad','SUBSCRIBER_RECEIVABLE',999,0,'NGN')`); err != nil {
			return err
		}
		return tx.Commit(ctx) // deferred trigger fires HERE
	}()
	if err == nil {
		t.Fatal("SF-7 trigger must reject an unbalanced journal at COMMIT even from a raw admin insert")
	}
	t.Logf("SF-7 structural backstop proven: raw unbalanced insert rejected at commit: %v", err)
}
