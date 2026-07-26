package simseed

// Seeder-B/C adversarial pack. The generator's one job is to emit a CLEAN recovery
// day that reconciles EXACTLY through the REAL RECOVERY engine (all MATCHED) — the
// contract's core promise (build/PHASE1_S3_SEEDER_CONTRACT.md §2). If that ever
// breaks, the integration seam (#70) would inject breaks into a world that already
// looks broken, and a green run would prove nothing. So the central test drives the
// production recon over seeded data and asserts a perfect reconciliation, plus the
// two properties that make the seeder safe to re-run and trust: determinism
// (byte-identical across databases — a wall-clock leak fails it) and idempotency.
//
// These tests import the real recon service, so they are the first end-to-end proof
// that the two parallel tracks (seeder + S3) actually agree on the frozen contract.

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/recon"
)

const (
	seedRecTestSeed = "phase1-seederbc-test"
	seedRecTestDate = "2026-06-15" // a Lagos business day (UTC window [06-14T23:00Z, 06-15T23:00Z))
)

// seedCleanDay seeds a cohort of `n` and one clean recovery day over it, via the
// RLS-enforced app pool (exactly how the cmd runs it). Returns the seed result.
func seedCleanDay(t *testing.T, app *pgxpool.Pool, n int) RecoveryDaySeedResult {
	t.Helper()
	ctx := context.Background()
	if _, err := SeedCohort(ctx, app, CohortPlan{Seed: seedRecTestSeed, Count: n}); err != nil {
		t.Fatalf("SeedCohort: %v", err)
	}
	res, err := SeedRecoveryDay(ctx, app, RecoveryDayPlan{Seed: seedRecTestSeed, BusinessDate: seedRecTestDate, Count: n})
	if err != nil {
		t.Fatalf("SeedRecoveryDay: %v", err)
	}
	return res
}

// TestSeederBC_CleanDayReconcilesAllMatched is the core proof: a seeded clean day
// reconciles with zero breaks and full money confirmation through the real engine.
func TestSeederBC_CleanDayReconcilesAllMatched(t *testing.T) {
	db := testutil.MustSetup(t, "seederbc_clean")
	const n = 8
	res := seedCleanDay(t, db.App, n)

	if res.EventsCreated < n || res.FeedRowsCreated != n {
		t.Fatalf("seed created events=%d feedRows=%d; want events>=%d and feedRows=%d",
			res.EventsCreated, res.FeedRowsCreated, n, n)
	}

	svc := recon.New(db.App, configsvc.New(db.App), slog.Default())
	sum, err := svc.ReconcileRecoveryDay(context.Background(), SyntheticTelco, seedRecTestDate)
	if err != nil {
		t.Fatalf("ReconcileRecoveryDay: %v", err)
	}
	// Perfect reconciliation: one MATCHED key per subscriber, no break of any kind.
	if sum.Matched != n || sum.MissingTelco != 0 || sum.MissingPlatform != 0 || sum.AmountMismatch != 0 {
		t.Fatalf("clean day must reconcile all-matched: matched=%d missingTelco=%d missingPlatform=%d amountMismatch=%d (want matched=%d, rest 0)",
			sum.Matched, sum.MissingTelco, sum.MissingPlatform, sum.AmountMismatch, n)
	}
	// Both sides see exactly n subjects, and the money confirmed == the money booked
	// == the seeded control total (the feed confirms 100% of platform recoveries).
	if sum.PlatformRecords != n || sum.SourceRecordCount != int64(n) {
		t.Fatalf("record counts: platform=%d source=%d (want %d each)", sum.PlatformRecords, sum.SourceRecordCount, n)
	}
	if sum.PlatformControlTotalMinor != res.TotalMinor || sum.MatchedControlTotalMinor != res.TotalMinor {
		t.Fatalf("money confirmation: platformTotal=%d matchedTotal=%d seedTotal=%d (all must be equal)",
			sum.PlatformControlTotalMinor, sum.MatchedControlTotalMinor, res.TotalMinor)
	}
}

// TestSeederBC_PerSubjectSumExercised proves the platform-side per-token SUM path:
// at least one subscriber books >1 event in the day, its feed row equals the sum,
// and it still reconciles to ONE matched key (aggregation, not a false break).
func TestSeederBC_PerSubjectSumExercised(t *testing.T) {
	db := testutil.MustSetup(t, "seederbc_sum")
	const n = 8
	seedCleanDay(t, db.App, n)

	// A multi-event subscriber must exist for this seed (else the SUM path is untested).
	multi := -1
	for i := 0; i < n; i++ {
		if eventsForSubscriber(seedRecTestSeed, i) > 1 {
			multi = i
			break
		}
	}
	if multi < 0 {
		t.Fatalf("test seed %q produced no multi-event subscriber in [0,%d) — SUM path unexercised; adjust the seed", seedRecTestSeed, n)
	}

	token := msisdnToken(seedRecTestSeed, multi)
	var eventCount int
	var eventSum, feedDeducted int64
	ctx := context.Background()
	if err := db.Admin.QueryRow(ctx, `
		SELECT count(*), COALESCE(SUM(amount_minor),0) FROM recovery_events
		WHERE telco_id=$1 AND msisdn_token=$2 AND source_event_id LIKE 'wh:%'`,
		SyntheticTelco, token).Scan(&eventCount, &eventSum); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if err := db.Admin.QueryRow(ctx, `
		SELECT recovery_deducted_minor FROM recovery_eod_feed
		WHERE telco_id=$1 AND business_date=$2::date AND msisdn_token=$3`,
		SyntheticTelco, seedRecTestDate, token).Scan(&feedDeducted); err != nil {
		t.Fatalf("read feed row: %v", err)
	}
	if eventCount < 2 {
		t.Fatalf("subscriber %d expected >1 event, got %d", multi, eventCount)
	}
	if feedDeducted != eventSum {
		t.Fatalf("feed deduction %d != sum of %d events %d — the EOD row must equal the per-subscriber SUM", feedDeducted, eventCount, eventSum)
	}
}

// TestSeederBC_DeterministicAcrossDatabases is the falsification test for
// determinism: the same (seed, date, count) seeded into two independent databases
// must produce byte-identical rows — same ids, amounts, tokens AND occurred_at
// instants. A time.Now() leak anywhere in generation fails this.
func TestSeederBC_DeterministicAcrossDatabases(t *testing.T) {
	dbA := testutil.MustSetup(t, "seederbc_det_a")
	dbB := testutil.MustSetup(t, "seederbc_det_b")
	const n = 6
	resA := seedCleanDay(t, dbA.App, n)
	resB := seedCleanDay(t, dbB.App, n)

	if resA != resB {
		t.Fatalf("seed results differ across databases: %+v vs %+v", resA, resB)
	}
	if got := dumpDay(t, dbA.Admin); got != dumpDay(t, dbB.Admin) {
		t.Fatalf("seeded rows are NOT byte-identical across databases — a non-deterministic input (likely wall-clock) leaked")
	}
}

// TestSeederBC_IdempotentReRun proves a re-run into the SAME database creates NO new
// rows (no divergent duplicate that would trip source_event_id dedup) and leaves the
// day still fully reconciling.
func TestSeederBC_IdempotentReRun(t *testing.T) {
	db := testutil.MustSetup(t, "seederbc_idem")
	const n = 5
	first := seedCleanDay(t, db.App, n)
	if first.EventsCreated == 0 {
		t.Fatal("first run created no events")
	}
	ctx := context.Background()
	second, err := SeedRecoveryDay(ctx, db.App, RecoveryDayPlan{Seed: seedRecTestSeed, BusinessDate: seedRecTestDate, Count: n})
	if err != nil {
		t.Fatalf("second SeedRecoveryDay: %v", err)
	}
	if second.EventsCreated != 0 || second.FeedRowsCreated != 0 {
		t.Fatalf("re-run must be a no-op: created events=%d feedRows=%d (want 0,0)", second.EventsCreated, second.FeedRowsCreated)
	}
	if second.TotalMinor != first.TotalMinor {
		t.Fatalf("re-run control total %d != first %d (must be deterministic)", second.TotalMinor, first.TotalMinor)
	}
	// And the day still reconciles all-matched (no phantom duplicate leaked in).
	svc := recon.New(db.App, configsvc.New(db.App), slog.Default())
	sum, err := svc.ReconcileRecoveryDay(ctx, SyntheticTelco, seedRecTestDate)
	if err != nil {
		t.Fatalf("ReconcileRecoveryDay after re-run: %v", err)
	}
	if sum.Matched != n || sum.MissingTelco != 0 || sum.MissingPlatform != 0 || sum.AmountMismatch != 0 {
		t.Fatalf("after re-run: matched=%d missingTelco=%d missingPlatform=%d amountMismatch=%d (want matched=%d, rest 0)",
			sum.Matched, sum.MissingTelco, sum.MissingPlatform, sum.AmountMismatch, n)
	}
}

// TestSeederBC_OnlySyntheticTelcoRows confirms every seeded row is bound to SIM_NG —
// the RLS tenant tx cannot write another telco's data (defence in depth over the
// VerifySyntheticOnly guard).
func TestSeederBC_OnlySyntheticTelcoRows(t *testing.T) {
	db := testutil.MustSetup(t, "seederbc_rls")
	seedCleanDay(t, db.App, 4)
	ctx := context.Background()
	for _, q := range []string{
		`SELECT count(*) FROM recovery_events WHERE telco_id <> $1`,
		`SELECT count(*) FROM recovery_eod_feed WHERE telco_id <> $1`,
	} {
		var foreign int
		if err := db.Admin.QueryRow(ctx, q, SyntheticTelco).Scan(&foreign); err != nil {
			t.Fatalf("count foreign rows: %v", err)
		}
		if foreign != 0 {
			t.Fatalf("seeder wrote %d row(s) for a non-%s telco (query %q)", foreign, SyntheticTelco, q)
		}
	}
}

// TestSeederBC_RejectsBadInput is the fast input-validation falsification pack.
func TestSeederBC_RejectsBadInput(t *testing.T) {
	ctx := context.Background()
	cases := []RecoveryDayPlan{
		{Seed: seedRecTestSeed, BusinessDate: seedRecTestDate, Count: 0},  // non-positive count
		{Seed: seedRecTestSeed, BusinessDate: seedRecTestDate, Count: -1}, // negative count
		{Seed: "", BusinessDate: seedRecTestDate, Count: 1},               // empty seed
		{Seed: seedRecTestSeed, BusinessDate: "2026/06/15", Count: 1},     // bad date format
		{Seed: seedRecTestSeed, BusinessDate: "not-a-date", Count: 1},     // unparseable date
	}
	for i, p := range cases {
		if _, err := SeedRecoveryDay(ctx, nil, p); err == nil {
			t.Errorf("case %d (%+v): expected an error, got nil", i, p)
		}
	}
}

// dumpDay renders the seeded platform + feed rows as a canonical, order-stable
// string for cross-database byte-identity comparison.
func dumpDay(t *testing.T, admin *pgxpool.Pool) string {
	t.Helper()
	ctx := context.Background()
	var out string
	rows, err := admin.Query(ctx, `
		SELECT recovery_event_id, source_event_id, msisdn_token, amount_minor, occurred_at
		FROM recovery_events WHERE source_event_id LIKE 'wh:%' ORDER BY recovery_event_id`)
	if err != nil {
		t.Fatalf("dump events: %v", err)
	}
	for rows.Next() {
		var id, src, tok string
		var minor int64
		var occ time.Time
		if err := rows.Scan(&id, &src, &tok, &minor, &occ); err != nil {
			rows.Close()
			t.Fatalf("scan event: %v", err)
		}
		out += fmt.Sprintf("E|%s|%s|%s|%d|%s\n", id, src, tok, minor, occ.UTC().Format(time.RFC3339Nano))
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("dump events rows: %v", err)
	}
	frows, err := admin.Query(ctx, `
		SELECT business_date, msisdn_token, recovery_deducted_minor
		FROM recovery_eod_feed ORDER BY msisdn_token`)
	if err != nil {
		t.Fatalf("dump feed: %v", err)
	}
	defer frows.Close()
	for frows.Next() {
		var bd time.Time
		var tok string
		var minor int64
		if err := frows.Scan(&bd, &tok, &minor); err != nil {
			t.Fatalf("scan feed: %v", err)
		}
		out += fmt.Sprintf("F|%s|%s|%d\n", bd.UTC().Format("2006-01-02"), tok, minor)
	}
	if err := frows.Err(); err != nil {
		t.Fatalf("dump feed rows: %v", err)
	}
	return out
}
