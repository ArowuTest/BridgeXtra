package configsvc

// Phase 1 S2 — telco.recharge_feed validator (inbound MNO recharge webhook).
// Fail-closed and no-hardcoding: every knob is required and range-checked at
// config-write, unsupported transports/auth schemes are refused (like S1 mtls),
// and the freshness window is clamped so a typo can neither disable the feed
// (0s) nor open a long replay horizon. Secrets are NOT here — the HMAC secret
// lives in telco_webhook_credentials.secret_env (an env-var name), unique per
// credential by DB index.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
)

func init() {
	validators["telco.recharge_feed"] = validateRechargeFeed
}

func validateRechargeFeed(_ context.Context, _ pgx.Tx, content json.RawMessage) error {
	var v struct {
		Enabled                   *bool   `json:"enabled"`
		Transport                 *string `json:"transport"`
		Auth                      *string `json:"auth"`
		KeyIDHeader               *string `json:"key_id_header"`
		SignatureHeader           *string `json:"signature_header"`
		TimestampHeader           *string `json:"timestamp_header"`
		ReplayWindowSeconds       *int    `json:"replay_window_seconds"`
		FutureSkewSeconds         *int    `json:"future_skew_seconds"`
		MaxBodyBytes              *int    `json:"max_body_bytes"`
		ExpectedCurrency          *string `json:"expected_currency"`
		PerEventAmountMaxMinor    *int64  `json:"per_event_amount_max_minor"`
		PerTelcoDailyCeilingMinor *int64  `json:"per_telco_daily_ceiling_minor"`
	}
	if err := strictUnmarshal(content, &v); err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	if v.Enabled == nil {
		return fmt.Errorf("telco.recharge_feed: enabled required (bool) — arming must be explicit")
	}
	// Only the transports/auth schemes the handler actually implements may arm
	// (unsupported => reject, never armed-but-dead).
	if v.Transport == nil || *v.Transport != "webhook_push" {
		return fmt.Errorf("telco.recharge_feed: transport must be \"webhook_push\" (only supported transport)")
	}
	if v.Auth == nil || *v.Auth != "hmac_sha256" {
		return fmt.Errorf("telco.recharge_feed: auth must be \"hmac_sha256\" (only supported inbound auth)")
	}
	for name, h := range map[string]*string{
		"key_id_header": v.KeyIDHeader, "signature_header": v.SignatureHeader, "timestamp_header": v.TimestampHeader,
	} {
		if h == nil || *h == "" {
			return fmt.Errorf("telco.recharge_feed: %s is required (non-empty header name)", name)
		}
	}
	// Freshness window: clamped so 0 can't stall the feed and a huge value can't
	// open a long replay horizon.
	if v.ReplayWindowSeconds == nil || *v.ReplayWindowSeconds < 30 || *v.ReplayWindowSeconds > 300 {
		return fmt.Errorf("telco.recharge_feed: replay_window_seconds must be 30..300")
	}
	if v.FutureSkewSeconds == nil || *v.FutureSkewSeconds < 0 || *v.FutureSkewSeconds > 300 {
		return fmt.Errorf("telco.recharge_feed: future_skew_seconds must be 0..300")
	}
	if v.MaxBodyBytes == nil || *v.MaxBodyBytes < 1024 || *v.MaxBodyBytes > 10_485_760 {
		return fmt.Errorf("telco.recharge_feed: max_body_bytes must be 1024..10485760 (1KiB..10MiB)")
	}
	if v.ExpectedCurrency == nil || !isISO4217(*v.ExpectedCurrency) {
		return fmt.Errorf("telco.recharge_feed: expected_currency must be a 3-letter ISO-4217 code")
	}
	if v.PerEventAmountMaxMinor == nil || *v.PerEventAmountMaxMinor <= 0 || *v.PerEventAmountMaxMinor > 922_000_000_000_000 {
		return fmt.Errorf("telco.recharge_feed: per_event_amount_max_minor must be in (0, 922e12] — the single-recharge blast-radius clamp")
	}
	if v.PerTelcoDailyCeilingMinor == nil || *v.PerTelcoDailyCeilingMinor <= 0 || *v.PerTelcoDailyCeilingMinor > 922_000_000_000_000 {
		return fmt.Errorf("telco.recharge_feed: per_telco_daily_ceiling_minor must be in (0, 922e12] — the daily blast-radius clamp")
	}
	if *v.PerTelcoDailyCeilingMinor < *v.PerEventAmountMaxMinor {
		return fmt.Errorf("telco.recharge_feed: per_telco_daily_ceiling_minor must be >= per_event_amount_max_minor")
	}
	return nil
}

// isISO4217 does a structural check: exactly three ASCII uppercase letters. The
// authoritative currency of record is entity.Currency at ingest; this only keeps
// a malformed code out of the config.
func isISO4217(s string) bool {
	if len(s) != 3 {
		return false
	}
	for _, r := range s {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}
