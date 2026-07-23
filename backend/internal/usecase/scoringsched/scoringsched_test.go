package scoringsched_test

// Phase 0 scoring scheduler — service + claim-ledger integration tests against a
// real database and a live simulator serving the feature file. Covers the
// reviewer's required properties that need a DB: concurrent-claim idempotency,
// restart-safety (lease reclaim / no-reclaim of a done cycle), failure
// isolation, tenant scoping, fail-closed config, and the cold-start arming proof.

import (
	"context"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/scoringsched"
	"github.com/ArowuTest/telco-credit-platform/simulator/sim"
)

const (
	schedTelco     = "SIM_NG"
	schedProgramme = "prg_sim_airtime01"
)

// newSched stands up the scheduler against a fresh DB and a live simulator, with
// the telco.adapter config pointing at the sim. The scoring.schedule is left at
// the seeded GLOBAL default (DISABLED) — tests enable it explicitly.
func newSched(t *testing.T, suffix string) (*scoringsched.Service, *testutil.DB) {
	t.Helper()
	db := testutil.MustSetup(t, suffix)

	simulator := sim.New(slog.Default(), "sched-test", 0)
	srv := httptest.NewServer(simulator.Handler())
	t.Cleanup(srv.Close)
	activateTelcoAdapter(t, db, srv.URL)

	svc := scoringsched.New(db.App, db.Worker, configsvc.New(db.App), slog.Default(), "test-instance-1")
	return svc, db
}

func activateTelcoAdapter(t *testing.T, db *testutil.DB, url string) {
	t.Helper()
	ctx := context.Background()
	cfgW := configsvc.New(db.Worker)
	content := fmt.Sprintf(`{"fulfilment_url":%q,"request_timeout_ms":2000,"retry_budget":0,"circuit_error_threshold_pct":50,"circuit_min_requests":20,"circuit_cooldown_seconds":30,"max_weekly_recharge_minor":100000000}`, url)
	c, err := cfgW.CreateDraft(ctx, "telco.adapter", "telco:"+schedTelco, "alice", "sched sim", []byte(content))
	if err != nil {
		t.Fatal(err)
	}
	mustActivate(t, cfgW, c.ConfigVersionID)
}

// enableSchedule activates a scoring.schedule for the programme with the given
// cadence (headroom 1, lease 900s, max_attempts 6, enabled).
func enableSchedule(t *testing.T, db *testutil.DB, cadenceHours int) {
	t.Helper()
	ctx := context.Background()
	cfgW := configsvc.New(db.Worker)
	content := fmt.Sprintf(`{"enabled":true,"cadence_hours":%d,"headroom_cycles":1,"lease_seconds":900,"max_attempts":6}`, cadenceHours)
	c, err := cfgW.CreateDraft(ctx, "scoring.schedule", "programme:"+schedProgramme, "alice", "arm", []byte(content))
	if err != nil {
		t.Fatal(err)
	}
	mustActivate(t, cfgW, c.ConfigVersionID)
}

func mustActivate(t *testing.T, cfgW *configsvc.Service, id string) {
	t.Helper()
	ctx := context.Background()
	if err := cfgW.Submit(ctx, id, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := cfgW.Approve(ctx, id, "bob"); err != nil {
		t.Fatal(err)
	}
	if err := cfgW.Activate(ctx, id, "bob", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
}

// currentDecisions returns the count and freshest valid_until of the telco's
// current, unexpired scored decisions — the arming proof.
func currentDecisions(t *testing.T, db *testutil.DB) (int, *time.Time) {
	t.Helper()
	var n int
	var vu *time.Time
	if err := db.Admin.QueryRow(context.Background(), `
		SELECT count(*), max(valid_until) FROM decision_snapshots
		WHERE telco_id=$1 AND is_current AND valid_until IS NOT NULL`, schedTelco).Scan(&n, &vu); err != nil {
		t.Fatal(err)
	}
	return n, vu
}

func cycleCount(t *testing.T, db *testutil.DB) int {
	t.Helper()
	var n int
	if err := db.Admin.QueryRow(context.Background(),
		`SELECT count(*) FROM scoring_schedule_cycles WHERE programme_id=$1`, schedProgramme).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Cold start: an armed programme with no decisions on file ends the cycle with
// fresh, unexpired decisions queryable by offers. This is the arming proof.
func TestColdStart_Arms(t *testing.T) {
	svc, db := newSched(t, "sched_cold")
	enableSchedule(t, db, 24)
	now := time.Now().UTC()

	if before, _ := currentDecisions(t, db); before != 0 {
		t.Fatalf("expected no scored decisions before arming, got %d", before)
	}

	out, err := svc.RunDueForProgramme(context.Background(), schedTelco, schedProgramme, now)
	if err != nil {
		t.Fatalf("cold-start run: %v", err)
	}
	if !out.Claimed || out.Status != entity.CycleSucceeded {
		t.Fatalf("cold start must claim and SUCCEED, got claimed=%v status=%q reason=%q", out.Claimed, out.Status, out.SkipReason)
	}
	if out.Scored <= 0 {
		t.Fatalf("cold start must score at least one subject, got %d", out.Scored)
	}
	n, vu := currentDecisions(t, db)
	if n <= 0 {
		t.Fatalf("arming must leave current scored decisions on file, got %d", n)
	}
	if vu == nil || !vu.After(now) {
		t.Fatalf("armed decisions must be unexpired (valid_until in the future), got %v", vu)
	}
}

// Idempotency in one window: a second call in the same effective-cadence bucket
// claims nothing and runs nothing — exactly one cycle, one scoring run.
func TestIdempotent_SameWindowSkips(t *testing.T) {
	svc, db := newSched(t, "sched_idem")
	enableSchedule(t, db, 24)
	ctx := context.Background()
	now := time.Now().UTC()

	first, err := svc.RunDueForProgramme(ctx, schedTelco, schedProgramme, now)
	if err != nil || !first.Claimed {
		t.Fatalf("first run must claim: %+v err=%v", first, err)
	}
	second, err := svc.RunDueForProgramme(ctx, schedTelco, schedProgramme, now)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if second.Claimed || second.SkipReason != "already_ran" {
		t.Fatalf("second run in the same window must skip already_ran, got %+v", second)
	}
	if c := cycleCount(t, db); c != 1 {
		t.Fatalf("exactly one cycle row expected, got %d", c)
	}
	var runs int
	if err := db.Admin.QueryRow(ctx, `SELECT count(*) FROM scoring_runs WHERE programme_id=$1`, schedProgramme).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 1 {
		t.Fatalf("exactly one scoring run expected, got %d", runs)
	}
}

// Concurrent instances on the same cycle: exactly one claims and runs.
func TestConcurrentClaim_ExactlyOne(t *testing.T) {
	svc, db := newSched(t, "sched_conc")
	enableSchedule(t, db, 24)
	now := time.Now().UTC()

	const n = 6
	var wg sync.WaitGroup
	claims := make([]bool, n)
	errs := make([]error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			out, err := svc.RunDueForProgramme(context.Background(), schedTelco, schedProgramme, now)
			claims[i] = out.Claimed
			errs[i] = err
		}(i)
	}
	wg.Wait()

	won := 0
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("goroutine %d errored: %v", i, errs[i])
		}
		if claims[i] {
			won++
		}
	}
	if won != 1 {
		t.Fatalf("exactly one concurrent instance must claim the cycle, got %d", won)
	}
	if c := cycleCount(t, db); c != 1 {
		t.Fatalf("exactly one cycle row expected, got %d", c)
	}
}

// Fail-closed: a disabled schedule (the seeded default) never arms.
func TestDisabled_Skips(t *testing.T) {
	svc, db := newSched(t, "sched_disabled")
	// deliberately do NOT enableSchedule — global default is disabled.
	out, err := svc.RunDueForProgramme(context.Background(), schedTelco, schedProgramme, time.Now().UTC())
	if err != nil {
		t.Fatalf("disabled run: %v", err)
	}
	if out.Claimed || out.SkipReason != "disabled" {
		t.Fatalf("a disabled schedule must skip, got %+v", out)
	}
	if c := cycleCount(t, db); c != 0 {
		t.Fatalf("a disabled programme must claim no cycle, got %d", c)
	}
}

// Fail-closed: no resolvable scoring.schedule config => skip, never a hardcoded
// default. (Supersede every scoring.schedule version, leaving the domain empty.)
func TestConfigError_FailClosed(t *testing.T) {
	svc, db := newSched(t, "sched_cfgerr")
	enableSchedule(t, db, 24)
	// Close the effective window on every scoring.schedule version so none is
	// effective now — GetActiveAt is temporal (state + effective_from/to), so a
	// bare state change would not hide it. This simulates the domain having no
	// resolvable config, which must fail-closed (never a hardcoded default).
	if _, err := db.Admin.Exec(context.Background(),
		`UPDATE config_versions SET state='SUPERSEDED', effective_to=now()
		 WHERE domain='scoring.schedule'`); err != nil {
		t.Fatal(err)
	}
	out, err := svc.RunDueForProgramme(context.Background(), schedTelco, schedProgramme, time.Now().UTC())
	if err != nil {
		t.Fatalf("config-error run must not error, it must skip: %v", err)
	}
	if out.Claimed || out.SkipReason != "config_error" {
		t.Fatalf("missing schedule config must fail-closed skip, got %+v", out)
	}
	if c := cycleCount(t, db); c != 0 {
		t.Fatalf("fail-closed must claim no cycle, got %d", c)
	}
}

// Failure isolation at the cycle level: an ingest failure parks the cycle FAILED
// (re-claimable), and does not leave a phantom SUCCEEDED.
func TestIngestFailure_ParksFailed(t *testing.T) {
	svc, db := newSched(t, "sched_ingfail")
	enableSchedule(t, db, 24)
	// Point the adapter at a dead URL so featureingest fails.
	activateTelcoAdapter(t, db, "http://127.0.0.1:1")

	out, err := svc.RunDueForProgramme(context.Background(), schedTelco, schedProgramme, time.Now().UTC())
	if err == nil {
		t.Fatal("ingest against a dead endpoint must error")
	}
	if !out.Claimed || out.Status != entity.CycleFailed {
		t.Fatalf("a failed ingest must park the claimed cycle FAILED, got %+v", out)
	}
	var status string
	if err := db.Admin.QueryRow(context.Background(),
		`SELECT status FROM scoring_schedule_cycles WHERE cycle_id=$1`, out.CycleID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != entity.CycleFailed {
		t.Fatalf("cycle row must be FAILED, got %s", status)
	}
}

// The arming proof through the FULL scheduler entrypoint (RunDueAll, as the
// worker loop and -score one-shot call it): a cold-start SIM_NG ends with
// offer-ready decisions on file — current, unexpired, and lendable
// (max_face_value_minor > 0), exactly what the offers gate requires.
func TestRunDueAll_ColdStart_ArmsPilot(t *testing.T) {
	svc, db := newSched(t, "sched_rundueall")
	enableSchedule(t, db, 24)

	svc.RunDueAll(context.Background(), time.Now().UTC())

	var offerReady int
	if err := db.Admin.QueryRow(context.Background(), `
		SELECT count(*) FROM decision_snapshots
		WHERE telco_id=$1 AND is_current AND valid_until IS NOT NULL
		  AND valid_until > now() AND max_face_value_minor > 0`, schedTelco).Scan(&offerReady); err != nil {
		t.Fatal(err)
	}
	if offerReady <= 0 {
		t.Fatal("RunDueAll cold start must leave offer-ready decisions on file (current, unexpired, lendable)")
	}
	// The cycle is recorded SUCCEEDED for the programme.
	var status string
	if err := db.Admin.QueryRow(context.Background(),
		`SELECT status FROM scoring_schedule_cycles WHERE programme_id=$1 ORDER BY claimed_at DESC LIMIT 1`,
		schedProgramme).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != entity.CycleSucceeded {
		t.Fatalf("cold-start cycle must be SUCCEEDED, got %s", status)
	}
}

// ----- claim-ledger (repo) tests: the exactly-once + lease-reclaim primitive ---

func claim(t *testing.T, db *testutil.DB, windowSeconds, lease int) (claimed bool, cycleID, file string, attempts int) {
	t.Helper()
	tctx := platform.WithTenant(context.Background(), schedTelco)
	err := repo.WithTenantTx(tctx, db.App, func(tx pgx.Tx) error {
		var e error
		claimed, cycleID, file, attempts, e = (repo.ScoringCycles{}).ClaimOrReclaim(
			context.Background(), tx, schedTelco, schedProgramme, "inst", windowSeconds, 1, lease)
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	return
}

func markCycle(t *testing.T, db *testutil.DB, cycleID, how string) {
	t.Helper()
	tctx := platform.WithTenant(context.Background(), schedTelco)
	if err := repo.WithTenantTx(tctx, db.App, func(tx pgx.Tx) error {
		switch how {
		case "succeeded":
			return (repo.ScoringCycles{}).MarkSucceeded(context.Background(), tx, cycleID, "run_x", 3)
		case "failed":
			return (repo.ScoringCycles{}).MarkFailed(context.Background(), tx, cycleID, "boom")
		case "bind":
			return (repo.ScoringCycles{}).BindFeatureFile(context.Background(), tx, cycleID, "ffl_bound")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestClaimGrid_OnePerWindowThenNext(t *testing.T) {
	_, db := newSched(t, "sched_grid")

	// Same 2s window: first claims, second (fresh CLAIMED) is refused.
	c1, id1, _, a1 := claim(t, db, 2, 900)
	if !c1 || a1 != 1 {
		t.Fatalf("first claim must succeed with attempts=1, got claimed=%v attempts=%d", c1, a1)
	}
	c2, _, _, _ := claim(t, db, 2, 900)
	if c2 {
		t.Fatal("a fresh CLAIMED cycle in the same window must not be re-claimed")
	}
	// Complete it; still refused in-window.
	markCycle(t, db, id1, "succeeded")
	if c3, _, _, _ := claim(t, db, 2, 900); c3 {
		t.Fatal("a SUCCEEDED cycle must never be re-claimed in its window")
	}
	// Next window: a new cycle claims.
	time.Sleep(2100 * time.Millisecond)
	c4, id4, _, a4 := claim(t, db, 2, 900)
	if !c4 || id4 == id1 || a4 != 1 {
		t.Fatalf("the next window must start a fresh cycle, got claimed=%v sameID=%v attempts=%d", c4, id4 == id1, a4)
	}
}

func TestReclaim_StaleLeasePreservesFile(t *testing.T) {
	_, db := newSched(t, "sched_reclaim")
	// Claim with a long window so we stay in one bucket, then bind a file.
	c1, id1, _, _ := claim(t, db, 100000, 900)
	if !c1 {
		t.Fatal("initial claim must succeed")
	}
	markCycle(t, db, id1, "bind") // feature_file_id = ffl_bound

	// Still under lease: not re-claimable.
	if c2, _, _, _ := claim(t, db, 100000, 900); c2 {
		t.Fatal("a CLAIMED cycle under a live lease must not be reclaimed")
	}
	// Expire the lease.
	if _, err := db.Admin.Exec(context.Background(),
		`UPDATE scoring_schedule_cycles SET claimed_at = now() - interval '1000 seconds' WHERE cycle_id=$1`, id1); err != nil {
		t.Fatal(err)
	}
	c3, id3, file, a3 := claim(t, db, 100000, 900)
	if !c3 || id3 != id1 || a3 != 2 {
		t.Fatalf("a stale-lease cycle must be reclaimed in place with attempts=2, got claimed=%v id==%v attempts=%d", c3, id3 == id1, a3)
	}
	if file != "ffl_bound" {
		t.Fatalf("reclaim must preserve the bound feature file so scoringrun replays, got %q", file)
	}
}

func TestReclaim_FailedButNotSucceeded(t *testing.T) {
	_, db := newSched(t, "sched_reclaim_fail")
	c1, id1, _, _ := claim(t, db, 100000, 900)
	if !c1 {
		t.Fatal("initial claim must succeed")
	}
	markCycle(t, db, id1, "failed")
	c2, id2, _, a2 := claim(t, db, 100000, 900)
	if !c2 || id2 != id1 || a2 != 2 {
		t.Fatalf("a FAILED cycle must be immediately reclaimable (attempts=2), got claimed=%v id==%v attempts=%d", c2, id2 == id1, a2)
	}
	markCycle(t, db, id1, "succeeded")
	if c3, _, _, _ := claim(t, db, 100000, 900); c3 {
		t.Fatal("a SUCCEEDED cycle must never be reclaimed")
	}
}
