package main

// Held-recharge scenario for cmd/seed-dev (-held-recharges). It produces REAL rows in
// the Finance held-recharge queue the only sanctioned way — a signed webhook that the
// treasury guardrail holds back — never a direct held-row INSERT. Steps:
//   1. register a webhook credential (key_id -> telco -> the NAME of the secret env var),
//   2. activate telco.recharge_feed at global + telco scope with a tiny per-event clamp,
//   3. arm the RECOVERY layer LIVE (SetLive AND AdvanceFreshness — armed alone is not
//      live, so the webhook would 403 before the hold path),
//   4. post signed webhooks whose amount exceeds the clamp -> HTTP 202 {"status":"HELD"}.
//
// A running api is required: the handler verifies the HMAC against os.Getenv(secret_env)
// in the api process, so the SAME secret env var must be set on both this tool and the
// api. Event ids are deterministic, so a re-run is idempotent (Hold is
// ON CONFLICT(telco_id, source_event_id) DO NOTHING).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/rechargewebhook"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/simseed"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	devWHKeyID = "wh-dev-key"
	// #nosec G101 -- this is the NAME of the env var that holds the HMAC secret, not the
	// secret value. The value is read via os.Getenv at runtime and is never stored in
	// code or config (the webhook credential persists only this name).
	devSecretEnvDefault = "TCP_DEV_WH_SECRET"
	devPerEventMax      = int64(100)    // tiny clamp: any normal amount trips PER_EVENT_CLAMP
	devHeldAmountMinor  = int64(500000) // 5,000.00 NGN in minor units — well over the clamp
	devFreshnessSecs    = 172800        // 48h, matches the S3 arm-freshness window
)

func runHeldRecharges(ctx context.Context, adminPool *pgxpool.Pool) error {
	telco := simseed.SyntheticTelco // "SIM_NG"
	secretEnvName := envOr("TCP_SEED_WH_SECRET_ENV", devSecretEnvDefault)
	secret := os.Getenv(secretEnvName)
	if secret == "" {
		return fmt.Errorf("%s (the webhook HMAC secret value) must be set, and the api must have the SAME env var", secretEnvName)
	}
	apiURL := envOr("TCP_SEED_API_URL", "http://localhost:8090")
	count := heldCount()

	// 1. Webhook credential — the config stores the secret env var NAME, never the value.
	creds := &repo.WebhookCredentials{Pool: adminPool}
	if _, err := creds.ResolveByKeyID(ctx, devWHKeyID); errors.Is(err, repo.ErrWebhookCredentialNotFound) {
		if err := creds.Create(ctx, devWHKeyID, telco, secretEnvName, "seed-dev"); err != nil {
			return fmt.Errorf("webhook credential: %w", err)
		}
		log.Printf("seed-dev: webhook credential %s -> %s (secret env %s)", devWHKeyID, telco, secretEnvName)
	} else if err != nil {
		return fmt.Errorf("resolve webhook credential: %w", err)
	}

	// 2. Activate telco.recharge_feed at global + telco scope (kill-switch requires BOTH).
	cfg := configsvc.New(adminPool)
	for _, scope := range []string{"global", "telco:" + telco} {
		if err := activateRechargeFeed(ctx, adminPool, cfg, scope); err != nil {
			return fmt.Errorf("activate recharge_feed %s: %w", scope, err)
		}
	}

	// 3. Arm RECOVERY LIVE — SetLive alone is not enough (IsLayerLive also requires a
	// fresh last_recon_at), so advance freshness too, or the webhook 403s before the hold.
	arm := &repo.ReconArming{Pool: adminPool}
	if err := arm.SetLive(ctx, telco, repo.ReconLayerRecovery); err != nil {
		return fmt.Errorf("arm recovery live: %w", err)
	}
	if _, err := arm.AdvanceFreshness(ctx, telco, repo.ReconLayerRecovery, devFreshnessSecs); err != nil {
		return fmt.Errorf("advance recovery freshness: %w", err)
	}

	// 4. Post signed webhooks over the clamp -> HELD rows.
	client := &http.Client{Timeout: 15 * time.Second}
	held := 0
	for i := 0; i < count; i++ {
		if err := postHeldWebhook(ctx, client, apiURL, telco, secret, i); err != nil {
			return err
		}
		held++
	}
	log.Printf("seed-dev: held recharges — %d posted, all HELD (reason PER_EVENT_CLAMP), telco %s", held, telco)
	log.Print("seed-dev: held-recharge queue populated. Open the portal → Held recharges.")
	return nil
}

// activateRechargeFeed drives the real config maker-checker lifecycle (distinct actors)
// to an ACTIVE telco.recharge_feed at scope. Idempotent: if one is already ACTIVE it is
// left as-is (so repeated runs don't pile up config versions).
func activateRechargeFeed(ctx context.Context, adminPool *pgxpool.Pool, cfg *configsvc.Service, scope string) error {
	// Skip only if an ENABLED feed is already active at this scope. The migrations seed
	// global as enabled=false (the prod-safe default), so a coarse "is ACTIVE" check
	// would leave the kill-switch off and every webhook would 403 RECHARGE_FEED_DISABLED
	// (the kill-switch requires enabled=true at BOTH global and telco scope).
	var enabledActive bool
	if err := adminPool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM config_versions
		   WHERE domain='telco.recharge_feed' AND scope=$1 AND state='ACTIVE'
		     AND (content->>'enabled')::boolean IS TRUE)`,
		scope).Scan(&enabledActive); err != nil {
		return fmt.Errorf("probe active config: %w", err)
	}
	if enabledActive {
		return nil
	}
	content := fmt.Sprintf(`{"enabled":true,"transport":"webhook_push","auth":"hmac_sha256",`+
		`"key_id_header":"X-Bx-Key-Id","signature_header":"X-Bx-Signature","timestamp_header":"X-Bx-Timestamp",`+
		`"replay_window_seconds":120,"future_skew_seconds":60,"max_body_bytes":65536,"expected_currency":"NGN",`+
		`"per_event_amount_max_minor":%d,"per_telco_daily_ceiling_minor":50000000000,`+
		`"recovery_max_backdate_seconds":1209600,"recovery_max_future_skew_seconds":60}`, devPerEventMax)
	cv, err := cfg.CreateDraft(ctx, "telco.recharge_feed", scope, "seed-dev-maker", "seed-dev held-recharge scenario", json.RawMessage(content))
	if err != nil {
		return fmt.Errorf("draft: %w", err)
	}
	if err := cfg.Submit(ctx, cv.ConfigVersionID, "seed-dev-maker"); err != nil {
		return fmt.Errorf("submit: %w", err)
	}
	if err := cfg.Approve(ctx, cv.ConfigVersionID, "seed-dev-checker"); err != nil {
		return fmt.Errorf("approve: %w", err)
	}
	if err := cfg.Activate(ctx, cv.ConfigVersionID, "seed-dev-checker", time.Now().UTC()); err != nil {
		return fmt.Errorf("activate: %w", err)
	}
	return nil
}

// postHeldWebhook signs and posts one recharge webhook whose amount exceeds the clamp,
// asserting the handler HELD it (202 {"status":"HELD"}) rather than ingesting it.
func postHeldWebhook(ctx context.Context, client *http.Client, apiURL, telco, secret string, i int) error {
	eventID := fmt.Sprintf("wh-dev-held-%d", i)
	body := fmt.Sprintf(`{"event_id":%q,"msisdn_token":%q,"amount_minor":%d,"currency":"NGN","occurred_at":%q}`,
		eventID, fmt.Sprintf("tok_sim_held_%03d", i), devHeldAmountMinor, time.Now().UTC().Format(time.RFC3339))
	ts := strconv.FormatInt(time.Now().UTC().Unix(), 10)
	sig := rechargewebhook.Sign(rechargewebhook.NewHMACSHA256Adapter(), []byte(secret), devWHKeyID, ts, []byte(body))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		apiURL+"/v1/telcos/"+telco+"/recharge-webhook", bytes.NewReader([]byte(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Bx-Key-Id", devWHKeyID)
	req.Header.Set("X-Bx-Timestamp", ts)
	req.Header.Set("X-Bx-Signature", sig)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("post webhook %s: %w", eventID, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("webhook %s: expected 202, got %d: %s", eventID, resp.StatusCode, string(b))
	}
	var r struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal(b, &r)
	if r.Status != "HELD" {
		return fmt.Errorf("webhook %s: expected status HELD, got %q (body %s)", eventID, r.Status, string(b))
	}
	return nil
}

func heldCount() int {
	if v := os.Getenv("TCP_SEED_HELD_COUNT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 4
}
