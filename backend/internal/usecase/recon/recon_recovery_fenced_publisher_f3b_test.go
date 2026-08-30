package recon

// BX-MED-004-A2 reviewer finding F3b — the money-gate publisher must resolve the governed config
// AS PART OF the final publication statement (one snapshot, taken AFTER the arming-row lock), not
// once early and then reuse that stale copy through a later write. F3a already moved config
// resolution onto Postgres's statement_timestamp() (per-statement, not transaction-start). F3b
// closes the residual gap F3a leaves under LOCK CONTENTION: config resolved BEFORE the arming
// FOR UPDATE can go stale while the publisher blocks behind another writer, so a governed threshold
// that tightens during that block would be missed and the money gate wrongly advanced.
//
// Invariant: any governed policy committed BEFORE the final publication statement begins must be
// honoured. The final statement is issued after the lock, so its statement_timestamp() falls after
// any contention has drained — a tightening committed while the publisher was blocked is visible to
// it and refuses the no-longer-confirming run.

import (
	"context"
	"testing"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
)

// TestMED004A2_F3b_PolicyTighteningDuringLockWaitIsHonoured is the deterministic proof. A separate
// connection holds the arming row's FOR UPDATE lock, forcing the publisher to block on that lock
// BEFORE its final publication statement runs. While it is blocked (confirmed via pg_stat_activity,
// never a bare sleep — this machine is slow enough that a fixed wait would race), the governed
// threshold is tightened so the qualifying run no longer confirms. Releasing the lock lets the
// publisher proceed; it MUST refuse.
//
// RED on the pre-F3b code: config is resolved (and confirmation evaluated) BEFORE the arming lock,
// so the publisher holds the loose threshold across the block and advances the gate anyway. GREEN on
// the fixed code: config is resolved and confirmation evaluated INSIDE the final statement, issued
// after the lock releases, so the tightened threshold is seen and the run is refused.
func TestMED004A2_F3b_PolicyTighteningDuringLockWaitIsHonoured(t *testing.T) {
	f := newRecoveryFixture(t, "a2_f3b_lockwait")
	ctx := context.Background()
	armLayer(t, f, "SIM_NG", repo.ReconLayerRecovery)

	dLast, start := med004a2NewestSettledDay(t)
	at := start.Add(12 * time.Hour)

	// Loose threshold at qualification time: exactly 50% confirms a day that is exactly 50%
	// confirmed (matched=500 of platform=1000) — the same construction as the "threshold tightened
	// before publish" case, but here the tightening lands DURING the publish, mid-lock-wait.
	med004a2ActivateRecoveryConfig(t, f, "telco:SIM_NG", 0.50, 172800)

	// tok_matched: booked 500, feed confirms 500 -> MATCHED (contributes to both totals).
	if _, err := f.db.Admin.Exec(ctx, `
		INSERT INTO recovery_events (recovery_event_id, telco_id, source_event_id, msisdn_token, amount_minor, currency, state, occurred_at)
		VALUES ('a2f3b_matched','SIM_NG','wh:a2f3b_matched','tok_a2f3b_matched',500,'NGN','ALLOCATED',$1)`, at); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(ctx, `
		INSERT INTO recovery_eod_feed (telco_id, business_date, msisdn_token, recovery_deducted_minor, currency)
		VALUES ('SIM_NG', $1::date, 'tok_a2f3b_matched', 500, 'NGN')`, dLast); err != nil {
		t.Fatal(err)
	}
	// tok_phantom: booked 500, feed never confirms it -> BREAK_MISSING_TELCO (platform total only).
	if _, err := f.db.Admin.Exec(ctx, `
		INSERT INTO recovery_events (recovery_event_id, telco_id, source_event_id, msisdn_token, amount_minor, currency, state, occurred_at)
		VALUES ('a2f3b_phantom','SIM_NG','wh:a2f3b_phantom','tok_a2f3b_phantom',500,'NGN','ALLOCATED',$1)`, at); err != nil {
		t.Fatal(err)
	}

	res, err := f.svc.RunRecoveryControl(ctx, "SIM_NG")
	if err != nil {
		t.Fatalf("RunRecoveryControl: %v", err)
	}
	if res.QualifiedEvidence == nil {
		t.Fatalf("expected a 50%%-confirmed day to qualify under the loose 50%% threshold, got summaries=%+v", res.Summaries)
	}

	// Hold the arming row's lock on a SEPARATE connection. recon_layer_arming is non-RLS, so this
	// admin FOR UPDATE is a genuine physical row lock the tcp_freshness publisher must wait on.
	blockTx, err := f.db.Admin.Begin(ctx)
	if err != nil {
		t.Fatalf("begin block tx: %v", err)
	}
	blockReleased := false
	defer func() {
		if !blockReleased {
			_ = blockTx.Rollback(context.Background())
		}
	}()
	if _, err := blockTx.Exec(ctx,
		`SELECT 1 FROM recon_layer_arming WHERE telco_id=$1 AND layer=$2 FOR UPDATE`,
		"SIM_NG", repo.ReconLayerRecovery); err != nil {
		t.Fatalf("acquire arming lock: %v", err)
	}

	pub := &FencedControlPublisher{Pool: f.db.Freshness}
	pubErr := make(chan error, 1)
	go func() { pubErr <- pub.Publish(ctx, "SIM_NG", *res.QualifiedEvidence) }()

	// Wait until the publisher is genuinely BLOCKED on the arming-row lock — not merely a fixed
	// sleep. Once a tcp_freshness backend is in a Lock wait, it has passed the point where the
	// pre-F3b code already resolved its (loose) config, so tightening now proves the gap.
	if !waitForFreshnessLockWait(t, f, 20*time.Second) {
		t.Fatal("publisher never reached the arming-row lock wait — cannot prove F3b deterministically")
	}

	// Tighten the governed threshold to 90% while the publisher is blocked. This commits strictly
	// before the publisher's final publication statement can run.
	med004a2ActivateRecoveryConfig(t, f, "telco:SIM_NG", 0.90, 172800)

	// Release the lock — the publisher now proceeds to (on the fixed code) its final publication
	// statement, whose statement_timestamp() falls after the tightening committed.
	if err := blockTx.Rollback(ctx); err != nil {
		t.Fatalf("release arming lock: %v", err)
	}
	blockReleased = true

	select {
	case perr := <-pubErr:
		if perr == nil {
			t.Fatal("Publish must REFUSE: the governed threshold tightened to 0.90 before the final " +
				"publication statement ran, and the half-confirmed run no longer confirms. A config " +
				"resolved once BEFORE the arming lock (BX-MED-004-A2 finding F3b) misses this and wrongly " +
				"advances the money gate.")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Publish did not return after the lock was released — possible deadlock")
	}

	if got := lastReconAt(t, f, "SIM_NG", repo.ReconLayerRecovery); got != nil {
		t.Fatalf("a refused re-verification must never advance the gate, got %v", got)
	}
}

// waitForFreshnessLockWait polls pg_stat_activity (via the superuser admin pool, which can see other
// backends) until at least one tcp_freshness backend is actively waiting on a Lock — i.e. the
// publisher has reached its arming-row FOR UPDATE and is blocked. Deterministic on a slow host,
// where a fixed sleep would race the publisher's own progress. Scoped to THIS fixture's database
// (datname = current_database(), the admin pool's own DB, which every role pool in the fixture shares)
// so a tcp_freshness backend from another concurrently-running test database can never satisfy it.
func waitForFreshnessLockWait(t *testing.T, f *reconFixture, timeout time.Duration) bool {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var n int
		if err := f.db.Admin.QueryRow(ctx, `
			SELECT count(*) FROM pg_stat_activity
			WHERE usename = 'tcp_freshness'
			  AND datname = current_database()
			  AND state = 'active'
			  AND wait_event_type = 'Lock'
			  AND query ILIKE '%recon_layer_arming%'`).Scan(&n); err != nil {
			t.Fatalf("poll pg_stat_activity: %v", err)
		}
		if n >= 1 {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// TestMED004A2_F3b_ConfigResolvesFromGlobalWhenNoTelcoScope proves the in-SQL resolver's telco->global
// fallback: with NO telco:SIM_NG recon.recovery config activated, both RunRecoveryControl and the
// publisher must resolve the GLOBAL-scope config, and the gate must advance with the global window.
func TestMED004A2_F3b_ConfigResolvesFromGlobalWhenNoTelcoScope(t *testing.T) {
	f := newRecoveryFixture(t, "a2_f3b_global")
	ctx := context.Background()
	armLayer(t, f, "SIM_NG", repo.ReconLayerRecovery)

	// A GLOBAL-scope config with a distinctive window (supersedes the fixture's default global). No
	// telco-scoped version exists, so resolution must fall back to this one.
	const globalWindow = 200000
	med004a2ActivateRecoveryConfig(t, f, "global", 0.10, globalWindow)

	res, err := f.svc.RunRecoveryControl(ctx, "SIM_NG")
	if err != nil {
		t.Fatalf("RunRecoveryControl: %v", err)
	}
	if res.QualifiedEvidence == nil {
		t.Fatalf("expected a quiet day to qualify under the loose global threshold, got summaries=%+v", res.Summaries)
	}

	pub := &FencedControlPublisher{Pool: f.db.Freshness}
	if err := pub.Publish(ctx, "SIM_NG", *res.QualifiedEvidence); err != nil {
		t.Fatalf("Publish must succeed resolving the global config: %v", err)
	}

	var win int
	if err := f.db.Admin.QueryRow(ctx,
		`SELECT arm_freshness_max_seconds FROM recon_layer_arming WHERE telco_id='SIM_NG' AND layer=$1`,
		repo.ReconLayerRecovery).Scan(&win); err != nil {
		t.Fatalf("read arm_freshness_max_seconds: %v", err)
	}
	if win != globalWindow {
		t.Fatalf("arm_freshness_max_seconds=%d, want the GLOBAL window %d — with no telco-scope config the "+
			"publisher's in-SQL resolver must fall back to global", win, globalWindow)
	}
}

// TestMED004A2_F3b_ResolverSelectsNewestEffectiveConfig proves the in-SQL resolver's newest-
// effective_from selection within a scope: V1 is activated, then V2 supersedes it (V1.effective_to =
// V2.effective_from). Both are currently-eligible by state, but V2 is the newer-effective one; both
// share a threshold a quiet day meets, with distinct windows so the wrong pick is directly
// observable. NOTE: this does NOT exercise the state-exclusion predicate — in a linear supersession
// V2 also has the newer effective_from, so removing `state IN ('ACTIVE','SUPERSEDED')` would still
// select V2. The dedicated ROLLED_BACK test below is what makes the state predicate load-bearing.
func TestMED004A2_F3b_ResolverSelectsNewestEffectiveConfig(t *testing.T) {
	f := newRecoveryFixture(t, "a2_f3b_rolledback")
	ctx := context.Background()
	armLayer(t, f, "SIM_NG", repo.ReconLayerRecovery)

	const v1Window, v2Window = 180000, 190000
	med004a2ActivateRecoveryConfig(t, f, "telco:SIM_NG", 0.10, v1Window)
	med004a2ActivateRecoveryConfig(t, f, "telco:SIM_NG", 0.10, v2Window)

	res, err := f.svc.RunRecoveryControl(ctx, "SIM_NG")
	if err != nil {
		t.Fatalf("RunRecoveryControl: %v", err)
	}
	if res.QualifiedEvidence == nil {
		t.Fatalf("expected a quiet day to qualify, got summaries=%+v", res.Summaries)
	}

	pub := &FencedControlPublisher{Pool: f.db.Freshness}
	if err := pub.Publish(ctx, "SIM_NG", *res.QualifiedEvidence); err != nil {
		t.Fatalf("Publish must succeed against the currently-effective config V2: %v", err)
	}

	var win int
	if err := f.db.Admin.QueryRow(ctx,
		`SELECT arm_freshness_max_seconds FROM recon_layer_arming WHERE telco_id='SIM_NG' AND layer=$1`,
		repo.ReconLayerRecovery).Scan(&win); err != nil {
		t.Fatalf("read arm_freshness_max_seconds: %v", err)
	}
	if win != v2Window {
		t.Fatalf("arm_freshness_max_seconds=%d, want the CURRENTLY-EFFECTIVE V2 window %d, not the older "+
			"superseded V1 window %d — the resolver must select the newest effective version",
			win, v2Window, v1Window)
	}
}

// TestMED004A2_F3b_ResolverExcludesRolledBackNewerEffectiveConfig makes the `state IN
// ('ACTIVE','SUPERSEDED')` predicate LOAD-BEARING. It constructs a genuinely adversarial governance
// state: a ROLLED_BACK recon.recovery version that carries a STRICTLY NEWER effective_from than the
// legitimate currently-effective one (a version that was activated — thus stamped with a newer
// effective_from — then rolled back). Both are within their effective window right now, so the ONLY
// thing that excludes the rolled-back row is the state predicate; the newest-effective_from ordering
// would otherwise PREFER it. The publisher must ignore the rolled-back row and stamp the legitimate
// version's window.
//
// Mutation proof (per the pre-merge review): deleting `state IN ('ACTIVE','SUPERSEDED')` from the
// resolver makes this test RED — the rolled-back, newer-effective window is wrongly selected.
func TestMED004A2_F3b_ResolverExcludesRolledBackNewerEffectiveConfig(t *testing.T) {
	f := newRecoveryFixture(t, "a2_f3b_rolledback_state")
	ctx := context.Background()
	armLayer(t, f, "SIM_NG", repo.ReconLayerRecovery)

	// V1 legitimate, then V2 activated (so V2 gets the NEWER effective_from and supersedes V1). Same
	// threshold on both (a quiet day confirms under either); distinct windows so a wrong pick shows.
	const legitWindow, rolledBackWindow = 180000, 190000
	v1ID := med004a2ActivateRecoveryConfig(t, f, "telco:SIM_NG", 0.10, legitWindow)
	v2ID := med004a2ActivateRecoveryConfig(t, f, "telco:SIM_NG", 0.10, rolledBackWindow)

	// Roll V2 back: it KEEPS its newer effective_from but leaves the eligible-state set; restore V1 as
	// the legitimate currently-effective version. Order matters — drop the ACTIVE V2 BEFORE
	// re-activating V1, so the ACTIVE-no-overlap exclusion constraint is never momentarily violated.
	if _, err := f.db.Admin.Exec(ctx,
		`UPDATE config_versions SET state='ROLLED_BACK' WHERE config_version_id=$1`, v2ID); err != nil {
		t.Fatalf("roll back V2: %v", err)
	}
	if _, err := f.db.Admin.Exec(ctx,
		`UPDATE config_versions SET state='ACTIVE', effective_to=NULL WHERE config_version_id=$1`, v1ID); err != nil {
		t.Fatalf("restore V1 to ACTIVE: %v", err)
	}

	// Precondition: the construction is genuinely adversarial — V1 ACTIVE, V2 ROLLED_BACK with a
	// STRICTLY NEWER effective_from. Without this, the test could pass vacuously (e.g. if V2 were not
	// actually newer, the state predicate would not be the load-bearing exclusion).
	var v1State, v2State string
	var v1From, v2From time.Time
	if err := f.db.Admin.QueryRow(ctx, `SELECT state, effective_from FROM config_versions WHERE config_version_id=$1`, v1ID).Scan(&v1State, &v1From); err != nil {
		t.Fatalf("read V1: %v", err)
	}
	if err := f.db.Admin.QueryRow(ctx, `SELECT state, effective_from FROM config_versions WHERE config_version_id=$1`, v2ID).Scan(&v2State, &v2From); err != nil {
		t.Fatalf("read V2: %v", err)
	}
	if v1State != "ACTIVE" || v2State != "ROLLED_BACK" || !v2From.After(v1From) {
		t.Fatalf("precondition: want V1 ACTIVE and V2 ROLLED_BACK with a strictly-newer effective_from; "+
			"got V1=%s@%v V2=%s@%v", v1State, v1From, v2State, v2From)
	}

	res, err := f.svc.RunRecoveryControl(ctx, "SIM_NG")
	if err != nil {
		t.Fatalf("RunRecoveryControl: %v", err)
	}
	if res.QualifiedEvidence == nil {
		t.Fatalf("expected a quiet day to qualify under the legitimate config, got summaries=%+v", res.Summaries)
	}

	pub := &FencedControlPublisher{Pool: f.db.Freshness}
	if err := pub.Publish(ctx, "SIM_NG", *res.QualifiedEvidence); err != nil {
		t.Fatalf("Publish must succeed against the legitimate currently-effective config: %v", err)
	}

	var win int
	if err := f.db.Admin.QueryRow(ctx,
		`SELECT arm_freshness_max_seconds FROM recon_layer_arming WHERE telco_id='SIM_NG' AND layer=$1`,
		repo.ReconLayerRecovery).Scan(&win); err != nil {
		t.Fatalf("read arm_freshness_max_seconds: %v", err)
	}
	if win != legitWindow {
		t.Fatalf("arm_freshness_max_seconds=%d, want the LEGITIMATE window %d — the publisher must exclude the "+
			"ROLLED_BACK config (window %d) by state even though it has a NEWER effective_from the ordering "+
			"would otherwise prefer", win, legitWindow, rolledBackWindow)
	}
}

// TestMED004A2_F3b_ReplayUnderLoosenedWindowLeavesBothColumnsUnchanged strengthens the exact-replay
// invariant: equal (not strictly-newer) evidence succeeds but must change NEITHER last_recon_at NOR
// arm_freshness_max_seconds — even when the governed window has been LOOSENED between the first
// publish and the replay. Rewriting the window on stale evidence would widen the arming window and
// could reopen IsLayerLive on evidence that is no fresher than before — the exact class of defect
// the Phase-2 designs caught. The original exact-replay case asserts only last_recon_at; this one
// pins the window column too.
//
// Mutation proof (per the pre-merge review): replacing the conditional window assignment with an
// unconditional current window (arm_freshness_max_seconds = d.window_s) makes this test RED — the
// replay rewrites the window to the looser B.
func TestMED004A2_F3b_ReplayUnderLoosenedWindowLeavesBothColumnsUnchanged(t *testing.T) {
	f := newRecoveryFixture(t, "a2_f3b_replay_window")
	ctx := context.Background()
	armLayer(t, f, "SIM_NG", repo.ReconLayerRecovery)

	const windowA, windowB = 180000, 250000 // B strictly looser (wider) than A
	med004a2ActivateRecoveryConfig(t, f, "telco:SIM_NG", 0.10, windowA)

	res, err := f.svc.RunRecoveryControl(ctx, "SIM_NG")
	if err != nil {
		t.Fatalf("RunRecoveryControl: %v", err)
	}
	if res.QualifiedEvidence == nil {
		t.Fatalf("expected a quiet day to qualify, got summaries=%+v", res.Summaries)
	}

	pub := &FencedControlPublisher{Pool: f.db.Freshness}
	if err := pub.Publish(ctx, "SIM_NG", *res.QualifiedEvidence); err != nil {
		t.Fatalf("first Publish: %v", err)
	}
	firstAt := lastReconAt(t, f, "SIM_NG", repo.ReconLayerRecovery)
	if firstAt == nil {
		t.Fatal("precondition: first Publish must have advanced the gate")
	}
	firstWin := freshnessWindow(t, f)
	if firstWin != windowA {
		t.Fatalf("precondition: first publish must stamp window A %d, got %d", windowA, firstWin)
	}

	// Loosen the governed window to B (a new, currently-effective config version). Threshold
	// unchanged, so the run still confirms — the replay stays a success, not a refusal.
	med004a2ActivateRecoveryConfig(t, f, "telco:SIM_NG", 0.10, windowB)

	// Replay the IDENTICAL evidence: equal (not newer) -> no-op success that changes NEITHER column.
	if err := pub.Publish(ctx, "SIM_NG", *res.QualifiedEvidence); err != nil {
		t.Fatalf("replay of equal evidence must be a no-op success, not an error: %v", err)
	}

	secondAt := lastReconAt(t, f, "SIM_NG", repo.ReconLayerRecovery)
	if secondAt == nil || !secondAt.Equal(*firstAt) {
		t.Fatalf("last_recon_at changed on equal replay: before=%v after=%v", firstAt, secondAt)
	}
	secondWin := freshnessWindow(t, f)
	if secondWin != windowA {
		t.Fatalf("arm_freshness_max_seconds=%d after replay, want the UNCHANGED window A %d — a replay of "+
			"equal (not newer) evidence must NOT rewrite the window to the now-looser current B %d; doing so "+
			"would widen the arming window and could reopen IsLayerLive on stale evidence", secondWin, windowA, windowB)
	}
}

// freshnessWindow reads the RECOVERY layer's current arm_freshness_max_seconds for SIM_NG.
func freshnessWindow(t *testing.T, f *reconFixture) int {
	t.Helper()
	var win int
	if err := f.db.Admin.QueryRow(context.Background(),
		`SELECT arm_freshness_max_seconds FROM recon_layer_arming WHERE telco_id='SIM_NG' AND layer=$1`,
		repo.ReconLayerRecovery).Scan(&win); err != nil {
		t.Fatalf("read arm_freshness_max_seconds: %v", err)
	}
	return win
}
