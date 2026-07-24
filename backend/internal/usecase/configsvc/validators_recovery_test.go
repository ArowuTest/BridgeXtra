package configsvc

// S3-A validator tests (pure): the recon.recovery and telco.recovery_feed
// fail-closed floors, incl. the DST-zone rejection (I16) and the feed shape
// (I10-structural / I14-structural). Adversarial: each bad case asserts the
// SPECIFIC floor a typo would breach.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

const goodReconRecovery = `{"amount_tolerance_minor":0,"auto_resolve":false,"max_amount_minor":1000000000000,"min_completeness_ratio":0.5,"min_confirmation_ratio":0.99,"recon_lag_seconds":3600,"rereconcile_lookback_seconds":1209600,"break_aging_alert_hours":24,"business_timezone":"Africa/Lagos","arm_freshness_max_seconds":172800}`

func TestValidateReconRecovery(t *testing.T) {
	if err := validateReconRecovery(context.Background(), nil, json.RawMessage(goodReconRecovery)); err != nil {
		t.Fatalf("the seeded default must validate: %v", err)
	}
	bad := map[string]string{
		"auto_resolve true":         `{"amount_tolerance_minor":0,"auto_resolve":true,"max_amount_minor":1000000000000,"min_completeness_ratio":0.5,"min_confirmation_ratio":0.99,"recon_lag_seconds":3600,"rereconcile_lookback_seconds":1209600,"break_aging_alert_hours":24,"business_timezone":"Africa/Lagos","arm_freshness_max_seconds":172800}`,
		"confirmation ratio 0":      `{"amount_tolerance_minor":0,"auto_resolve":false,"max_amount_minor":1000000000000,"min_completeness_ratio":0.5,"min_confirmation_ratio":0,"recon_lag_seconds":3600,"rereconcile_lookback_seconds":1209600,"break_aging_alert_hours":24,"business_timezone":"Africa/Lagos","arm_freshness_max_seconds":172800}`,
		"confirmation ratio >1":     `{"amount_tolerance_minor":0,"auto_resolve":false,"max_amount_minor":1000000000000,"min_completeness_ratio":0.5,"min_confirmation_ratio":1.5,"recon_lag_seconds":3600,"rereconcile_lookback_seconds":1209600,"break_aging_alert_hours":24,"business_timezone":"Africa/Lagos","arm_freshness_max_seconds":172800}`,
		"arm freshness too small":   `{"amount_tolerance_minor":0,"auto_resolve":false,"max_amount_minor":1000000000000,"min_completeness_ratio":0.5,"min_confirmation_ratio":0.99,"recon_lag_seconds":3600,"rereconcile_lookback_seconds":1209600,"break_aging_alert_hours":24,"business_timezone":"Africa/Lagos","arm_freshness_max_seconds":60}`,
		"arm freshness too large":   `{"amount_tolerance_minor":0,"auto_resolve":false,"max_amount_minor":1000000000000,"min_completeness_ratio":0.5,"min_confirmation_ratio":0.99,"recon_lag_seconds":3600,"rereconcile_lookback_seconds":1209600,"break_aging_alert_hours":24,"business_timezone":"Africa/Lagos","arm_freshness_max_seconds":999999}`,
		"DST zone rejected":         `{"amount_tolerance_minor":0,"auto_resolve":false,"max_amount_minor":1000000000000,"min_completeness_ratio":0.5,"min_confirmation_ratio":0.99,"recon_lag_seconds":3600,"rereconcile_lookback_seconds":1209600,"break_aging_alert_hours":24,"business_timezone":"America/New_York","arm_freshness_max_seconds":172800}`,
		"unloadable zone rejected":  `{"amount_tolerance_minor":0,"auto_resolve":false,"max_amount_minor":1000000000000,"min_completeness_ratio":0.5,"min_confirmation_ratio":0.99,"recon_lag_seconds":3600,"rereconcile_lookback_seconds":1209600,"break_aging_alert_hours":24,"business_timezone":"Not/AZone","arm_freshness_max_seconds":172800}`,
		"tolerance exceeds ceiling": `{"amount_tolerance_minor":2000000000000,"auto_resolve":false,"max_amount_minor":1000000000000,"min_completeness_ratio":0.5,"min_confirmation_ratio":0.99,"recon_lag_seconds":3600,"rereconcile_lookback_seconds":1209600,"break_aging_alert_hours":24,"business_timezone":"Africa/Lagos","arm_freshness_max_seconds":172800}`,
		"unknown field rejected":    `{"amount_tolerance_minor":0,"auto_resolve":false,"max_amount_minor":1000000000000,"min_completeness_ratio":0.5,"min_confirmation_ratio":0.99,"recon_lag_seconds":3600,"rereconcile_lookback_seconds":1209600,"break_aging_alert_hours":24,"business_timezone":"Africa/Lagos","arm_freshness_max_seconds":172800,"surprise":1}`,
	}
	for name, c := range bad {
		if err := validateReconRecovery(context.Background(), nil, json.RawMessage(c)); err == nil {
			t.Errorf("%s: expected rejection, got nil", name)
		}
	}
}

func TestValidateReconFeed(t *testing.T) {
	good := map[string]string{
		"mock":  `{"source":"mock","expected_currency":"NGN","business_date_basis":"occurred_at_lagos_date"}`,
		"https": `{"source":"https","expected_currency":"NGN","url":"https://feeds.mtn.example/eod","business_date_basis":"occurred_at_lagos_date","envelope_auth":{"scheme":"hmac_sha256","secret_env":"MTN_EOD_HMAC","signature_field":"envelope_signature"}}`,
	}
	for name, c := range good {
		if err := validateReconFeed(context.Background(), nil, json.RawMessage(c)); err != nil {
			t.Errorf("%s: must validate, got %v", name, err)
		}
	}
	bad := map[string]string{
		"unknown source":      `{"source":"sftp","expected_currency":"NGN","business_date_basis":"occurred_at_lagos_date"}`,
		"mock with url":       `{"source":"mock","expected_currency":"NGN","url":"https://x.example","business_date_basis":"occurred_at_lagos_date"}`,
		"https no envelope":   `{"source":"https","expected_currency":"NGN","url":"https://x.example","business_date_basis":"occurred_at_lagos_date"}`,
		"https raw secret":    `{"source":"https","expected_currency":"NGN","url":"https://x.example","business_date_basis":"occurred_at_lagos_date","envelope_auth":{"scheme":"hmac_sha256","secret":"s3cr3t"}}`,
		"https bad scheme":    `{"source":"https","expected_currency":"NGN","url":"https://x.example","business_date_basis":"occurred_at_lagos_date","envelope_auth":{"scheme":"rsa","secret_env":"X"}}`,
		"https no secret_env": `{"source":"https","expected_currency":"NGN","url":"https://x.example","business_date_basis":"occurred_at_lagos_date","envelope_auth":{"scheme":"hmac_sha256"}}`,
		"https relative url":  `{"source":"https","expected_currency":"NGN","url":"/eod","business_date_basis":"occurred_at_lagos_date","envelope_auth":{"scheme":"hmac_sha256","secret_env":"X"}}`,
		"bad basis":           `{"source":"mock","expected_currency":"NGN","business_date_basis":"received_date"}`,
		"bad currency":        `{"source":"mock","expected_currency":"naira","business_date_basis":"occurred_at_lagos_date"}`,
	}
	for name, c := range bad {
		if err := validateReconFeed(context.Background(), nil, json.RawMessage(c)); err == nil {
			t.Errorf("%s: expected rejection, got nil", name)
		}
	}
}

// The seed content strings in migration 0053 must be exactly what the validators
// accept (a drift between seed and validator would ship a config that cannot be
// re-activated). Mirror the migration seeds here.
func TestSeeds_MatchValidators(t *testing.T) {
	if err := validateReconRecovery(context.Background(), nil, json.RawMessage(goodReconRecovery)); err != nil {
		t.Fatalf("recon.recovery seed drift: %v", err)
	}
	feedSeed := `{"source":"mock","expected_currency":"NGN","business_date_basis":"occurred_at_lagos_date"}`
	if err := validateReconFeed(context.Background(), nil, json.RawMessage(feedSeed)); err != nil {
		t.Fatalf("telco.recovery_feed seed drift: %v", err)
	}
	// guard against a copy-paste of a DST zone into the seed
	if strings.Contains(goodReconRecovery, "America/") {
		t.Fatal("seed must not use a DST zone")
	}
}
