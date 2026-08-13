package recon

// BX-MED-004-A1 acceptance/mutation cases (design_MED-004_v11_FINAL.md, the
// reviewer's 9-case list). Case numbering below matches that list.
//
//   1. confirmed newest day    -> exact run_id + DB created_at, last_recon_at unchanged
//   2. confirmed quiet day     -> same rule (this IS the scenario cases 1+8 use)
//   3. REJECTED run            -> no qualified evidence, freshness unchanged
//   4. NothingToReconcile      -> no qualified evidence — already proven, ZERO new
//      test code: TestS3B_DayConfirmed (recon_recovery_freshness_test.go) already
//      asserts NothingToReconcile:true -> dayConfirmed==false, and that predicate
//      is the ONLY place RunRecoveryControl decides whether to build QualifiedEvidence.
//      RunRecovery cannot itself reach NothingToReconcile==true (it is set in
//      exactly one place, RunFulfilment's empty-watermark branch — verified by grep
//      — so an INTEGRATION case against RunRecoveryControl would be vacuous).
//   5. confirmation shortfall  -> no qualified evidence
//   6. reconciliation error after earlier days -> no qualifying publication — not
//      independently mutation-tested; structurally guaranteed by RunRecoveryControl
//      returning on the FIRST `err != nil` (every `return RecoveryControlResult{...}, err`
//      above the qualification branch has QualifiedEvidence at its unset zero value,
//      nil), same collapsed mutation locus as case 6 in the earlier v9 critique round
//      ("Cases 1/2/3/5/6 collapse to ~3 independent mutation loci").
//   7. last_recon_at populated beforehand -> byte-for-byte unchanged
//   8. evidence references the ACTUAL persisted run, not a caller-generated ID
//   9. two sequential (reclaimed-execution-shaped) control runs each produce their
//      own evidence; NEITHER independently touches the gate
//
// Item 3 from the v10 critique round (ArmFreshnessMaxSeconds must be the governed
// value, not a hardcoded literal) is also covered here, against RunRecovery — the
// actual write path — since it exercises the SAME AdvanceFreshness call that
// existed before this extraction, just reached through the new wrapper.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
)

// Cases 1 + 2 + 8: an armed telco with a genuinely quiet newest settled day.
// RunRecoveryControl must (a) qualify it, (b) reference the ACTUAL persisted
// recon_runs row — not a caller-generated id or a caller-side clock — and
// (c) leave last_recon_at completely untouched (that authority stays with the
// caller, not the control).
func TestMED004A1_QuietDayProducesEvidenceMatchingThePersistedRun(t *testing.T) {
	f := newRecoveryFixture(t, "a1_quiet_evidence")
	ctx := context.Background()
	armLayer(t, f, "SIM_NG", repo.ReconLayerRecovery)

	if ts := lastReconAt(t, f, "SIM_NG", repo.ReconLayerRecovery); ts != nil {
		t.Fatalf("precondition: a freshly armed layer must have no freshness, got %v", ts)
	}

	res, err := f.svc.RunRecoveryControl(ctx, "SIM_NG")
	if err != nil {
		t.Fatalf("RunRecoveryControl: %v", err)
	}
	if len(res.Summaries) == 0 {
		t.Fatal("RunRecoveryControl reconciled no days — the fixture never reached the code under test")
	}
	if res.QualifiedEvidence == nil {
		t.Fatalf("a genuinely quiet newest settled day must produce QualifiedEvidence, got nil (summaries=%+v)", res.Summaries)
	}
	newest := res.Summaries[len(res.Summaries)-1]
	if res.QualifiedEvidence.RunID != newest.RunID {
		t.Fatalf("QualifiedEvidence.RunID (%s) must be the newest day's own run_id (%s)",
			res.QualifiedEvidence.RunID, newest.RunID)
	}
	if res.QualifiedEvidence.QualificationID == "" {
		t.Fatal("QualifiedEvidence.QualificationID must be set (BX-MED-004-A2)")
	}

	// Independent read-back — NOT the code under test's own query — proving the
	// evidence names a run that actually exists, in the state the money gate must
	// be able to trust.
	var telcoID, layer, state string
	if err := f.db.Admin.QueryRow(ctx,
		`SELECT telco_id, layer, state FROM recon_runs WHERE run_id=$1`,
		res.QualifiedEvidence.RunID).Scan(&telcoID, &layer, &state); err != nil {
		t.Fatalf("independent SELECT for run_id=%s: %v", res.QualifiedEvidence.RunID, err)
	}
	if telcoID != "SIM_NG" || layer != layerRecovery || state != "ACTIVE" {
		t.Fatalf("persisted run mismatch: telco_id=%s layer=%s state=%s (want SIM_NG/%s/ACTIVE)",
			telcoID, layer, state, layerRecovery)
	}

	// BX-MED-004-A2: EvidenceAt is the QUALIFICATION row's own created_at — the moment
	// qualification was PROVEN — not recon_runs.created_at (which a REJECTED run also gets, and
	// which is a genuinely different transaction/statement now). Independently verify both: the
	// qualification row exists, points at this exact run, and its created_at is EvidenceAt.
	var qualRunID string
	var qualCreatedAt time.Time
	if err := f.db.Admin.QueryRow(ctx,
		`SELECT run_id, telco_id, layer, created_at FROM recon_recovery_qualifications WHERE qualification_id=$1`,
		res.QualifiedEvidence.QualificationID).Scan(&qualRunID, &telcoID, &layer, &qualCreatedAt); err != nil {
		t.Fatalf("independent SELECT for qualification_id=%s: %v", res.QualifiedEvidence.QualificationID, err)
	}
	if qualRunID != res.QualifiedEvidence.RunID || telcoID != "SIM_NG" || layer != layerRecovery {
		t.Fatalf("qualification row mismatch: run_id=%s telco_id=%s layer=%s (want %s/SIM_NG/%s)",
			qualRunID, telcoID, layer, res.QualifiedEvidence.RunID, layerRecovery)
	}
	if !qualCreatedAt.Equal(res.QualifiedEvidence.EvidenceAt) {
		t.Fatalf("EvidenceAt (%v) must equal the qualification row's own created_at (%v), not a caller-side clock",
			res.QualifiedEvidence.EvidenceAt, qualCreatedAt)
	}

	// The control itself must never have touched the gate.
	if ts := lastReconAt(t, f, "SIM_NG", repo.ReconLayerRecovery); ts != nil {
		t.Fatalf("BX-MED-004-A1: RunRecoveryControl advanced last_recon_at to %v — the control must "+
			"never touch the freshness gate, only report qualifying evidence", ts)
	}
}

// Case 3: a REJECTED run (the completeness floor refusing an untrustworthy
// snapshot — the exact BX-HIGH-016 shape) must produce no qualified evidence,
// and the gate must stay exactly as untouched as it was before the run.
func TestMED004A1_RejectedRunProducesNoQualifiedEvidence(t *testing.T) {
	f := newRecoveryFixture(t, "a1_rejected")
	ctx := context.Background()
	armLayer(t, f, "SIM_NG", repo.ReconLayerRecovery)

	loc, err := time.LoadLocation("Africa/Lagos")
	if err != nil {
		t.Fatal(err)
	}
	dLast, ok := lastSettledBusinessDay(loc, time.Now().UTC().Add(-time.Hour))
	if !ok {
		t.Fatal("expected a settled business day")
	}
	start, _, err := businessDayWindow(loc, dLast)
	if err != nil {
		t.Fatal(err)
	}
	at := start.Add(12 * time.Hour)

	// A trusted baseline (4 booked, 4 confirmed) established WITHOUT advancing
	// freshness, via the single-day entrypoint — same pattern as BX-HIGH-016's test.
	for _, ev := range []struct {
		id, token string
		minor     int64
	}{{"a1r_a", "tok_a1r_a", 500}, {"a1r_b", "tok_a1r_b", 500}, {"a1r_c", "tok_a1r_c", 500}, {"a1r_d", "tok_a1r_d", 500}} {
		if _, err := f.db.Admin.Exec(ctx, `
			INSERT INTO recovery_events (recovery_event_id, telco_id, source_event_id,
			  msisdn_token, amount_minor, currency, state, occurred_at)
			VALUES ($1,'SIM_NG',$2,$3,$4,'NGN','ALLOCATED',$5)`,
			ev.id, "wh:"+ev.id, ev.token, ev.minor, at); err != nil {
			t.Fatalf("seed recovery event %s: %v", ev.id, err)
		}
		if _, err := f.db.Admin.Exec(ctx, `
			INSERT INTO recovery_eod_feed (telco_id, business_date, msisdn_token, recovery_deducted_minor, currency)
			VALUES ('SIM_NG', $1::date, $2, $3, 'NGN')`, dLast, ev.token, ev.minor); err != nil {
			t.Fatalf("seed feed row %s: %v", ev.token, err)
		}
	}
	base, err := f.svc.ReconcileRecoveryDay(ctx, "SIM_NG", dLast)
	if err != nil {
		t.Fatalf("baseline ReconcileRecoveryDay: %v", err)
	}
	if base.Rejected || base.Matched != 4 {
		t.Fatalf("baseline must be a clean 4-match ACTIVE run, got %+v", base)
	}

	// Collapse both sides to zero against a count-4 baseline: below the
	// completeness floor -> REJECTED.
	if _, err := f.db.Admin.Exec(ctx, `DELETE FROM recovery_events WHERE telco_id='SIM_NG'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(ctx, `DELETE FROM recovery_eod_feed WHERE telco_id='SIM_NG'`); err != nil {
		t.Fatal(err)
	}

	res, err := f.svc.RunRecoveryControl(ctx, "SIM_NG")
	if err != nil {
		t.Fatalf("RunRecoveryControl: %v", err)
	}
	if len(res.Summaries) == 0 {
		t.Fatal("RunRecoveryControl reconciled no days")
	}
	newest := res.Summaries[len(res.Summaries)-1]
	if !newest.Rejected {
		t.Fatalf("a zero re-delivery under a count-4 baseline must be REJECTED by the completeness floor, got %+v", newest)
	}
	if st := f.runState(t, newest.RunID); st != "REJECTED" {
		t.Fatalf("the rejected run must be persisted REJECTED, got %s", st)
	}
	if st := f.runState(t, base.RunID); st != "ACTIVE" {
		t.Fatalf("the prior good run must remain ACTIVE, got %s", st)
	}
	if res.QualifiedEvidence != nil {
		t.Fatalf("a REJECTED run must produce NO qualified evidence, got %+v", res.QualifiedEvidence)
	}
	if ts := lastReconAt(t, f, "SIM_NG", repo.ReconLayerRecovery); ts != nil {
		t.Fatalf("a REJECTED run must never advance freshness, got %v", ts)
	}
}

// Case 5: a booked recovery on the newest settled day with an empty feed —
// below the money-confirmation floor — must produce no qualified evidence.
func TestMED004A1_ConfirmationShortfallProducesNoQualifiedEvidence(t *testing.T) {
	f := newRecoveryFixture(t, "a1_shortfall")
	ctx := context.Background()
	armLayer(t, f, "SIM_NG", repo.ReconLayerRecovery)

	loc, _ := time.LoadLocation("Africa/Lagos")
	dLast, ok := lastSettledBusinessDay(loc, time.Now().UTC().Add(-time.Hour))
	if !ok {
		t.Fatal("expected a settled day")
	}
	start, _, err := businessDayWindow(loc, dLast)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(ctx, `
		INSERT INTO recovery_events (recovery_event_id, telco_id, source_event_id,
		  msisdn_token, amount_minor, currency, state, occurred_at)
		VALUES ('a1_sf_ev','SIM_NG','wh:a1_sf_ev','tok_a1_sf',500,'NGN','ALLOCATED',$1)`,
		start.Add(12*time.Hour)); err != nil {
		t.Fatal(err)
	}

	res, err := f.svc.RunRecoveryControl(ctx, "SIM_NG")
	if err != nil {
		t.Fatalf("RunRecoveryControl: %v", err)
	}
	if res.QualifiedEvidence != nil {
		t.Fatalf("a booked-but-unconfirmed newest day must produce NO qualified evidence, got %+v", res.QualifiedEvidence)
	}
	if ts := lastReconAt(t, f, "SIM_NG", repo.ReconLayerRecovery); ts != nil {
		t.Fatalf("an unconfirmed newest day must not advance freshness, got %v", ts)
	}
}

// Case 7: RunRecoveryControl must leave a PRE-EXISTING last_recon_at
// byte-for-byte unchanged, even when the newest day genuinely qualifies — the
// strongest form of the invariant (proving "unchanged" against a real prior
// value, not merely "stays nil").
func TestMED004A1_PriorFreshnessUnchangedByteForByte(t *testing.T) {
	f := newRecoveryFixture(t, "a1_priorfresh")
	ctx := context.Background()
	armLayer(t, f, "SIM_NG", repo.ReconLayerRecovery)

	if _, err := f.svc.Arming.AdvanceFreshness(ctx, "SIM_NG", repo.ReconLayerRecovery, 172800); err != nil {
		t.Fatalf("seed prior freshness: %v", err)
	}
	before := lastReconAt(t, f, "SIM_NG", repo.ReconLayerRecovery)
	if before == nil {
		t.Fatal("precondition: prior freshness must be set")
	}

	res, err := f.svc.RunRecoveryControl(ctx, "SIM_NG")
	if err != nil {
		t.Fatalf("RunRecoveryControl: %v", err)
	}
	if res.QualifiedEvidence == nil {
		t.Fatalf("precondition: this scenario (quiet day) must qualify, got no evidence (summaries=%+v)", res.Summaries)
	}

	after := lastReconAt(t, f, "SIM_NG", repo.ReconLayerRecovery)
	if after == nil || !after.Equal(*before) {
		t.Fatalf("BX-MED-004-A1: last_recon_at changed from %v to %v — RunRecoveryControl must never "+
			"touch the gate even when it produces qualifying evidence", before, after)
	}
}

// Case 9: two sequential control runs (the shape a reclaimed/duplicate
// execution takes) — neither independently touches the gate. Each may
// legitimately produce its own evidence (a fresh recon_runs row each time,
// since re-reconciling the newest day is by design — see RunRecoveryControl's
// doc comment), but publication authority is not exercised by the control at
// all, so two runs cannot double-publish through this function.
func TestMED004A1_SequentialReclaimedExecutionsNeitherTouchGate(t *testing.T) {
	f := newRecoveryFixture(t, "a1_reclaimed")
	ctx := context.Background()
	armLayer(t, f, "SIM_NG", repo.ReconLayerRecovery)

	res1, err := f.svc.RunRecoveryControl(ctx, "SIM_NG")
	if err != nil {
		t.Fatalf("first RunRecoveryControl: %v", err)
	}
	if res1.QualifiedEvidence == nil {
		t.Fatalf("first run: expected qualifying evidence (quiet day), got summaries=%+v", res1.Summaries)
	}
	if ts := lastReconAt(t, f, "SIM_NG", repo.ReconLayerRecovery); ts != nil {
		t.Fatalf("first run must not touch the gate, got %v", ts)
	}

	res2, err := f.svc.RunRecoveryControl(ctx, "SIM_NG")
	if err != nil {
		t.Fatalf("second (reclaimed-shaped) RunRecoveryControl: %v", err)
	}
	if res2.QualifiedEvidence == nil {
		t.Fatalf("second run: expected qualifying evidence (still a quiet day), got summaries=%+v", res2.Summaries)
	}
	if res2.QualifiedEvidence.RunID == res1.QualifiedEvidence.RunID {
		t.Fatal("the second control run must produce its OWN persisted run, not reuse the first's run_id")
	}
	if ts := lastReconAt(t, f, "SIM_NG", repo.ReconLayerRecovery); ts != nil {
		t.Fatalf("BX-MED-004-A1: a second, reclaimed-shaped control run touched the gate (%v) — neither "+
			"execution may independently advance freshness through this function", ts)
	}
}

// v10 critique item 3: ArmFreshnessMaxSeconds must be the value that actually
// governed the confirmation (read from the SAME config load RunRecoveryControl
// used), not a hardcoded literal anywhere downstream. Seeds a DISTINGUISHABLE
// governed value (259200s / 72h — not the 172800s/48h global default, so a
// copy-paste of the default cannot accidentally pass) and drives the real
// write path (RunRecovery, the wrapper) end to end.
func TestMED004A1_ArmFreshnessMaxSecondsPublishedIsGovernedNotHardcoded(t *testing.T) {
	f := newRecoveryFixture(t, "a1_freshwindow")
	ctx := context.Background()
	armLayer(t, f, "SIM_NG", repo.ReconLayerRecovery)

	const distinguishableWindow = 259200 // 72h; global default is 172800 (48h)
	content := map[string]any{
		"amount_tolerance_minor":       0,
		"auto_resolve":                 false,
		"max_amount_minor":             1_000_000_000_000,
		"min_completeness_ratio":       0.5,
		"min_confirmation_ratio":       0.99,
		"recon_lag_seconds":            3600,
		"rereconcile_lookback_seconds": 1_209_600,
		"break_aging_alert_hours":      24,
		"business_timezone":            "Africa/Lagos",
		"arm_freshness_max_seconds":    distinguishableWindow,
	}
	raw, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(ctx, `
		WITH t AS (SELECT $1::text AS c)
		INSERT INTO config_versions (config_version_id, domain, scope, version_no, state, content, content_hash,
		  effective_from, created_by, approved_by, reason)
		SELECT 'cfg_a1_freshwindow_v1', 'recon.recovery', 'telco:SIM_NG', 1, 'ACTIVE',
		  t.c::jsonb, encode(sha256(t.c::bytea), 'hex'), now(), 'seed:builder', 'seed:reviewer',
		  'BX-MED-004-A1 test: telco-scoped override with a distinguishable arm_freshness_max_seconds'
		FROM t`,
		string(raw)); err != nil {
		t.Fatalf("seed telco-scoped recon.recovery override: %v", err)
	}

	if _, err := f.svc.RunRecovery(ctx, "SIM_NG"); err != nil {
		t.Fatalf("RunRecovery: %v", err)
	}
	var got int
	if err := f.db.Admin.QueryRow(ctx,
		`SELECT arm_freshness_max_seconds FROM recon_layer_arming WHERE telco_id='SIM_NG' AND layer=$1`,
		repo.ReconLayerRecovery).Scan(&got); err != nil {
		t.Fatalf("independent SELECT arm_freshness_max_seconds: %v", err)
	}
	if got != distinguishableWindow {
		t.Fatalf("arm_freshness_max_seconds = %d, want the governed value %d — the publisher must carry "+
			"the value RunRecoveryControl actually used, not a hardcoded literal", got, distinguishableWindow)
	}
}
