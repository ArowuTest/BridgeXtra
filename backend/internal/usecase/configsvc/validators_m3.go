package configsvc

// M3 domain validators (BUILD_PLAN §7c). Same contract as M1/M2: a value the
// code cannot honor is REJECTED at approval (SF-2 armed-but-dead prevention);
// safety controls carry zero-config floors — and per the G3 criteria, the
// guardrail re-arm and write-off maker-checker are NOT configurable off.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
)

func init() {
	validators["delinquency.buckets"] = validateDelinquencyBuckets
	validators["writeoff.policy"] = validateWriteoffPolicy
	validators["treasury.guardrails"] = validateTreasuryGuardrails
	validators["settlement.terms"] = validateSettlementTerms
}

// bucketCodes parses and validates the aging ladder, returning the set of
// codes for cross-domain checks (writeoff.policy references a bucket).
func bucketCodes(content json.RawMessage) (map[string]bool, error) {
	var v struct {
		Buckets []struct {
			Code           *string `json:"code"`
			MinDaysPastDue *int    `json:"min_days_past_due"`
		} `json:"buckets"`
		GraceDays *int `json:"grace_days"`
	}
	if err := strictUnmarshal(content, &v); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	if len(v.Buckets) < 2 {
		return nil, fmt.Errorf("buckets must include CURRENT plus at least one delinquency bucket")
	}
	seen := map[string]bool{}
	prev := -1
	for i, b := range v.Buckets {
		if b.Code == nil || *b.Code == "" {
			return nil, fmt.Errorf("buckets[%d]: code is required", i)
		}
		if seen[*b.Code] {
			return nil, fmt.Errorf("buckets[%d]: duplicate code %q", i, *b.Code)
		}
		seen[*b.Code] = true
		if b.MinDaysPastDue == nil || *b.MinDaysPastDue < 0 {
			return nil, fmt.Errorf("buckets[%d] (%s): min_days_past_due must be >= 0", i, *b.Code)
		}
		// Strictly ascending ladder: overlapping buckets would classify one
		// advance two ways — the aging query must have exactly one answer.
		if *b.MinDaysPastDue <= prev {
			return nil, fmt.Errorf("buckets[%d] (%s): ladder must be strictly ascending in min_days_past_due", i, *b.Code)
		}
		prev = *b.MinDaysPastDue
	}
	if v.Buckets[0].MinDaysPastDue == nil || *v.Buckets[0].MinDaysPastDue != 0 {
		return nil, fmt.Errorf("the first bucket must start at 0 days (a gap below the ladder leaves current advances unclassified)")
	}
	if v.GraceDays == nil || *v.GraceDays < 0 || *v.GraceDays > 30 {
		return nil, fmt.Errorf("grace_days is required and must be 0..30")
	}
	return seen, nil
}

func validateDelinquencyBuckets(ctx context.Context, tx pgx.Tx, content json.RawMessage) error {
	_, err := bucketCodes(content)
	return err
}

func validateWriteoffPolicy(ctx context.Context, tx pgx.Tx, content json.RawMessage) error {
	var v struct {
		MinBucket               *string `json:"min_bucket"`
		RequireDistinctApprover *bool   `json:"require_distinct_approver"`
		PostWriteoffRecovery    *string `json:"post_writeoff_recovery"`
	}
	if err := strictUnmarshal(content, &v); err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	if v.MinBucket == nil || *v.MinBucket == "" {
		return fmt.Errorf("min_bucket is required (the earliest bucket a write-off may start from)")
	}
	// Zero-config floor: maker-checker on loss crystallisation is NOT a
	// configuration — the schema enforces distinct approver regardless, so a
	// config claiming otherwise would be a lie about system behavior.
	if v.RequireDistinctApprover == nil || !*v.RequireDistinctApprover {
		return fmt.Errorf("require_distinct_approver must be true — write-off maker-checker is not configurable off (schema-enforced)")
	}
	if v.PostWriteoffRecovery == nil || *v.PostWriteoffRecovery != "RECOVERY_INCOME" {
		return fmt.Errorf("post_writeoff_recovery must be RECOVERY_INCOME (the only implemented policy — EDG-021)")
	}
	return nil
}

func validateTreasuryGuardrails(ctx context.Context, tx pgx.Tx, content json.RawMessage) error {
	var v struct {
		MaxDailyDisbursedMinor      *int64  `json:"max_daily_disbursed_minor"`
		MaxOpenExposureBpsCommitted *int64  `json:"max_open_exposure_bps_of_committed"`
		TripAction                  *string `json:"trip_action"`
		Rearm                       *string `json:"rearm"`
	}
	if err := strictUnmarshal(content, &v); err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	if v.MaxDailyDisbursedMinor == nil || *v.MaxDailyDisbursedMinor <= 0 {
		return fmt.Errorf("max_daily_disbursed_minor is required and must be > 0 (a zero cap halts lending; an absent cap is unlimited — both wrong)")
	}
	if v.MaxOpenExposureBpsCommitted == nil || *v.MaxOpenExposureBpsCommitted <= 0 || *v.MaxOpenExposureBpsCommitted > 10_000 {
		return fmt.Errorf("max_open_exposure_bps_of_committed must be in (0,10000]")
	}
	// G3 zero-config floors: the trip fails CLOSED and the re-arm requires
	// two humans. Neither is configurable away.
	if v.TripAction == nil || *v.TripAction != "SUSPEND_PROGRAMME" {
		return fmt.Errorf("trip_action must be SUSPEND_PROGRAMME — a guardrail that does not stop lending is armed-but-dead")
	}
	if v.Rearm == nil || *v.Rearm != "MAKER_CHECKER" {
		return fmt.Errorf("rearm must be MAKER_CHECKER — re-arming a tripped guardrail is a two-person decision, not configurable off")
	}
	return nil
}

func validateSettlementTerms(ctx context.Context, tx pgx.Tx, content json.RawMessage) error {
	var v struct {
		Cycle            *string `json:"cycle"`
		TelcoShareBps    *int64  `json:"telco_share_bps"`
		PlatformShareBps *int64  `json:"platform_share_bps"`
		Taxes            []struct {
			Code *string `json:"code"`
			Bps  *int64  `json:"bps"`
		} `json:"taxes"`
		ToleranceMinor *int64 `json:"tolerance_minor"`
	}
	if err := strictUnmarshal(content, &v); err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	if v.Cycle == nil || (*v.Cycle != "MONTHLY" && *v.Cycle != "WEEKLY") {
		return fmt.Errorf("cycle must be MONTHLY or WEEKLY")
	}
	if v.TelcoShareBps == nil || v.PlatformShareBps == nil ||
		*v.TelcoShareBps < 0 || *v.PlatformShareBps < 0 {
		return fmt.Errorf("telco_share_bps and platform_share_bps are required and must be >= 0")
	}
	// Shares must partition fee income EXACTLY — anything else silently
	// creates or destroys money at settlement (V2-LED-013 class).
	if *v.TelcoShareBps+*v.PlatformShareBps != 10_000 {
		return fmt.Errorf("telco_share_bps + platform_share_bps must equal 10000 exactly (got %d)", *v.TelcoShareBps+*v.PlatformShareBps)
	}
	seen := map[string]bool{}
	for i, t := range v.Taxes {
		if t.Code == nil || *t.Code == "" {
			return fmt.Errorf("taxes[%d]: code is required", i)
		}
		if seen[*t.Code] {
			return fmt.Errorf("taxes[%d]: duplicate code %q", i, *t.Code)
		}
		seen[*t.Code] = true
		if t.Bps == nil || *t.Bps < 0 || *t.Bps > 10_000 {
			return fmt.Errorf("taxes[%d] (%s): bps must be 0..10000", i, *t.Code)
		}
	}
	if v.ToleranceMinor == nil || *v.ToleranceMinor < 0 {
		return fmt.Errorf("tolerance_minor is required and must be >= 0")
	}
	return nil
}
