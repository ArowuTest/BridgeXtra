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
	if err := json.Unmarshal(content, &v); err != nil {
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
	if err := json.Unmarshal(content, &v); err != nil {
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
	if err := json.Unmarshal(content, &v); err != nil {
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
	if err := json.Unmarshal(content, &v); err != nil {
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
	if err := json.Unmarshal(content, &v); err != nil {
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
		FulfilmentURL       *string `json:"fulfilment_url"`
		RequestTimeoutMs    *int    `json:"request_timeout_ms"`
		RetryBudget         *int    `json:"retry_budget"`
		CircuitErrThreshPct *int    `json:"circuit_error_threshold_pct"`
		CircuitMinRequests  *int    `json:"circuit_min_requests"`
	}
	if err := json.Unmarshal(content, &v); err != nil {
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
	if v.CircuitMinRequests == nil || *v.CircuitMinRequests < 1 {
		return fmt.Errorf("circuit_min_requests must be >= 1")
	}
	return nil
}

func validateReconTolerance(ctx context.Context, tx pgx.Tx, content json.RawMessage) error {
	var v struct {
		AmountToleranceMinor *int64 `json:"amount_tolerance_minor"`
		AutoResolve          *bool  `json:"auto_resolve"`
		BreakAgingAlertHours *int   `json:"break_aging_alert_hours"`
	}
	if err := json.Unmarshal(content, &v); err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	if v.AmountToleranceMinor == nil || *v.AmountToleranceMinor < 0 {
		return fmt.Errorf("amount_tolerance_minor must be >= 0")
	}
	if v.AutoResolve == nil {
		return fmt.Errorf("auto_resolve is required")
	}
	if *v.AutoResolve && *v.AmountToleranceMinor == 0 {
		return fmt.Errorf("auto_resolve with zero tolerance is a no-op that reads as a control — set a tolerance or disable auto_resolve")
	}
	if v.BreakAgingAlertHours == nil || *v.BreakAgingAlertHours < 1 {
		return fmt.Errorf("break_aging_alert_hours must be >= 1")
	}
	return nil
}
