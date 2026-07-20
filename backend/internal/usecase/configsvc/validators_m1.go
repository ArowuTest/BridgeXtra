package configsvc

// M1 domain validators (BUILD_PLAN §7a). A config domain without a validator
// is a review finding; a value the code cannot honor must be REJECTED at
// approval, never stored-and-ignored (SF-2 armed-but-dead prevention).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/jackc/pgx/v5"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
)

func init() {
	validators["product.airtime"] = validateProductAirtime
	validators["advance.reservation"] = validateReservation
	validators["advance.fulfilment"] = validateFulfilment
	validators["recovery.allocation"] = validateAllocation
	validators["telco.adapter"] = validateTelcoAdapter
	validators["recon.tolerance"] = validateReconTolerance
	validators["ledger.accounts"] = validateLedgerAccounts
}

// validateLedgerAccounts (M1B2-F1): an empty or malformed chart fails closed
// at posting time (safe direction) but stops ALL money movement — an outage
// foot-gun an admin must not be able to approve.
func validateLedgerAccounts(ctx context.Context, tx pgx.Tx, content json.RawMessage) error {
	var v struct {
		Accounts []struct {
			Code *string `json:"code"`
			Kind *string `json:"kind"`
		} `json:"accounts"`
	}
	if err := strictUnmarshal(content, &v); err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	if len(v.Accounts) == 0 {
		return fmt.Errorf("accounts must be non-empty: an empty chart halts all ledger posting (M1B2-F1)")
	}
	kinds := map[string]bool{"ASSET": true, "LIABILITY": true, "INCOME": true, "EXPENSE": true, "EQUITY": true}
	seen := map[string]bool{}
	for i, a := range v.Accounts {
		if a.Code == nil || *a.Code == "" {
			return fmt.Errorf("accounts[%d]: code is required", i)
		}
		if seen[*a.Code] {
			return fmt.Errorf("accounts[%d]: duplicate code %q", i, *a.Code)
		}
		seen[*a.Code] = true
		if a.Kind == nil || !kinds[*a.Kind] {
			return fmt.Errorf("accounts[%d] (%s): kind must be one of ASSET|LIABILITY|INCOME|EXPENSE|EQUITY", i, *a.Code)
		}
	}
	return nil
}

func validateProductAirtime(ctx context.Context, tx pgx.Tx, content json.RawMessage) error {
	var v struct {
		Currency           *entity.Currency `json:"currency"`
		DenominationsMinor []int64          `json:"denominations_minor"`
		FeeBps             *int64           `json:"fee_bps"`
		FeeModel           *string          `json:"fee_model"`
		OfferExpiryMinutes *int             `json:"offer_expiry_minutes"`
	}
	if err := strictUnmarshal(content, &v); err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	if v.Currency == nil || !v.Currency.Valid() {
		return fmt.Errorf("currency is required and must be ISO 4217 alpha-3")
	}
	if len(v.DenominationsMinor) == 0 {
		return fmt.Errorf("denominations_minor must be a non-empty ascending ladder")
	}
	prev := int64(0)
	for i, d := range v.DenominationsMinor {
		if d <= 0 {
			return fmt.Errorf("denomination[%d]=%d must be positive minor units", i, d)
		}
		if d <= prev {
			return fmt.Errorf("denominations_minor must be strictly ascending (index %d)", i)
		}
		prev = d
	}
	if v.FeeBps == nil || *v.FeeBps < 0 || *v.FeeBps > 10_000 {
		return fmt.Errorf("fee_bps is required and must be 0..10000 (basis points, never a float percentage — ADR-0002)")
	}
	switch {
	case v.FeeModel == nil:
		return fmt.Errorf("fee_model is required")
	case *v.FeeModel != "DEDUCTED_UPFRONT" && *v.FeeModel != "ADDED_TO_REPAYMENT":
		return fmt.Errorf("fee_model %q not supported: DEDUCTED_UPFRONT | ADDED_TO_REPAYMENT (V1 §6.1)", *v.FeeModel)
	}
	if v.OfferExpiryMinutes == nil || *v.OfferExpiryMinutes < 1 {
		return fmt.Errorf("offer_expiry_minutes is required and must be >= 1")
	}
	return nil
}

func validateReservation(ctx context.Context, tx pgx.Tx, content json.RawMessage) error {
	var v struct {
		TTLMinutes    *int    `json:"reservation_ttl_minutes"`
		ExpiredRepair *string `json:"expired_repair"`
	}
	if err := strictUnmarshal(content, &v); err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	if v.TTLMinutes == nil || *v.TTLMinutes < 1 || *v.TTLMinutes > 24*60 {
		return fmt.Errorf("reservation_ttl_minutes must be 1..1440")
	}
	if v.ExpiredRepair == nil || *v.ExpiredRepair != "RELEASE_WITH_AUDIT" {
		return fmt.Errorf("expired_repair must be RELEASE_WITH_AUDIT (the only implemented policy — V1-ADV-005)")
	}
	return nil
}

func validateFulfilment(ctx context.Context, tx pgx.Tx, content json.RawMessage) error {
	var v struct {
		Delays            []int `json:"status_enquiry_delays_seconds"`
		EscalationMinutes *int  `json:"unknown_escalation_minutes"`
	}
	if err := strictUnmarshal(content, &v); err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	if len(v.Delays) == 0 {
		return fmt.Errorf("status_enquiry_delays_seconds must be non-empty: FULFILMENT_UNKNOWN without enquiry is a stalled book (V2-ADV-009)")
	}
	prev := 0
	for i, d := range v.Delays {
		if d < 1 || d < prev {
			return fmt.Errorf("status_enquiry_delays_seconds must be positive and non-decreasing (index %d)", i)
		}
		prev = d
	}
	if v.EscalationMinutes == nil || *v.EscalationMinutes < 1 {
		return fmt.Errorf("unknown_escalation_minutes must be >= 1")
	}
	return nil
}

func validateAllocation(ctx context.Context, tx pgx.Tx, content json.RawMessage) error {
	var v struct {
		Waterfall    []string `json:"waterfall"`
		OverRecovery *string  `json:"over_recovery"`
	}
	if err := strictUnmarshal(content, &v); err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	seen := map[string]bool{}
	for _, c := range v.Waterfall {
		if c != "FEE" && c != "PRINCIPAL" {
			return fmt.Errorf("waterfall component %q not supported: FEE | PRINCIPAL", c)
		}
		if seen[c] {
			return fmt.Errorf("waterfall component %q duplicated", c)
		}
		seen[c] = true
	}
	if !seen["FEE"] || !seen["PRINCIPAL"] {
		return fmt.Errorf("waterfall must include both FEE and PRINCIPAL exactly once (V2-COL-004)")
	}
	if v.OverRecovery == nil || *v.OverRecovery != "QUARANTINE_SUSPENSE" {
		return fmt.Errorf("over_recovery must be QUARANTINE_SUSPENSE (the only implemented policy — EDG-020)")
	}
	return nil
}

func validateTelcoAdapter(ctx context.Context, tx pgx.Tx, content json.RawMessage) error {
	var v struct {
		FulfilmentURL          *string `json:"fulfilment_url"`
		RequestTimeoutMs       *int    `json:"request_timeout_ms"`
		RetryBudget            *int    `json:"retry_budget"`
		CircuitErrThreshPct    *int    `json:"circuit_error_threshold_pct"`
		CircuitMinRequests     *int    `json:"circuit_min_requests"`
		CircuitCooldownSeconds *int    `json:"circuit_cooldown_seconds"`
		MaxWeeklyRechargeMinor *int64  `json:"max_weekly_recharge_minor"`
	}
	if err := strictUnmarshal(content, &v); err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	if v.FulfilmentURL == nil {
		return fmt.Errorf("fulfilment_url is required")
	}
	u, err := url.Parse(*v.FulfilmentURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("fulfilment_url must be an absolute http(s) URL")
	}
	if v.RequestTimeoutMs == nil || *v.RequestTimeoutMs < 100 || *v.RequestTimeoutMs > 60_000 {
		return fmt.Errorf("request_timeout_ms must be 100..60000")
	}
	// INV-009: fulfilment is never transport-retried. Anything else is a
	// double-credit vector; the enquiry/reconciliation path owns ambiguity.
	if v.RetryBudget == nil || *v.RetryBudget != 0 {
		return fmt.Errorf("retry_budget must be 0 for fulfilment: blind transport retry is prohibited (INV-009, V2-TEL-003)")
	}
	if v.CircuitErrThreshPct == nil || *v.CircuitErrThreshPct < 1 || *v.CircuitErrThreshPct > 100 {
		return fmt.Errorf("circuit_error_threshold_pct must be 1..100")
	}
	// R-P0-8b-F1: the breaker evaluates a rolling error rate over the last
	// circuit_min_requests calls, so this is also the breaker's sample-window
	// size. A finite ceiling keeps that window (a) a bounded allocation and
	// (b) a real control — a five-figure min sample would leave a low-volume
	// telco's breaker perpetually below quorum, i.e. armed-but-dead.
	if v.CircuitMinRequests == nil || *v.CircuitMinRequests < 1 || *v.CircuitMinRequests > 10_000 {
		return fmt.Errorf("circuit_min_requests must be 1..10000 (also the rolling error-rate sample window)")
	}
	// R-P0-8b: the open→half-open cooldown. Required so the breaker is a real
	// control, not the armed-but-dead pair of thresholds it was before.
	if v.CircuitCooldownSeconds == nil || *v.CircuitCooldownSeconds < 1 || *v.CircuitCooldownSeconds > 3600 {
		return fmt.Errorf("circuit_cooldown_seconds must be 1..3600 (the circuit-breaker open→half-open cooldown)")
	}
	// G2-F3: the feed plausibility ceiling — a corrupt row near int64-max
	// must be quarantined at ingest, never scored. Required, positive, and
	// small enough that downstream bps arithmetic can never overflow.
	if v.MaxWeeklyRechargeMinor == nil || *v.MaxWeeklyRechargeMinor <= 0 ||
		*v.MaxWeeklyRechargeMinor > 922_000_000_000_000 {
		return fmt.Errorf("max_weekly_recharge_minor is required and must be in (0, 922e12] — the feature-feed plausibility ceiling (G2-F3)")
	}
	return nil
}

func validateReconTolerance(ctx context.Context, tx pgx.Tx, content json.RawMessage) error {
	var v struct {
		AmountToleranceMinor *int64   `json:"amount_tolerance_minor"`
		AutoResolve          *bool    `json:"auto_resolve"`
		BreakAgingAlertHours *int     `json:"break_aging_alert_hours"`
		MaxAmountMinor       *int64   `json:"max_amount_minor"`
		MinCompletenessRatio *float64 `json:"min_completeness_ratio"`
		ReconLagSeconds      *int     `json:"recon_lag_seconds"`
		RereconcileLookback  *int     `json:"rereconcile_lookback_seconds"`
	}
	if err := strictUnmarshal(content, &v); err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	if v.AmountToleranceMinor == nil || *v.AmountToleranceMinor < 0 {
		return fmt.Errorf("amount_tolerance_minor must be >= 0")
	}
	// R-P0-4: a credible-amount ceiling is required — it is the overflow guard
	// for comparing external telco amounts. Kept below 1e15 so the difference
	// of two in-range amounts can never overflow int64 or trip abs64(MinInt64).
	if v.MaxAmountMinor == nil || *v.MaxAmountMinor <= 0 || *v.MaxAmountMinor > 1_000_000_000_000_000 {
		return fmt.Errorf("max_amount_minor must be in 1..1e15 (the credible-amount / overflow-guard ceiling)")
	}
	if v.AmountToleranceMinor != nil && *v.AmountToleranceMinor > *v.MaxAmountMinor {
		return fmt.Errorf("amount_tolerance_minor cannot exceed max_amount_minor")
	}
	if v.AutoResolve == nil {
		return fmt.Errorf("auto_resolve is required")
	}
	// R-P0-6 Slice E1: the governed floor — a money break is NEVER auto-resolved
	// (V1-FIN-005). auto_resolve must be false; a break clears only via the
	// two-actor maker-checker resolution path. true is refused, not discouraged.
	if *v.AutoResolve {
		return fmt.Errorf("auto_resolve must be false — a money break is never auto-resolved (V1-FIN-005); resolution is a two-actor decision")
	}
	if v.BreakAgingAlertHours == nil || *v.BreakAgingAlertHours < 1 {
		return fmt.Errorf("break_aging_alert_hours must be >= 1")
	}
	// R-P0-6: a completeness floor in (0,1] is required so an empty or truncated
	// rerun cannot supersede (wipe) a good reconciliation. A value of 1 demands
	// a rerun be at least as large as the prior; below that some shrinkage is
	// tolerated, but zero/absent is refused (no protection).
	if v.MinCompletenessRatio == nil || *v.MinCompletenessRatio <= 0 || *v.MinCompletenessRatio > 1 {
		return fmt.Errorf("min_completeness_ratio must be in (0,1] (rerun-completeness protection)")
	}
	// R-P0-6 Slice C: the settling lag bounds the reconciliation window end
	// (now - lag). Required and non-negative; a sane ceiling (7 days) stops a
	// mis-set value from parking the window indefinitely in the past.
	if v.ReconLagSeconds == nil || *v.ReconLagSeconds < 0 || *v.ReconLagSeconds > 604_800 {
		return fmt.Errorf("recon_lag_seconds must be 0..604800 (the settling lag before a window is reconciled)")
	}
	// R-P0-6 Slice D2 (VR-50-F1): the late-arrival re-reconcile horizon. A
	// telco credit that lands after its window was reconciled is recovered by the
	// scheduled re-reconcile only if the window ended within this horizon. Required
	// and POSITIVE — a zero/absent lookback would silently strand late arrivals as
	// missing-telco breaks (armed-but-dead). Ceiling 90d keeps the sweep bounded.
	if v.RereconcileLookback == nil || *v.RereconcileLookback < 1 || *v.RereconcileLookback > 7_776_000 {
		return fmt.Errorf("rereconcile_lookback_seconds must be in 1..7776000 (the late-arrival re-reconcile horizon)")
	}
	return nil
}
