package configsvc_test

// Phase 1 S2 — telco.recharge_feed validator + seeded global. Fail-closed:
// unsupported transport/auth, missing headers, out-of-range window/skew/body,
// bad currency, non-positive or inverted blast-radius clamps, and unknown
// fields are all refused; the seeded global default resolves and is DISABLED.

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// rechargeCfg builds a valid base config, applying overrides (a nil value
// deletes the key, to test "field absent").
func rechargeCfg(overrides map[string]any) string {
	base := map[string]any{
		"enabled": true, "transport": "webhook_push", "auth": "hmac_sha256",
		"key_id_header": "X-Bx-Key-Id", "signature_header": "X-Bx-Signature", "timestamp_header": "X-Bx-Timestamp",
		"replay_window_seconds": 120, "future_skew_seconds": 60, "max_body_bytes": 65536,
		"expected_currency": "NGN", "per_event_amount_max_minor": 50000000, "per_telco_daily_ceiling_minor": 50000000000,
	}
	for k, v := range overrides {
		if v == nil {
			delete(base, k)
		} else {
			base[k] = v
		}
	}
	b, _ := json.Marshal(base)
	return string(b)
}

func TestS2_RechargeFeedValidator(t *testing.T) {
	svc, _ := newSvc(t, "cfg_s2_recharge")
	scope := "telco:SIM_NG"

	reject := map[string]map[string]any{
		"enabled absent":          {"enabled": nil},
		"transport unsupported":   {"transport": "sftp_pull"},
		"auth unsupported":        {"auth": "mtls"},
		"key_id_header empty":     {"key_id_header": ""},
		"signature_header absent": {"signature_header": nil},
		"replay window too small": {"replay_window_seconds": 10},
		"replay window too big":   {"replay_window_seconds": 600},
		"future skew negative":    {"future_skew_seconds": -1},
		"max body too small":      {"max_body_bytes": 100},
		"max body too big":        {"max_body_bytes": 20_000_000},
		"bad currency":            {"expected_currency": "naira"},
		"per-event non-positive":  {"per_event_amount_max_minor": 0},
		"daily below per-event":   {"per_telco_daily_ceiling_minor": 1000, "per_event_amount_max_minor": 50_000_000},
		"unknown field":           {"foo": 1},
	}
	for label, ov := range reject {
		mustReject(t, svc, "telco.recharge_feed", scope, label, rechargeCfg(ov))
	}

	// A fully valid feed config approves.
	ctx := context.Background()
	c, err := svc.CreateDraft(ctx, "telco.recharge_feed", scope, "alice", "arm feed", []byte(rechargeCfg(nil)))
	if err != nil {
		t.Fatalf("draft: %v", err)
	}
	if err := svc.Submit(ctx, c.ConfigVersionID, "alice"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if err := svc.Approve(ctx, c.ConfigVersionID, "bob"); err != nil {
		t.Fatalf("valid recharge_feed must approve: %v", err)
	}
}

func TestS2_RechargeFeedGlobalSeed_DisabledAndRevalidates(t *testing.T) {
	svc, _ := newSvc(t, "cfg_s2_seed")
	ctx := context.Background()

	cv, err := svc.ActiveAt(ctx, "telco.recharge_feed", "global", time.Now().UTC())
	if err != nil {
		t.Fatalf("telco.recharge_feed global must be seeded ACTIVE: %v", err)
	}
	var s struct {
		Enabled   bool   `json:"enabled"`
		Transport string `json:"transport"`
		Auth      string `json:"auth"`
	}
	if err := json.Unmarshal(cv.Content, &s); err != nil {
		t.Fatal(err)
	}
	if s.Enabled {
		t.Fatal("seeded recharge_feed default must be DISABLED (arming is explicit)")
	}
	if s.Transport != "webhook_push" || s.Auth != "hmac_sha256" {
		t.Fatalf("seeded transport/auth unexpected: %+v", s)
	}

	// The shipped global seed (inserted directly, bypassing the validator) must
	// pass validateRechargeFeed against a fresh scope.
	c, err := svc.CreateDraft(ctx, "telco.recharge_feed", "telco:REVAL_PROBE", "alice", "revalidate seed", cv.Content)
	if err != nil {
		t.Fatalf("draft: %v", err)
	}
	if err := svc.Submit(ctx, c.ConfigVersionID, "alice"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if err := svc.Approve(ctx, c.ConfigVersionID, "bob"); err != nil {
		t.Fatalf("seeded global recharge_feed must pass its own validator: %v", err)
	}
}
