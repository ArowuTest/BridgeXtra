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
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/recoveryfeed"
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

// ReconcileRecoveryDay reconciles ONE Lagos business day: it authenticates the EOD
// feed via the config-selected adapter, maps each per-token deduction into the
// canonical telcoTransaction, and runs the shared engine. It does NOT advance
// arming freshness — that is the positive-confirmation gate in S3-B.
func (s *Service) ReconcileRecoveryDay(ctx context.Context, telcoID, businessDate string) (Summary, error) {
	cfg, adapter, err := s.loadForRecovery(ctx, telcoID)
	if err != nil {
		return Summary{RunID: platform.NewID("run")}, err
	}
	start, end, err := businessDayWindow(cfg.loc, businessDate)
	if err != nil {
		return Summary{}, err
	}
	// The feed is fetched + authenticated OUTSIDE the recon tx (like fulfilment's
	// telco fetch); a bad envelope MAC / manifest mismatch fails the whole day.
	env, err := adapter.FetchDay(ctx, telcoID, businessDate)
	if err != nil {
		return Summary{}, fmt.Errorf("recovery feed day %s: %w", businessDate, err)
	}
	telcoRecords := make([]telcoTransaction, 0, len(env.Rows))
	for _, r := range env.Rows {
		telcoRecords = append(telcoRecords, telcoTransaction{
			PlatformRequestID: r.MSISDNToken, // match key = token (see file header)
			TelcoReference:    businessDate,  //
			FaceValueMinor:    r.RecoveryDeductedMinor,
			Currency:          r.Currency,
			Status:            "SUCCESS", // every feed row is a reported deduction
			CreditedAt:        start,     // Lagos-midnight D -> windows into [start,end)
		})
	}
	return s.reconcileLayer(ctx, recoverySpec(), telcoID, recoveryProgrammeSentinel, start, end, telcoRecords, cfg.toleranceCfg)
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
