package recon

// BX-MED-004-A2 acceptance/mutation cases for FencedControlPublisher.Publish, matching the
// rigor of the MED-004-A1 pack (recon_med004a1_control_test.go). Cases:
//
//   1. happy path            -> freshness advances to evidence.EvidenceAt + the CURRENTLY
//      resolved governed window (never evidence.ArmFreshnessMaxSeconds)
//   2. threshold tightened between qualification and publish -> refused, re-derived from
//      recon_runs' own numbers against the CURRENT config, not the qualification's audit copy
//   3. unknown qualification/run pair -> refused (evidence binding)
//   4. evidence naming a SUPERSEDED run -> refused; the mutation-grade control that the
//      genuinely still-ACTIVE run's own evidence DOES publish
//   5. exact replay of already-published evidence -> no-op success, gate unchanged
//   6. stale evidence (older EvidenceAt than what is already stamped) -> no-op success, no
//      regression
//   7. disarmed mid-publish (no recon_layer_arming row) -> refused
//   8. tcp_freshness's own DB-level grant boundary: scoped config view yes / base table no,
//      unrelated tables no, qualification INSERT no, arming INSERT/DELETE no, arming UPDATE
//      column-scoped to (last_recon_at, arm_freshness_max_seconds) only

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
)

// med004a2NewestSettledDay returns the business_date string and UTC period start of "the
// newest settled Lagos business day" as RunRecoveryControl itself computes it — the same
// pattern recon_med004a1_control_test.go's REJECTED/shortfall cases use, needed whenever a
// test seeds data for a SPECIFIC day rather than relying on a genuinely quiet one.
func med004a2NewestSettledDay(t *testing.T) (dLast string, start time.Time) {
	t.Helper()
	loc, err := time.LoadLocation("Africa/Lagos")
	if err != nil {
		t.Fatal(err)
	}
	d, ok := lastSettledBusinessDay(loc, time.Now().UTC().Add(-time.Hour))
	if !ok {
		t.Fatal("expected a settled business day")
	}
	s, _, err := businessDayWindow(loc, d)
	if err != nil {
		t.Fatal(err)
	}
	return d, s
}

// med004a2ActivateRecoveryConfig drives the REAL configsvc maker-checker lifecycle
// (draft -> submit -> approve -> activate) to make a telco-scoped recon.recovery config
// version the CURRENT one, superseding any prior ACTIVE version at the same scope exactly as
// production governance would. Returns the new config_version_id.
func med004a2ActivateRecoveryConfig(t *testing.T, f *reconFixture, scope string, minConfirmationRatio float64, armFreshnessMaxSeconds int) string {
	t.Helper()
	ctx := context.Background()
	content := map[string]any{
		"amount_tolerance_minor":       0,
		"auto_resolve":                 false,
		"max_amount_minor":             1_000_000_000_000,
		"min_completeness_ratio":       0.5,
		"min_confirmation_ratio":       minConfirmationRatio,
		"recon_lag_seconds":            3600,
		"rereconcile_lookback_seconds": 1_209_600,
		"break_aging_alert_hours":      24,
		"business_timezone":            "Africa/Lagos",
		"arm_freshness_max_seconds":    armFreshnessMaxSeconds,
	}
	raw, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	cfg := configsvc.New(f.db.Admin)
	cv, err := cfg.CreateDraft(ctx, "recon.recovery", scope, "test-maker", "BX-MED-004-A2 test config", json.RawMessage(raw))
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}
	if err := cfg.Submit(ctx, cv.ConfigVersionID, "test-maker"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if err := cfg.Approve(ctx, cv.ConfigVersionID, "test-checker"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if err := cfg.Activate(ctx, cv.ConfigVersionID, "test-checker", time.Now().UTC()); err != nil {
		t.Fatalf("activate: %v", err)
	}
	return cv.ConfigVersionID
}

// Case 1: a genuinely quiet newest settled day, published through the fenced role, must
// advance the gate to EXACTLY evidence.EvidenceAt and the CURRENTLY resolved governed window
// — never a caller-supplied value.
func TestMED004A2_Publish_HappyPathAdvancesFreshnessToEvidenceValues(t *testing.T) {
	f := newRecoveryFixture(t, "a2_pub_happy")
	ctx := context.Background()
	armLayer(t, f, "SIM_NG", repo.ReconLayerRecovery)

	res, err := f.svc.RunRecoveryControl(ctx, "SIM_NG")
	if err != nil {
		t.Fatalf("RunRecoveryControl: %v", err)
	}
	if res.QualifiedEvidence == nil {
		t.Fatalf("expected qualifying evidence (quiet day), got summaries=%+v", res.Summaries)
	}

	pub := &FencedControlPublisher{Pool: f.db.Freshness}
	if err := pub.Publish(ctx, "SIM_NG", *res.QualifiedEvidence); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	got := lastReconAt(t, f, "SIM_NG", repo.ReconLayerRecovery)
	if got == nil || !got.Equal(res.QualifiedEvidence.EvidenceAt) {
		t.Fatalf("last_recon_at = %v, want evidence.EvidenceAt = %v", got, res.QualifiedEvidence.EvidenceAt)
	}
	var freshWin int
	if err := f.db.Admin.QueryRow(ctx,
		`SELECT arm_freshness_max_seconds FROM recon_layer_arming WHERE telco_id='SIM_NG' AND layer=$1`,
		repo.ReconLayerRecovery).Scan(&freshWin); err != nil {
		t.Fatalf("independent SELECT arm_freshness_max_seconds: %v", err)
	}
	const globalDefaultWindow = 172800
	if freshWin != globalDefaultWindow {
		t.Fatalf("arm_freshness_max_seconds = %d, want the CURRENT governed value %d (independently "+
			"re-resolved by the publisher, not evidence.ArmFreshnessMaxSeconds)", freshWin, globalDefaultWindow)
	}
}

// Case 2: the core BX-MED-004-A2 security property. A run that positively confirmed under a
// LOOSE threshold must be REFUSED at publish time if the governed threshold has since
// tightened enough that the run's own recon_runs numbers no longer confirm — re-derived
// independently, never trusting the qualification row's audit-only copy.
func TestMED004A2_Publish_RefusesWhenThresholdTightenedSinceQualification(t *testing.T) {
	f := newRecoveryFixture(t, "a2_pub_tighten")
	ctx := context.Background()
	armLayer(t, f, "SIM_NG", repo.ReconLayerRecovery)

	dLast, start := med004a2NewestSettledDay(t)
	at := start.Add(12 * time.Hour)

	// Loose threshold at qualification time: exactly 50% confirms a day that is exactly 50%
	// confirmed (matched=500 of platform=1000).
	med004a2ActivateRecoveryConfig(t, f, "telco:SIM_NG", 0.50, 172800)

	// tok_matched: booked 500, feed confirms 500 -> MATCHED (contributes to both totals).
	if _, err := f.db.Admin.Exec(ctx, `
		INSERT INTO recovery_events (recovery_event_id, telco_id, source_event_id, msisdn_token, amount_minor, currency, state, occurred_at)
		VALUES ('a2t_matched','SIM_NG','wh:a2t_matched','tok_a2t_matched',500,'NGN','ALLOCATED',$1)`, at); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(ctx, `
		INSERT INTO recovery_eod_feed (telco_id, business_date, msisdn_token, recovery_deducted_minor, currency)
		VALUES ('SIM_NG', $1::date, 'tok_a2t_matched', 500, 'NGN')`, dLast); err != nil {
		t.Fatal(err)
	}
	// tok_phantom: booked 500, feed never confirms it -> BREAK_MISSING_TELCO (contributes to the
	// platform total, NOT the matched total).
	if _, err := f.db.Admin.Exec(ctx, `
		INSERT INTO recovery_events (recovery_event_id, telco_id, source_event_id, msisdn_token, amount_minor, currency, state, occurred_at)
		VALUES ('a2t_phantom','SIM_NG','wh:a2t_phantom','tok_a2t_phantom',500,'NGN','ALLOCATED',$1)`, at); err != nil {
		t.Fatal(err)
	}

	res, err := f.svc.RunRecoveryControl(ctx, "SIM_NG")
	if err != nil {
		t.Fatalf("RunRecoveryControl: %v", err)
	}
	if res.QualifiedEvidence == nil {
		t.Fatalf("expected a 50%%-confirmed day to qualify under the loose 50%% threshold, got summaries=%+v", res.Summaries)
	}

	// Tighten the governed threshold to 90% AFTER qualification, BEFORE publish.
	med004a2ActivateRecoveryConfig(t, f, "telco:SIM_NG", 0.90, 172800)

	pub := &FencedControlPublisher{Pool: f.db.Freshness}
	if err := pub.Publish(ctx, "SIM_NG", *res.QualifiedEvidence); err == nil {
		t.Fatal("Publish must refuse: the run no longer confirms at the CURRENT (tightened) threshold")
	}
	if got := lastReconAt(t, f, "SIM_NG", repo.ReconLayerRecovery); got != nil {
		t.Fatalf("a refused re-verification must never advance the gate, got %v", got)
	}
}

// Case 3: evidence naming a qualification/run pair that does not exist must be refused.
func TestMED004A2_Publish_RejectsUnknownQualification(t *testing.T) {
	f := newRecoveryFixture(t, "a2_pub_unknown")
	ctx := context.Background()
	armLayer(t, f, "SIM_NG", repo.ReconLayerRecovery)

	fake := RecoveryEvidence{
		QualificationID: "qual_does_not_exist",
		RunID:           "run_does_not_exist",
		EvidenceAt:      time.Now().UTC(),
	}
	pub := &FencedControlPublisher{Pool: f.db.Freshness}
	if err := pub.Publish(ctx, "SIM_NG", fake); err == nil {
		t.Fatal("Publish must refuse evidence naming a qualification/run pair that does not exist")
	}
	if got := lastReconAt(t, f, "SIM_NG", repo.ReconLayerRecovery); got != nil {
		t.Fatalf("a refused evidence binding must never advance the gate, got %v", got)
	}
}

// Case 4 + its mutation-grade control: RunRecoveryControl always re-reconciles the newest day,
// so a second control run for the same (still-quiet) day supersedes the first's recon_runs row.
// Evidence naming the now-SUPERSEDED run must be refused; evidence naming the genuinely
// still-ACTIVE run must publish successfully — proving the state check isn't vacuous.
func TestMED004A2_Publish_RejectsSupersededRun(t *testing.T) {
	f := newRecoveryFixture(t, "a2_pub_superseded")
	ctx := context.Background()
	armLayer(t, f, "SIM_NG", repo.ReconLayerRecovery)

	res1, err := f.svc.RunRecoveryControl(ctx, "SIM_NG")
	if err != nil {
		t.Fatalf("first RunRecoveryControl: %v", err)
	}
	if res1.QualifiedEvidence == nil {
		t.Fatalf("expected evidence from the first (quiet-day) run, got summaries=%+v", res1.Summaries)
	}

	res2, err := f.svc.RunRecoveryControl(ctx, "SIM_NG")
	if err != nil {
		t.Fatalf("second RunRecoveryControl: %v", err)
	}
	if res2.QualifiedEvidence == nil {
		t.Fatalf("expected evidence from the second (still-quiet-day) run, got summaries=%+v", res2.Summaries)
	}
	if res2.QualifiedEvidence.RunID == res1.QualifiedEvidence.RunID {
		t.Fatal("the second control run must produce its OWN persisted run, not reuse the first's run_id")
	}
	if st := f.runState(t, res1.QualifiedEvidence.RunID); st != "SUPERSEDED" {
		t.Fatalf("precondition: the first run must now be SUPERSEDED by the second's re-reconcile, got %s", st)
	}

	pub := &FencedControlPublisher{Pool: f.db.Freshness}
	if err := pub.Publish(ctx, "SIM_NG", *res1.QualifiedEvidence); err == nil {
		t.Fatal("Publish must refuse evidence whose run is no longer ACTIVE (superseded by a later reconcile)")
	}
	if got := lastReconAt(t, f, "SIM_NG", repo.ReconLayerRecovery); got != nil {
		t.Fatalf("a refused superseded-run publish must never advance the gate, got %v", got)
	}

	if err := pub.Publish(ctx, "SIM_NG", *res2.QualifiedEvidence); err != nil {
		t.Fatalf("Publish of the genuinely still-ACTIVE run's evidence must succeed: %v", err)
	}
	got := lastReconAt(t, f, "SIM_NG", repo.ReconLayerRecovery)
	if got == nil || !got.Equal(res2.QualifiedEvidence.EvidenceAt) {
		t.Fatalf("last_recon_at = %v, want res2's EvidenceAt = %v", got, res2.QualifiedEvidence.EvidenceAt)
	}
}

// Case 5: an exact replay of already-published evidence (a reclaimed/duplicate worker
// execution) must be a no-op SUCCESS, not an error, and must not disturb the stamped value.
func TestMED004A2_Publish_ExactReplayIsNoOpSuccess(t *testing.T) {
	f := newRecoveryFixture(t, "a2_pub_replay")
	ctx := context.Background()
	armLayer(t, f, "SIM_NG", repo.ReconLayerRecovery)

	res, err := f.svc.RunRecoveryControl(ctx, "SIM_NG")
	if err != nil {
		t.Fatalf("RunRecoveryControl: %v", err)
	}
	if res.QualifiedEvidence == nil {
		t.Fatalf("expected evidence, got summaries=%+v", res.Summaries)
	}
	pub := &FencedControlPublisher{Pool: f.db.Freshness}
	if err := pub.Publish(ctx, "SIM_NG", *res.QualifiedEvidence); err != nil {
		t.Fatalf("first Publish: %v", err)
	}
	first := lastReconAt(t, f, "SIM_NG", repo.ReconLayerRecovery)
	if first == nil {
		t.Fatal("precondition: first Publish must have advanced the gate")
	}

	if err := pub.Publish(ctx, "SIM_NG", *res.QualifiedEvidence); err != nil {
		t.Fatalf("replayed Publish must be a no-op SUCCESS, not an error: %v", err)
	}
	second := lastReconAt(t, f, "SIM_NG", repo.ReconLayerRecovery)
	if second == nil || !second.Equal(*first) {
		t.Fatalf("last_recon_at changed on exact replay: before=%v after=%v", first, second)
	}
}

// Case 6: evidence for the SAME still-ACTIVE run claiming an EARLIER EvidenceAt than what is
// already stamped (a delayed publisher) must be a no-op SUCCESS and must never regress the gate.
func TestMED004A2_Publish_StaleEvidenceIsNoOpSuccess(t *testing.T) {
	f := newRecoveryFixture(t, "a2_pub_stale")
	ctx := context.Background()
	armLayer(t, f, "SIM_NG", repo.ReconLayerRecovery)

	res, err := f.svc.RunRecoveryControl(ctx, "SIM_NG")
	if err != nil {
		t.Fatalf("RunRecoveryControl: %v", err)
	}
	if res.QualifiedEvidence == nil {
		t.Fatalf("expected evidence, got summaries=%+v", res.Summaries)
	}
	pub := &FencedControlPublisher{Pool: f.db.Freshness}
	if err := pub.Publish(ctx, "SIM_NG", *res.QualifiedEvidence); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	current := lastReconAt(t, f, "SIM_NG", repo.ReconLayerRecovery)
	if current == nil {
		t.Fatal("precondition: Publish must have advanced the gate")
	}

	stale := *res.QualifiedEvidence
	stale.EvidenceAt = current.Add(-time.Hour)
	if err := pub.Publish(ctx, "SIM_NG", stale); err != nil {
		t.Fatalf("stale-evidence Publish must be a no-op SUCCESS, not an error: %v", err)
	}
	after := lastReconAt(t, f, "SIM_NG", repo.ReconLayerRecovery)
	if after == nil || !after.Equal(*current) {
		t.Fatalf("last_recon_at regressed: before=%v after=%v (stale evidence claimed %v)", current, after, stale.EvidenceAt)
	}
}

// Case 7: the layer disarmed BETWEEN qualification and publish (SetDown deletes the
// recon_layer_arming row entirely) must be refused, not silently no-op'd — an absent row is a
// genuinely different, more alarming condition than "not yet fresh enough".
func TestMED004A2_Publish_RefusesWhenDisarmedMidPublish(t *testing.T) {
	f := newRecoveryFixture(t, "a2_pub_disarmed")
	ctx := context.Background()
	armLayer(t, f, "SIM_NG", repo.ReconLayerRecovery)

	res, err := f.svc.RunRecoveryControl(ctx, "SIM_NG")
	if err != nil {
		t.Fatalf("RunRecoveryControl: %v", err)
	}
	if res.QualifiedEvidence == nil {
		t.Fatalf("expected evidence, got summaries=%+v", res.Summaries)
	}

	arm := &repo.ReconArming{Pool: f.db.Admin}
	if err := arm.SetDown(ctx, "SIM_NG", repo.ReconLayerRecovery); err != nil {
		t.Fatalf("disarm: %v", err)
	}

	pub := &FencedControlPublisher{Pool: f.db.Freshness}
	if err := pub.Publish(ctx, "SIM_NG", *res.QualifiedEvidence); err == nil {
		t.Fatal("Publish must refuse when the layer was disarmed between qualification and publish")
	}
}

// Case 8a: tcp_freshness can read the scoped recon.recovery view but not the base
// config_versions table (which spans >=10 unrelated governed domains).
func TestMED004A2_TcpFreshnessGrants_CannotReadUnrelatedConfigDomains(t *testing.T) {
	f := newRecoveryFixture(t, "a2_grants_config")
	ctx := context.Background()

	var n int
	if err := f.db.Freshness.QueryRow(ctx, `SELECT count(*) FROM config_versions_recon_recovery`).Scan(&n); err != nil {
		t.Fatalf("tcp_freshness must be able to SELECT the scoped recon.recovery view: %v", err)
	}
	if err := f.db.Freshness.QueryRow(ctx, `SELECT count(*) FROM config_versions`).Scan(&n); err == nil {
		t.Fatal("tcp_freshness must NOT be able to read the base config_versions table directly " +
			"(it spans unrelated domains — treasury.guardrails, ledger.accounts, etc.)")
	}
}

// Case 8b: tcp_freshness has no grant at all on tables outside its narrow, named authority.
func TestMED004A2_TcpFreshnessGrants_CannotTouchUnrelatedTables(t *testing.T) {
	f := newRecoveryFixture(t, "a2_grants_unrelated")
	ctx := context.Background()
	var n int

	if err := f.db.Freshness.QueryRow(ctx, `SELECT count(*) FROM telcos`).Scan(&n); err == nil {
		t.Fatal("tcp_freshness must NOT be able to read telcos — it has no grant on it")
	}
	if err := f.db.Freshness.QueryRow(ctx, `SELECT count(*) FROM advances`).Scan(&n); err == nil {
		t.Fatal("tcp_freshness must NOT be able to read advances — it has no grant on it")
	}
	if err := f.db.Freshness.QueryRow(ctx, `SELECT count(*) FROM funding_pools`).Scan(&n); err == nil {
		t.Fatal("tcp_freshness must NOT be able to read funding_pools — it has no grant on it")
	}
	if err := f.db.Freshness.QueryRow(ctx, `SELECT count(*) FROM recon_completeness_overrides`).Scan(&n); err == nil {
		t.Fatal("tcp_freshness must NOT be able to read recon_completeness_overrides — it has no grant on it")
	}
}

// Case 8c: tcp_freshness cannot INSERT qualifications (that authority stays with tcp_app, fenced
// at the Go level), cannot INSERT/DELETE recon_layer_arming (SetLive/SetDown stay
// tcp_worker/admin authority), and its UPDATE grant on recon_layer_arming is column-scoped to
// exactly (last_recon_at, arm_freshness_max_seconds) — not telco_id or layer.
func TestMED004A2_TcpFreshnessGrants_CannotWriteQualificationsOrInsertArming(t *testing.T) {
	f := newRecoveryFixture(t, "a2_grants_write")
	ctx := context.Background()

	if _, err := f.db.Freshness.Exec(ctx, `
		INSERT INTO recon_recovery_qualifications
		  (qualification_id, run_id, telco_id, layer, min_confirmation_ratio_used, arm_freshness_max_seconds_used, recon_recovery_config_version_id)
		VALUES ('qual_x','run_x','SIM_NG','RECOVERY',0.5,172800,'cfg_x')`); err == nil {
		t.Fatal("tcp_freshness must NOT be able to INSERT into recon_recovery_qualifications")
	}
	if _, err := f.db.Freshness.Exec(ctx, `
		INSERT INTO recon_layer_arming (telco_id, layer) VALUES ('SIM_NG','RECOVERY') ON CONFLICT DO NOTHING`); err == nil {
		t.Fatal("tcp_freshness must NOT be able to INSERT into recon_layer_arming (SetLive is not its authority)")
	}
	if _, err := f.db.Freshness.Exec(ctx,
		`DELETE FROM recon_layer_arming WHERE telco_id='SIM_NG' AND layer='RECOVERY'`); err == nil {
		t.Fatal("tcp_freshness must NOT be able to DELETE from recon_layer_arming (SetDown is not its authority)")
	}

	armLayer(t, f, "SIM_NG", repo.ReconLayerRecovery)
	if _, err := f.db.Freshness.Exec(ctx,
		`UPDATE recon_layer_arming SET telco_id='SIM_NG' WHERE telco_id='SIM_NG' AND layer='RECOVERY'`); err == nil {
		t.Fatal("tcp_freshness's UPDATE grant on recon_layer_arming must be column-scoped to " +
			"(last_recon_at, arm_freshness_max_seconds) only — telco_id must be denied")
	}
	if _, err := f.db.Freshness.Exec(ctx,
		`UPDATE recon_layer_arming SET last_recon_at=now(), arm_freshness_max_seconds=172800 WHERE telco_id='SIM_NG' AND layer='RECOVERY'`); err != nil {
		t.Fatalf("tcp_freshness must be able to UPDATE its granted columns: %v", err)
	}
}
