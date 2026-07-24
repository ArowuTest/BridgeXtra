package configsvc

// Phase 1 S3 — RECOVERY reconciliation config validators (build/PHASE1_S3_DESIGN.md).
// Two domains, both fail-closed / no-hardcoding:
//   * recon.recovery      — the RECOVERY-layer recon knobs (tolerance, completeness,
//                           money-confirmation floor, business-day tz, arm-freshness).
//   * telco.recovery_feed — the EOD-feed adapter selector (mock|https). A real telco
//                           needs source=https + an envelope HMAC block; secrets are
//                           env-var NAMES only, never raw (the S1/S2 rule).
//
// Two enforcement notes deliberately NOT in these validators (the Validator
// signature receives only the content, never the scope, so a per-telco check is
// impossible here):
//   * "mock feed only on a synthetic telco" is enforced where the telco is known —
//     the recon loader (loadForRecovery) and the four-eyes arm-maker (S3-C) — via
//     telcos.is_synthetic. This validator only checks the shape.
//   * business_timezone must have Go⇄Postgres tzdata parity; time/tzdata is
//     blank-imported here so the embedded zone database travels with EVERY binary
//     that can activate this config (the validator LoadLocation cannot fail for a
//     missing base-image tzdata).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	_ "time/tzdata" // embed the IANA zone DB so business_timezone always resolves

	"github.com/jackc/pgx/v5"
)

func init() {
	validators["recon.recovery"] = validateReconRecovery
	validators["telco.recovery_feed"] = validateReconFeed
}

func validateReconRecovery(_ context.Context, _ pgx.Tx, content json.RawMessage) error {
	var v struct {
		AmountToleranceMinor   *int64   `json:"amount_tolerance_minor"`
		AutoResolve            *bool    `json:"auto_resolve"`
		MaxAmountMinor         *int64   `json:"max_amount_minor"`
		MinCompletenessRatio   *float64 `json:"min_completeness_ratio"`
		MinConfirmationRatio   *float64 `json:"min_confirmation_ratio"`
		ReconLagSeconds        *int     `json:"recon_lag_seconds"`
		RereconcileLookback    *int     `json:"rereconcile_lookback_seconds"`
		BreakAgingAlertHours   *int     `json:"break_aging_alert_hours"`
		BusinessTimezone       *string  `json:"business_timezone"`
		ArmFreshnessMaxSeconds *int     `json:"arm_freshness_max_seconds"`
	}
	if err := strictUnmarshal(content, &v); err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	// Overflow-guard ceiling first (tolerance is bounded by it).
	if v.MaxAmountMinor == nil || *v.MaxAmountMinor <= 0 || *v.MaxAmountMinor > 1_000_000_000_000_000 {
		return fmt.Errorf("recon.recovery: max_amount_minor must be in 1..1e15 (credible-amount / overflow-guard ceiling)")
	}
	if v.AmountToleranceMinor == nil || *v.AmountToleranceMinor < 0 {
		return fmt.Errorf("recon.recovery: amount_tolerance_minor must be >= 0")
	}
	if *v.AmountToleranceMinor > *v.MaxAmountMinor {
		return fmt.Errorf("recon.recovery: amount_tolerance_minor cannot exceed max_amount_minor")
	}
	// A money break is NEVER auto-resolved (V1-FIN-005) — true is refused.
	if v.AutoResolve == nil {
		return fmt.Errorf("recon.recovery: auto_resolve is required")
	}
	if *v.AutoResolve {
		return fmt.Errorf("recon.recovery: auto_resolve must be false — a money break is never auto-resolved (V1-FIN-005); resolution is a two-actor decision")
	}
	if v.MinCompletenessRatio == nil || *v.MinCompletenessRatio <= 0 || *v.MinCompletenessRatio > 1 {
		return fmt.Errorf("recon.recovery: min_completeness_ratio must be in (0,1] (re-delivery supersession protection)")
	}
	// The MONEY-confirmation floor: the fraction of booked recovery money the
	// independent feed must confirm before "live" freshness advances (S3-B). In
	// (0,1] — never 0 (would confirm nothing) and never required to be exactly 1
	// (one late single-subscriber row must not dark the whole telco's webhook; the
	// re-sweep re-confirms late rows). Seeded high (0.99).
	if v.MinConfirmationRatio == nil || *v.MinConfirmationRatio <= 0 || *v.MinConfirmationRatio > 1 {
		return fmt.Errorf("recon.recovery: min_confirmation_ratio must be in (0,1] (the money-confirmation floor for freshness)")
	}
	if v.ReconLagSeconds == nil || *v.ReconLagSeconds < 0 || *v.ReconLagSeconds > 604_800 {
		return fmt.Errorf("recon.recovery: recon_lag_seconds must be 0..604800 (the settling lag)")
	}
	if v.RereconcileLookback == nil || *v.RereconcileLookback < 1 || *v.RereconcileLookback > 7_776_000 {
		return fmt.Errorf("recon.recovery: rereconcile_lookback_seconds must be in 1..7776000 (late-arrival / backdate re-sweep horizon)")
	}
	if v.BreakAgingAlertHours == nil || *v.BreakAgingAlertHours < 1 {
		return fmt.Errorf("recon.recovery: break_aging_alert_hours must be >= 1")
	}
	// The arm-freshness window. Bounded [1h, 7d] so a typo can neither collapse the
	// window to 0 (would never be live) nor open an unbounded one (the structural
	// CHECK on recon_layer_arming.arm_freshness_max_seconds mirrors this ceiling).
	if v.ArmFreshnessMaxSeconds == nil || *v.ArmFreshnessMaxSeconds < 3600 || *v.ArmFreshnessMaxSeconds > 604_800 {
		return fmt.Errorf("recon.recovery: arm_freshness_max_seconds must be 3600..604800 (1h..7d freshness window)")
	}
	// Business-day bucketing timezone: must load AND be fixed-offset (no DST).
	if v.BusinessTimezone == nil || *v.BusinessTimezone == "" {
		return fmt.Errorf("recon.recovery: business_timezone is required (IANA zone, e.g. Africa/Lagos)")
	}
	if err := requireFixedOffsetZone(*v.BusinessTimezone); err != nil {
		return fmt.Errorf("recon.recovery: %w", err)
	}
	return nil
}

// requireFixedOffsetZone refuses a business_timezone that is not loadable or that
// observes DST. Day-bucketing parity between Go's window math and Postgres'
// AT TIME ZONE is only guaranteed for a fixed offset here; a DST zone would need a
// verified tzdata-parity path not built. Deterministic (fixed probe dates).
func requireFixedOffsetZone(name string) error {
	loc, err := time.LoadLocation(name)
	if err != nil {
		return fmt.Errorf("business_timezone %q is not a loadable IANA zone: %w", name, err)
	}
	_, offJan := time.Date(2025, 1, 15, 12, 0, 0, 0, loc).Zone()
	_, offJul := time.Date(2025, 7, 15, 12, 0, 0, 0, loc).Zone()
	if offJan != offJul {
		return fmt.Errorf("business_timezone %q observes DST (offset %ds in Jan vs %ds in Jul); only fixed-offset zones are supported", name, offJan, offJul)
	}
	return nil
}

func validateReconFeed(_ context.Context, _ pgx.Tx, content json.RawMessage) error {
	var v struct {
		Source            *string `json:"source"`
		ExpectedCurrency  *string `json:"expected_currency"`
		URL               *string `json:"url"`
		BusinessDateBasis *string `json:"business_date_basis"`
		EnvelopeAuth      *struct {
			Scheme         *string `json:"scheme"`
			SecretEnv      *string `json:"secret_env"`
			Secret         *string `json:"secret"`        // forbidden — raw secret
			ClientSecret   *string `json:"client_secret"` // forbidden — raw secret
			SignatureField *string `json:"signature_field"`
		} `json:"envelope_auth"`
	}
	if err := strictUnmarshal(content, &v); err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	if v.Source == nil || (*v.Source != "https" && *v.Source != "mock") {
		return fmt.Errorf("telco.recovery_feed: source must be \"https\" or \"mock\"")
	}
	if v.ExpectedCurrency == nil || !isISO4217(*v.ExpectedCurrency) {
		return fmt.Errorf("telco.recovery_feed: expected_currency must be a 3-letter ISO-4217 code")
	}
	// business_date_basis is pinned to the single supported semantic — changing it
	// is a coordinated code+config act (the MTN ask), not a silent config edit.
	if v.BusinessDateBasis == nil || *v.BusinessDateBasis != "occurred_at_lagos_date" {
		return fmt.Errorf("telco.recovery_feed: business_date_basis must be \"occurred_at_lagos_date\" (the only supported basis)")
	}
	if *v.Source == "https" {
		if v.URL == nil {
			return fmt.Errorf("telco.recovery_feed: url is required for source=https")
		}
		u, err := url.Parse(*v.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("telco.recovery_feed: url must be an absolute http(s) URL")
		}
		if v.EnvelopeAuth == nil {
			return fmt.Errorf("telco.recovery_feed: envelope_auth is required for source=https (the feed body must be authenticated; SafeClient guards only the outbound request)")
		}
		if v.EnvelopeAuth.Secret != nil || v.EnvelopeAuth.ClientSecret != nil {
			return fmt.Errorf("telco.recovery_feed: raw secrets are forbidden — use secret_env (env-var name, resolved at verify time)")
		}
		if v.EnvelopeAuth.Scheme == nil || *v.EnvelopeAuth.Scheme != "hmac_sha256" {
			return fmt.Errorf("telco.recovery_feed: envelope_auth.scheme must be \"hmac_sha256\" (only supported feed auth)")
		}
		if v.EnvelopeAuth.SecretEnv == nil || *v.EnvelopeAuth.SecretEnv == "" {
			return fmt.Errorf("telco.recovery_feed: envelope_auth.secret_env is required (env var holding the HMAC secret)")
		}
	} else {
		// source=mock: no url / envelope_auth. The synthetic-telco restriction is
		// enforced at the loader + arm-maker (which know the telco), not here.
		if v.URL != nil || v.EnvelopeAuth != nil {
			return fmt.Errorf("telco.recovery_feed: source=mock must not carry url / envelope_auth")
		}
	}
	return nil
}
