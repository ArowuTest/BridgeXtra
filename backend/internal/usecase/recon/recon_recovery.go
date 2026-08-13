package recon

// Phase 1 S3-A — the RECOVERY reconciliation layer. It registers a layerSpec into
// the existing generic engine (reconcileLayer), so it inherits the run header,
// manifest/control-total, completeness floor, supersession and evidence machinery
// unchanged. Only the platform-side fetch and the telco-side source are new.
//
// Design: build/PHASE1_S3_DESIGN.md. ONE deliberate deviation from that doc, for
// the reviewer's NET-SQL source-verify:
//
//   The match key is msisdn_token, NOT a resolved subscriber_account_id. The EOD
//   feed is natively per-MSISDN (the frozen contract), and recovery_events now
//   carry msisdn_token (mig 0053), so the token is the stable identity BOTH sides
//   hold. Keying on it subsumes the designed symmetric point-in-time resolver more
//   directly — an intra-day port (token T deducted under SA1 then SA2) sums under
//   T on both sides -> one MATCHED key; an unmatched-at-ingest event (NULL
//   subscriber) still carries its token -> matches — without the resolver's
//   subscriber_accounts join or its unresolvable-key edge cases. The reversal-aware
//   NET figure and all other design decisions are unchanged.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/recoveryfeed"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
)

const (
	layerRecovery = "RECOVERY"
	// recoveryProgrammeSentinel is the telco-wide "programme" for RECOVERY runs:
	// the EOD feed is not programme-attributed, so RECOVERY reconciles per telco.
	// recon_runs.programme_id has no FK, so a sentinel is legal there.
	recoveryProgrammeSentinel = "__RECOVERY__"
)

// recoveryCfg is the governed recon.recovery config, floors re-asserted at load.
type recoveryCfg struct {
	toleranceCfg
	MinConfirmationRatio   float64 // S3-B: money-confirmation floor for freshness
	ArmFreshnessMaxSeconds int     // S3-B
	BusinessTimezone       string
	loc                    *time.Location
	// ConfigVersionID is the exact recon.recovery config_versions row this cfg was loaded from —
	// BX-MED-004-A2: recorded on the qualification for audit (never trusted by the fenced
	// publisher's own decision, which independently re-resolves the CURRENT governed values).
	ConfigVersionID string
}

// recoverySpec is the RECOVERY layer: the platform side is the reversal-aware NET
// of the telco's booked webhook recoveries, aggregated per msisdn_token for one
// Lagos business day. Reused telcoTransaction shape (no engine fork).
func recoverySpec() layerSpec {
	return layerSpec{
		name: layerRecovery,
		fetchPlatform: func(ctx context.Context, tx pgx.Tx, _ string, periodStart, periodEnd time.Time) (map[string]platformRecord, error) {
			plat := map[string]platformRecord{}
			// Platform figure = Σ(amount_minor + negative reversal allocations) per
			// token. Reversal-aware NET (Conflict-A / money-loss BLOCKER fix): a
			// same-day reversal writes negative recovery_allocations rows (never a
			// wh: event), so a gross SUM would let a reversal MASK a dropped recovery
			// into a false MATCH. NET is safe against BOTH a gross and a net feed —
			// it matches a net feed and over-BREAKS a gross one, never a false MATCH.
			// amount_minor>0 and a reversal can never claw back more than was applied,
			// so net_minor is always >= 0 (matches the feed's >=0 deduction).
			// RLS scopes recovery_events to the telco (tenant tx), like fulfilmentSpec.
			rows, err := tx.Query(ctx, `
				WITH ev AS (
				  SELECT re.recovery_event_id,
				         COALESCE(NULLIF(re.msisdn_token,''), '__notoken__|'||re.recovery_event_id) AS subject_key,
				         re.amount_minor, re.currency
				  FROM recovery_events re
				  WHERE re.source_event_id LIKE 'wh:%'
				    AND re.occurred_at >= $1 AND re.occurred_at < $2
				),
				rev AS (
				  SELECT ra.recovery_event_id, COALESCE(SUM(ra.amount_minor),0) AS neg_minor
				  FROM recovery_allocations ra JOIN ev ON ev.recovery_event_id = ra.recovery_event_id
				  WHERE ra.amount_minor < 0
				  GROUP BY ra.recovery_event_id
				)
				SELECT ev.subject_key,
				       SUM(ev.amount_minor + COALESCE(rev.neg_minor,0))::bigint AS net_minor,
				       min(ev.currency)            AS currency,
				       count(DISTINCT ev.currency) AS currency_variants
				FROM ev LEFT JOIN rev ON rev.recovery_event_id = ev.recovery_event_id
				GROUP BY ev.subject_key`, periodStart, periodEnd)
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			for rows.Next() {
				var subjectKey, currency string
				var net int64
				var variants int
				if err := rows.Scan(&subjectKey, &net, &currency, &variants); err != nil {
					return nil, err
				}
				if variants > 1 {
					// Mixed currencies for one subject (corruption — webhook rejects
					// non-NGN at ingest). Never silently min()-collapse; a non-ISO
					// marker forces a currency break, surfaced for ops.
					currency = "MIX"
				}
				plat[subjectKey] = platformRecord{AdvanceID: subjectKey, FaceValueMinor: net, Currency: currency}
			}
			return plat, rows.Err()
		},
	}
}

// businessDayWindow returns the [start,end) UTC instants of one civil business day
// in loc. periodEnd is the NEXT CIVIL MIDNIGHT in loc — never a +24h add on a UTC
// instant — so no business day is split (DST-safe; loc is validated fixed-offset).
func businessDayWindow(loc *time.Location, businessDate string) (start, end time.Time, err error) {
	d, err := time.ParseInLocation("2006-01-02", businessDate, loc)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("business_date must be YYYY-MM-DD: %w", err)
	}
	y, m, day := d.Date()
	start = time.Date(y, m, day, 0, 0, 0, 0, loc).UTC()
	end = time.Date(y, m, day+1, 0, 0, 0, 0, loc).UTC()
	return start, end, nil
}

// ReconcileRecoveryDay reconciles ONE explicit Lagos business day (the single-day
// entrypoint used by ops/tests). It does NOT advance arming freshness — that is the
// positive-confirmation gate, applied by RunRecovery to the newest settled day.
func (s *Service) ReconcileRecoveryDay(ctx context.Context, telcoID, businessDate string) (Summary, error) {
	cfg, adapter, err := s.loadForRecovery(ctx, telcoID)
	if err != nil {
		return Summary{RunID: platform.NewID("run")}, err
	}
	return s.reconcileRecoveryDayWith(ctx, cfg, adapter, telcoID, businessDate)
}

// reconcileRecoveryDayWith is the core: authenticate the EOD feed via the adapter
// (a bad envelope MAC / manifest mismatch fails the whole day), map each per-token
// deduction into the canonical telcoTransaction, and run the shared engine.
func (s *Service) reconcileRecoveryDayWith(ctx context.Context, cfg recoveryCfg, adapter recoveryfeed.Adapter, telcoID, businessDate string) (Summary, error) {
	start, end, err := businessDayWindow(cfg.loc, businessDate)
	if err != nil {
		return Summary{}, err
	}
	env, err := adapter.FetchDay(ctx, telcoID, businessDate)
	if err != nil {
		return Summary{}, fmt.Errorf("recovery feed day %s: %w", businessDate, err)
	}
	sum, err := s.reconcileLayer(ctx, recoverySpec(), telcoID, recoveryProgrammeSentinel, start, end, mapFeedToTelco(env, businessDate, start), cfg.toleranceCfg)
	if err != nil {
		return sum, err
	}
	// S3-C2: clear the re-origination hold for any subscriber whose recovery the
	// feed CONFIRMED this day (its token MATCHED). A phantom whose token did not
	// match stays held (surfaced as a break; the subscriber stays blocked). Skipped
	// on a REJECTED run (no ACTIVE items to confirm against).
	if !sum.Rejected && !sum.NothingToReconcile {
		if err := s.clearRecoveryHolds(ctx, telcoID, start, end); err != nil {
			return sum, err
		}
	}
	return sum, nil
}

// clearRecoveryHolds clears the re-origination holds a confirmed recon day proved.
func (s *Service) clearRecoveryHolds(ctx context.Context, telcoID string, start, end time.Time) error {
	tctx := platform.WithTenant(ctx, telcoID)
	return repo.WithTenantTx(tctx, s.Pool, func(tx pgx.Tx) error {
		n, err := (repo.RecoveryHolds{}).ClearMatched(ctx, tx, start, end)
		if err != nil {
			return err
		}
		if n > 0 {
			s.Log.Info("RECOVERY recon cleared feed-confirmed re-origination holds", "telco", telcoID, "cleared", n)
		}
		return nil
	})
}

// mapFeedToTelco maps each per-token EOD deduction into the canonical telco record.
// CreditedAt = the Lagos-midnight day start, so every row windows into [start,end).
func mapFeedToTelco(env recoveryfeed.DayEnvelope, businessDate string, start time.Time) []telcoTransaction {
	out := make([]telcoTransaction, 0, len(env.Rows))
	for _, r := range env.Rows {
		out = append(out, telcoTransaction{
			PlatformRequestID: r.MSISDNToken, // match key = token (see file header)
			TelcoReference:    businessDate,
			FaceValueMinor:    r.RecoveryDeductedMinor,
			Currency:          r.Currency,
			Status:            "SUCCESS", // every feed row is a reported deduction
			CreditedAt:        start,
		})
	}
	return out
}

// dayConfirmed is the S3-B positive-confirmation gate: does this day's recon prove
// the feed is confirming the booked recovery money? A genuine quiet day (nothing
// booked, nothing in the feed) confirms; a feed-only day (all MISSING_PLATFORM)
// does NOT; otherwise the feed must confirm at least min_confirmation_ratio of the
// platform control total (measured on MONEY, not row count, so an intra-day
// sub-event drop where counts happen to match but totals move is caught).
func dayConfirmed(sum Summary, minConfirmationRatio float64) bool {
	// BX-HIGH-016: evidence the recon engine REJECTED can never be positive confirmation for the
	// RECOVERY money gate.
	//
	// A completeness failure sets sum.Rejected, persists a REJECTED run and deliberately preserves
	// the previous ACTIVE run — the engine is saying "this snapshot is not trustworthy". That
	// Summary then returns NORMALLY (no error), and the hold-clearing path below already refuses it
	// with `!sum.Rejected && !sum.NothingToReconcile`. The freshness path did not, so a rejected
	// snapshot could still renew last_recon_at and hold the recharge webhook open.
	//
	// The reachable shape is narrow but is exactly what the completeness floor exists to catch: a
	// prior ACTIVE run with rows, a window with zero platform recovery events, and a feed that
	// collapses to an authenticated zero. The floor rejects it; without this guard the quiet-day
	// branch below then reads (0,0) as "confirmed" and reopens the gate on the evidence the engine
	// just refused.
	//
	// The rule lives HERE rather than at the call site so it holds for every future caller — a
	// second consumer of this predicate must not be able to reintroduce the hole.
	// Note this does NOT change legitimate quiet-day semantics: an authenticated zero day that was
	// NOT rejected still confirms, and an approved completeness override produces an accepted run
	// (Rejected=false) which therefore still confirms.
	if sum.Rejected || sum.NothingToReconcile {
		return false
	}
	return confirmationHolds(int64(sum.PlatformRecords), int64(sum.SourceRecordCount),
		sum.MatchedControlTotalMinor, sum.PlatformControlTotalMinor, minConfirmationRatio)
}

// confirmationHolds is the money-confirmation arithmetic itself, factored out of dayConfirmed so
// BX-MED-004-A2's fenced publisher can independently re-derive the SAME verdict from recon_runs'
// own persisted columns at publish time, with no second hand-copy to drift out of sync. It does
// NOT know about Rejected/NothingToReconcile — those are state exclusions the CALLER is
// responsible for applying first (dayConfirmed applies them via the Summary fields before ever
// reaching here; FencedControlPublisher applies them via recon_runs.state == 'ACTIVE', a
// structurally different but equivalent check — see design_MED-004-A2_v3.md Fix 5's own residual-
// gap note on why this split is documented explicitly rather than silently assumed identical).
func confirmationHolds(platformRecordCount, sourceRecordCount, matchedTotalMinor, platformTotalMinor int64, minConfirmationRatio float64) bool {
	if platformRecordCount == 0 {
		return sourceRecordCount == 0
	}
	floor := int64(math.Ceil(minConfirmationRatio * float64(platformTotalMinor)))
	return matchedTotalMinor >= floor
}

// RecoveryEvidence is the durable, independently-verifiable proof that ONE
// RECOVERY reconciliation run positively confirmed the booked recovery money.
// QualificationID names the durable recon_recovery_qualifications row (BX-MED-004-A2);
// RunID is the recon_runs row it points at. QualificationID+RunID are the only
// fields FencedControlPublisher.Publish trusts — they are a lookup KEY, nothing more.
//
// EvidenceAt is the qualification row's OWN created_at at the moment RunRecoveryControl
// captured it — informational (tests and callers read it to independently verify what was
// captured), never authoritative. BX-MED-004-A2 finding F1: FencedControlPublisher.Publish
// MUST NEVER read this field as an input to its own decision — even a genuine caller value can
// go stale between capture and publish, and a forged/miscalculated one must have zero effect.
// Publish always re-reads recon_recovery_qualifications.created_at fresh, inside its own
// transaction, as the sole authoritative evidence timestamp.
//
// ArmFreshnessMaxSeconds is a MED-004-A1 COMPATIBILITY FIELD ONLY: it exists
// solely so LegacyRecoveryPublisher (still the live Phase-1 production path)
// can reproduce its pre-A2 behaviour with zero semantic change.
// FencedControlPublisher (MED-004-A2) MUST NEVER read this field either — it always
// independently re-resolves the CURRENTLY effective governed window itself
// (see FencedControlPublisher.currentRecoveryCfg). DELETE both compatibility-only
// fields at Phase-2 cutover, when LegacyRecoveryPublisher is deleted alongside them.
type RecoveryEvidence struct {
	QualificationID        string
	RunID                  string
	EvidenceAt             time.Time
	ArmFreshnessMaxSeconds int
}

// RecoveryControlResult is RunRecoveryControl's output. QualifiedEvidence is nil
// unless the newest settled day's RECOVERY reconciliation positively confirmed
// the booked recovery money (dayConfirmed) — nil means the control completed but
// produced no evidence eligible to advance the money gate.
type RecoveryControlResult struct {
	Summaries         []Summary
	QualifiedEvidence *RecoveryEvidence
}

// RunRecoveryControl reconciles the recent settled Lagos business days for an
// ARMED telco, exactly as RunRecovery always has, and reports whether the
// NEWEST settled day's booked recovery money was positively confirmed. Older
// days in the re-sweep window are re-reconciled only when their feed OR
// platform side changed (a backdated/forged event landing in an already-
// reconciled day changes the platform fingerprint -> re-run -> the phantom
// surfaces); unchanged days are skipped (no churn). The newest day is ALWAYS
// reconciled (its fresh Summary drives the confirmation gate).
//
// BX-MED-004-A1 core invariant: executing this control can create
// reconciliation evidence (recon_runs rows) and the other existing
// reconciliation effects (hold-clearing), but it can NEVER change
// recon_layer_arming.last_recon_at — that authority stays with the caller
// (today: LegacyRecoveryPublisher via RunRecovery's wrapper; from MED-004-A2:
// the fenced publisher). See recon_recovery_fence_med004a1_test.go for the
// structural proof.
func (s *Service) RunRecoveryControl(ctx context.Context, telcoID string) (RecoveryControlResult, error) {
	cfg, adapter, err := s.loadForRecovery(ctx, telcoID)
	if err != nil {
		return RecoveryControlResult{}, err
	}
	// Never reconcile-then-arm: only an already-armed telco is reconciled (the
	// webhook is dead-closed until the four-eyes arm path creates the row).
	armed, err := s.Arming.IsLayerArmed(ctx, telcoID, repo.ReconLayerRecovery)
	if err != nil {
		return RecoveryControlResult{}, err
	}
	if !armed {
		return RecoveryControlResult{}, nil
	}
	nowLag := time.Now().UTC().Add(-time.Duration(cfg.ReconLagSeconds) * time.Second)
	dLast, ok := lastSettledBusinessDay(cfg.loc, nowLag)
	if !ok {
		return RecoveryControlResult{}, nil // nothing fully settled yet
	}
	from := nowLag.Add(-time.Duration(cfg.RereconcileLookbackSeconds) * time.Second)
	days := businessDayStrings(cfg.loc, from, dLast)
	out := make([]Summary, 0, len(days))
	var qualified *RecoveryEvidence
	for i, d := range days {
		isLast := i == len(days)-1
		if !isLast {
			unchanged, err := s.recoveryDayUnchanged(ctx, cfg, adapter, telcoID, d)
			if err != nil {
				return RecoveryControlResult{Summaries: out}, err
			}
			if unchanged {
				out = append(out, Summary{Unchanged: true, PeriodEnd: time.Time{}})
				continue
			}
		}
		sum, err := s.reconcileRecoveryDayWith(ctx, cfg, adapter, telcoID, d)
		if err != nil {
			return RecoveryControlResult{Summaries: out}, err
		}
		out = append(out, sum)
		if isLast {
			if dayConfirmed(sum, cfg.MinConfirmationRatio) {
				qualID, evidenceAt, err := s.writeQualification(ctx, telcoID, sum.RunID, cfg)
				if err != nil {
					return RecoveryControlResult{Summaries: out}, err
				}
				qualified = &RecoveryEvidence{
					QualificationID:        qualID,
					RunID:                  sum.RunID,
					EvidenceAt:             evidenceAt,
					ArmFreshnessMaxSeconds: cfg.ArmFreshnessMaxSeconds,
				}
			} else {
				s.Log.Error("RECOVERY feed confirmation shortfall — no qualifying evidence produced; freshness NOT advanced; the webhook fails closed as the gate ages out",
					"telco", telcoID, "day", d,
					"matched_total_minor", sum.MatchedControlTotalMinor,
					"platform_total_minor", sum.PlatformControlTotalMinor,
					"min_confirmation_ratio", cfg.MinConfirmationRatio)
			}
		}
	}
	return RecoveryControlResult{Summaries: out, QualifiedEvidence: qualified}, nil
}

// writeQualification persists the durable BX-MED-004-A2 qualification pointer for a positively-
// confirmed RECOVERY run. UNEXPORTED — Go's own visibility rules make it impossible for any
// package other than this one to reference it, let alone call it, at all. Its ONE legitimate call
// site (above, RunRecoveryControl's post-dayConfirmed-true branch) is structurally proven by
// TestMED004A2_WriteQualificationOnlyFromPostConfirmBranch. It carries NO money figures — the
// fenced publisher reads matched/platform totals from recon_runs itself, never a copy here (see
// design_MED-004-A2_v3.md §1's forgery-risk rationale). The audit-only threshold/window/config-
// version columns record what was ACTUALLY used at qualification time; the publisher's own
// decision never trusts them, always re-resolving the CURRENT governed config independently.
// Returns the row's own DB-generated created_at — the durable EvidenceAt, distinct from
// recon_runs.created_at (which a REJECTED run also gets).
func (s *Service) writeQualification(ctx context.Context, telcoID, runID string, cfg recoveryCfg) (qualificationID string, evidenceAt time.Time, err error) {
	qualificationID = platform.NewID("qual")
	tctx := platform.WithTenant(ctx, telcoID)
	err = repo.WithTenantTx(tctx, s.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO recon_recovery_qualifications
			  (qualification_id, run_id, telco_id, layer,
			   min_confirmation_ratio_used, arm_freshness_max_seconds_used, recon_recovery_config_version_id)
			VALUES ($1,$2,$3,$4, $5,$6,$7)
			RETURNING created_at`,
			qualificationID, runID, telcoID, layerRecovery,
			cfg.MinConfirmationRatio, cfg.ArmFreshnessMaxSeconds, cfg.ConfigVersionID).Scan(&evidenceAt)
	})
	return qualificationID, evidenceAt, err
}

// RunRecovery is the production entrypoint the scheduler/worker calls. It is a
// thin wrapper (MED-004-A1): all reconciliation logic and the positive-
// confirmation gate live in RunRecoveryControl; this function's only remaining
// job is to publish any qualifying evidence to the freshness gate, preserving
// the exact behaviour this function had before the extraction.
func (s *Service) RunRecovery(ctx context.Context, telcoID string) ([]Summary, error) {
	res, err := s.RunRecoveryControl(ctx, telcoID)
	if err != nil {
		return res.Summaries, err
	}
	if res.QualifiedEvidence != nil {
		if err := s.LegacyRecoveryPublisher(ctx, telcoID, res.QualifiedEvidence); err != nil {
			return res.Summaries, err
		}
	}
	return res.Summaries, nil
}

// LegacyRecoveryPublisher is the MED-004-A1 temporary compatibility publisher:
// it does exactly what RunRecovery's freshness-advance did before this
// extraction — AdvanceFreshness with the evidence's own governed window — kept
// as a named, isolated function so it is unmistakably marked for deletion.
//
// DELETE AT MED-004-A2 CUTOVER, when FencedControlPublisher becomes the sole,
// atomically-fenced authority+mutation writer. Do not call this from the new
// scheduler; do not add new callers.
func (s *Service) LegacyRecoveryPublisher(ctx context.Context, telcoID string, evidence *RecoveryEvidence) error {
	_, err := s.Arming.AdvanceFreshness(ctx, telcoID, repo.ReconLayerRecovery, evidence.ArmFreshnessMaxSeconds)
	return err
}

// recoveryDayUnchanged reports whether a day already has an ACTIVE run whose feed
// source-hash AND platform fingerprint (count, net total) both still match — i.e.
// nothing new arrived on either side, so re-reconciling would only churn.
func (s *Service) recoveryDayUnchanged(ctx context.Context, cfg recoveryCfg, adapter recoveryfeed.Adapter, telcoID, businessDate string) (bool, error) {
	start, end, err := businessDayWindow(cfg.loc, businessDate)
	if err != nil {
		return false, err
	}
	var storedHash string
	var storedPlatCount, storedPlatTotal int64
	tctx := platform.WithTenant(ctx, telcoID)
	err = repo.WithTenantTx(tctx, s.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT source_hash, platform_record_count, platform_control_total_minor
			FROM recon_runs
			WHERE telco_id=$1 AND programme_id=$2 AND layer=$3 AND state='ACTIVE' AND period_start=$4`,
			telcoID, recoveryProgrammeSentinel, layerRecovery, start).Scan(&storedHash, &storedPlatCount, &storedPlatTotal)
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil // never reconciled -> must reconcile
		}
		return false, err
	}
	env, err := adapter.FetchDay(ctx, telcoID, businessDate)
	if err != nil {
		return false, err
	}
	_, _, freshHash, err := sourceManifest(mapFeedToTelco(env, businessDate, start), cfg.MaxAmountMinor)
	if err != nil {
		return false, err
	}
	pc, pt, err := s.recoveryPlatformFingerprint(ctx, telcoID, start, end)
	if err != nil {
		return false, err
	}
	return freshHash == storedHash && int64(pc) == storedPlatCount && pt == storedPlatTotal, nil
}

// recoveryPlatformFingerprint is the (count, net-total) of the platform side for a
// day — the cheap signal that a late/backdated event changed an already-reconciled
// window (the telco-only hash skip of ReconcileRecentPeriods would miss this).
func (s *Service) recoveryPlatformFingerprint(ctx context.Context, telcoID string, start, end time.Time) (int, int64, error) {
	var count int
	var total int64
	tctx := platform.WithTenant(ctx, telcoID)
	err := repo.WithTenantTx(tctx, s.Pool, func(tx pgx.Tx) error {
		plat, e := recoverySpec().fetchPlatform(ctx, tx, "", start, end)
		if e != nil {
			return e
		}
		count = len(plat)
		for _, p := range plat {
			total += p.FaceValueMinor
		}
		return nil
	})
	return count, total, err
}

// lastSettledBusinessDay returns the most recent Lagos business day whose window
// end (next civil midnight) is at or before the cutoff (now-lag) — i.e. fully
// settled. ok=false if none in the recent past.
func lastSettledBusinessDay(loc *time.Location, cutoff time.Time) (string, bool) {
	c := cutoff.In(loc)
	d := time.Date(c.Year(), c.Month(), c.Day(), 0, 0, 0, 0, loc)
	for i := 0; i < 4; i++ {
		if end := d.AddDate(0, 0, 1); !end.After(cutoff) {
			return d.Format("2006-01-02"), true
		}
		d = d.AddDate(0, 0, -1)
	}
	return "", false
}

// businessDayStrings lists the Lagos business days from the calendar date of `from`
// through `toDate` inclusive, oldest first (bounded by the re-sweep lookback).
func businessDayStrings(loc *time.Location, from time.Time, toDate string) []string {
	end, err := time.ParseInLocation("2006-01-02", toDate, loc)
	if err != nil {
		return nil
	}
	f := from.In(loc)
	cur := time.Date(f.Year(), f.Month(), f.Day(), 0, 0, 0, 0, loc)
	var out []string
	for !cur.After(end) {
		out = append(out, cur.Format("2006-01-02"))
		cur = cur.AddDate(0, 0, 1)
	}
	return out
}

// loadForRecovery reads recon.recovery (telco->global) with every fail-closed
// floor re-asserted at load (a raw seed bypasses the validator, so the floor lives
// in code too), and builds the config-selected feed adapter.
func (s *Service) loadForRecovery(ctx context.Context, telcoID string) (recoveryCfg, recoveryfeed.Adapter, error) {
	var rc recoveryCfg
	cv, err := s.Config.ActiveAt(ctx, "recon.recovery", "telco:"+telcoID, time.Now().UTC())
	if err != nil {
		return rc, nil, fmt.Errorf("recon.recovery config: %w", err)
	}
	var raw struct {
		AmountToleranceMinor       int64   `json:"amount_tolerance_minor"`
		AutoResolve                bool    `json:"auto_resolve"`
		MaxAmountMinor             int64   `json:"max_amount_minor"`
		MinCompletenessRatio       float64 `json:"min_completeness_ratio"`
		MinConfirmationRatio       float64 `json:"min_confirmation_ratio"`
		ReconLagSeconds            int     `json:"recon_lag_seconds"`
		RereconcileLookbackSeconds int     `json:"rereconcile_lookback_seconds"`
		BusinessTimezone           string  `json:"business_timezone"`
		ArmFreshnessMaxSeconds     int     `json:"arm_freshness_max_seconds"`
	}
	if err := json.Unmarshal(cv.Content, &raw); err != nil {
		return rc, nil, err
	}
	if raw.MaxAmountMinor <= 0 || raw.MaxAmountMinor > 1_000_000_000_000_000 {
		return rc, nil, fmt.Errorf("recon.recovery max_amount_minor out of range — refusing")
	}
	if raw.AutoResolve {
		return rc, nil, fmt.Errorf("recon.recovery auto_resolve must be false — refusing (a money break is never auto-resolved)")
	}
	if raw.MinCompletenessRatio <= 0 || raw.MinCompletenessRatio > 1 {
		return rc, nil, fmt.Errorf("recon.recovery min_completeness_ratio must be in (0,1] — refusing")
	}
	if raw.MinConfirmationRatio <= 0 || raw.MinConfirmationRatio > 1 {
		return rc, nil, fmt.Errorf("recon.recovery min_confirmation_ratio must be in (0,1] — refusing")
	}
	if raw.ReconLagSeconds < 0 {
		return rc, nil, fmt.Errorf("recon.recovery recon_lag_seconds must be >= 0 — refusing")
	}
	if raw.RereconcileLookbackSeconds <= 0 {
		return rc, nil, fmt.Errorf("recon.recovery rereconcile_lookback_seconds must be > 0 — refusing")
	}
	if raw.ArmFreshnessMaxSeconds < 3600 || raw.ArmFreshnessMaxSeconds > 604_800 {
		return rc, nil, fmt.Errorf("recon.recovery arm_freshness_max_seconds must be 3600..604800 — refusing")
	}
	loc, err := time.LoadLocation(raw.BusinessTimezone)
	if err != nil {
		return rc, nil, fmt.Errorf("recon.recovery business_timezone %q not loadable — refusing: %w", raw.BusinessTimezone, err)
	}
	if _, oj := time.Date(2025, 1, 15, 12, 0, 0, 0, loc).Zone(); true {
		if _, ol := time.Date(2025, 7, 15, 12, 0, 0, 0, loc).Zone(); oj != ol {
			return rc, nil, fmt.Errorf("recon.recovery business_timezone %q observes DST — refusing (day-bucketing parity unsafe)", raw.BusinessTimezone)
		}
	}
	rc = recoveryCfg{
		toleranceCfg: toleranceCfg{
			AmountToleranceMinor:       raw.AmountToleranceMinor,
			AutoResolve:                raw.AutoResolve,
			MaxAmountMinor:             raw.MaxAmountMinor,
			MinCompletenessRatio:       raw.MinCompletenessRatio,
			ReconLagSeconds:            raw.ReconLagSeconds,
			RereconcileLookbackSeconds: raw.RereconcileLookbackSeconds,
		},
		MinConfirmationRatio:   raw.MinConfirmationRatio,
		ArmFreshnessMaxSeconds: raw.ArmFreshnessMaxSeconds,
		BusinessTimezone:       raw.BusinessTimezone,
		loc:                    loc,
		ConfigVersionID:        cv.ConfigVersionID,
	}
	adapter, err := s.buildFeedAdapter(ctx, telcoID)
	if err != nil {
		return rc, nil, err
	}
	return rc, adapter, nil
}

// buildFeedAdapter selects the EOD-feed adapter from telco.recovery_feed. A mock
// feed is permitted ONLY on a synthetic telco (telcos.is_synthetic) — the
// circularity guard the validator cannot enforce (it lacks the scope). A real
// telco requires source=https + an envelope HMAC block.
func (s *Service) buildFeedAdapter(ctx context.Context, telcoID string) (recoveryfeed.Adapter, error) {
	cv, err := s.Config.ActiveAt(ctx, "telco.recovery_feed", "telco:"+telcoID, time.Now().UTC())
	if err != nil {
		return nil, fmt.Errorf("telco.recovery_feed config: %w", err)
	}
	var fc struct {
		Source       string `json:"source"`
		URL          string `json:"url"`
		EnvelopeAuth *struct {
			SecretEnv      string `json:"secret_env"`
			SignatureField string `json:"signature_field"`
		} `json:"envelope_auth"`
	}
	if err := json.Unmarshal(cv.Content, &fc); err != nil {
		return nil, err
	}
	switch fc.Source {
	case "mock":
		var synthetic bool
		if err := s.Pool.QueryRow(ctx, `SELECT is_synthetic FROM telcos WHERE telco_id=$1`, telcoID).Scan(&synthetic); err != nil {
			return nil, fmt.Errorf("is_synthetic lookup for %q: %w", telcoID, err)
		}
		if !synthetic {
			return nil, fmt.Errorf("telco.recovery_feed: source=mock is not permitted on non-synthetic telco %q (circularity guard)", telcoID)
		}
		return &recoveryfeed.MockAdapter{Pool: s.Pool}, nil
	case "https":
		if fc.EnvelopeAuth == nil || fc.EnvelopeAuth.SecretEnv == "" {
			return nil, fmt.Errorf("telco.recovery_feed: source=https requires envelope_auth.secret_env")
		}
		return &recoveryfeed.HTTPAdapter{
			Client: s.HTTPClient, URL: fc.URL,
			SecretEnv: fc.EnvelopeAuth.SecretEnv, SignatureField: fc.EnvelopeAuth.SignatureField,
		}, nil
	default:
		return nil, fmt.Errorf("telco.recovery_feed: unknown source %q", fc.Source)
	}
}
