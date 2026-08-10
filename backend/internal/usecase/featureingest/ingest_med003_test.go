package featureingest

// BX-MED-003: the feature-feed fetch bounds the in-memory buffer with a GOVERNED ceiling and
// refuses an oversized body EXPLICITLY, rather than the old fixed 512MiB LimitReader that silently
// truncated (a confusing parse error, or — absent the HIGH-006 control total — a silent partial).

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
)

type fakeFetcher struct {
	body   []byte
	status int
}

func (f fakeFetcher) AuthenticatedGet(_ context.Context, _, _ string) (*http.Response, error) {
	return &http.Response{StatusCode: f.status, Body: io.NopCloser(bytes.NewReader(f.body))}, nil
}

// activateAdapterWithFeedCeiling activates a SIM_NG telco.adapter config carrying feature_feed_max_bytes.
func activateAdapterWithFeedCeiling(t *testing.T, db *testutil.DB, maxBytes int64) {
	t.Helper()
	cfg := configsvc.New(db.Worker)
	ctx := context.Background()
	content := fmt.Sprintf(`{"fulfilment_url":"http://sim.invalid","request_timeout_ms":3000,"retry_budget":0,"circuit_error_threshold_pct":50,"circuit_min_requests":20,"circuit_cooldown_seconds":30,"max_weekly_recharge_minor":100000000,"feature_feed_max_bytes":%d}`, maxBytes)
	c, err := cfg.CreateDraft(ctx, "telco.adapter", "telco:SIM_NG", "alice", "med003", []byte(content))
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []func() error{
		func() error { return cfg.Submit(ctx, c.ConfigVersionID, "alice") },
		func() error { return cfg.Approve(ctx, c.ConfigVersionID, "bob") },
		func() error { return cfg.Activate(ctx, c.ConfigVersionID, "bob", time.Now().UTC()) },
	} {
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBXMED003_FeedBufferCeiling_RefusesOversizedBody(t *testing.T) {
	const maxBytes = 1 << 20 // 1MiB, the validator floor — small enough to test cheaply
	db := testutil.MustSetup(t, "med003_over")
	activateAdapterWithFeedCeiling(t, db, maxBytes)
	// A body ONE byte over the governed ceiling. Load-bearing (BX-MED-003): it is refused
	// explicitly, not silently truncated. Mutation proof: drop the `len(raw) > maxBytes` check in
	// fetch and this returns a nil error with truncated bytes — RED.
	over := bytes.Repeat([]byte("x"), maxBytes+1)
	svc := New(db.App, configsvc.New(db.App), fakeFetcher{body: over, status: http.StatusOK}, slog.Default())
	if _, err := svc.fetch(context.Background(), "SIM_NG"); err == nil || !strings.Contains(err.Error(), "BX-MED-003") {
		t.Fatalf("an oversized feed must be refused with the BX-MED-003 ceiling error, got %v", err)
	}
}

func TestBXMED003_FeedBufferCeiling_AllowsWithinLimit(t *testing.T) {
	const maxBytes = 1 << 20
	db := testutil.MustSetup(t, "med003_ok")
	activateAdapterWithFeedCeiling(t, db, maxBytes)
	body := []byte(`{"telco_id":"SIM_NG","rows":[]}`) // well within the ceiling
	svc := New(db.App, configsvc.New(db.App), fakeFetcher{body: body, status: http.StatusOK}, slog.Default())
	raw, err := svc.fetch(context.Background(), "SIM_NG")
	if err != nil {
		t.Fatalf("a within-limit feed must fetch cleanly, got %v", err)
	}
	if !bytes.Equal(raw, body) {
		t.Fatalf("fetch must return the body unchanged, got %d bytes", len(raw))
	}
}

// The governed knob is bounded: a ceiling below 1MiB (here 100 bytes) is refused by the validator,
// so an operator cannot neuter the protection or set an unusable value.
func TestBXMED003_Validator_RejectsOutOfBoundsCeiling(t *testing.T) {
	db := testutil.MustSetup(t, "med003_val")
	cfg := configsvc.New(db.Worker)
	ctx := context.Background()
	content := `{"fulfilment_url":"http://sim.invalid","request_timeout_ms":3000,"retry_budget":0,"circuit_error_threshold_pct":50,"circuit_min_requests":20,"circuit_cooldown_seconds":30,"max_weekly_recharge_minor":100000000,"feature_feed_max_bytes":100}`
	c, err := cfg.CreateDraft(ctx, "telco.adapter", "telco:SIM_NG", "alice", "bad", []byte(content))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Submit(ctx, c.ConfigVersionID, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Approve(ctx, c.ConfigVersionID, "bob"); err == nil || !strings.Contains(err.Error(), "feature_feed_max_bytes") {
		t.Fatalf("feature_feed_max_bytes below 1MiB must be rejected by the validator, got %v", err)
	}
}
