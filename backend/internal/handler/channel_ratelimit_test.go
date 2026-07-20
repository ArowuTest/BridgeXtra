package handler_test

// R-P0-8a-F1 (reviewer): the adversarial COMPLEMENT of per-key isolation. A
// rotating-invalid-key flood must NOT get a fresh bucket per key — the
// pre-auth channel_ip throttle puts the whole flood in one IP bucket and
// refuses it, before any DB credential lookup.

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/handler"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform/ratelimit"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
)

func TestRP08F1_RotatingInvalidKeys_StillThrottledByIP(t *testing.T) {
	db := testutil.MustSetup(t, "rp08f1_rot")
	db.SeedTelco(t, "SIM_NG", "unused")
	telcos := &repo.Telcos{Pool: db.App}
	auth := &handler.TenantAuth{Telcos: telcos, Pool: db.App, Log: slog.Default()}

	mux := http.NewServeMux()
	// Tight channel_ip burst of 5; generous per-telco (irrelevant — the flood
	// never authenticates). TrustedProxyCount 0 → key on RemoteAddr (all the
	// httptest requests share 127.0.0.1, exactly one attacker IP).
	ch := &handler.Channel{
		Origination: nil, Recovery: nil, TrustedProxyCount: 0, Log: slog.Default(),
		Limiter: ratelimit.New(map[string]ratelimit.Limit{
			"login":      {RatePerMinute: 0.001, Burst: 5},
			"channel":    {RatePerMinute: 1e9, Burst: 1e9},
			"channel_ip": {RatePerMinute: 0.001, Burst: 5},
		}),
	}
	ch.Mount(mux, auth)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// Each request presents a DIFFERENT invalid api key — the v1 bypass.
	got429 := false
	for i := 0; i < 30; i++ {
		req, _ := http.NewRequest("POST", srv.URL+"/v1/recovery/events", http.NoBody)
		req.Header.Set("X-Api-Key", "forged-key-"+strconv.Itoa(i))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			got429 = true
			break
		}
	}
	if !got429 {
		t.Fatal("a rotating-invalid-key flood from one IP must be throttled by the pre-auth channel_ip bucket")
	}
}
