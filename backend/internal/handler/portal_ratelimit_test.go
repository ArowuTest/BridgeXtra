package handler_test

// R-P0-8: the portal /login is rate-limited. With a tight limiter the burst is
// admitted (even as failed logins) and the next request is refused 429 —
// before any credential check, blunting credential-stuffing.

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/handler"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform/ratelimit"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
)

func TestRP08_Login_RateLimited(t *testing.T) {
	db := testutil.MustSetup(t, "rp08_login")
	p := &handler.Portal{
		Admins:   &repo.Admins{Pool: db.App},
		Sessions: &repo.PortalSessions{Pool: db.App},
		Config:   configsvc.New(db.Worker),
		ReadPool: db.Worker,
		// Tight: 3-request burst so the 4th is refused deterministically.
		Limiter: ratelimit.New(map[string]ratelimit.Limit{
			"login":   {RatePerMinute: 0.001, Burst: 3},
			"channel": {RatePerMinute: 0.001, Burst: 3},
		}),
		Log: slog.Default(),
	}
	mux := http.NewServeMux()
	p.Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	post := func() int {
		body, _ := json.Marshal(map[string]string{"api_key": "wrong-key-000000"})
		resp, err := http.Post(srv.URL+"/v1/portal/login", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	// The burst of 3 is admitted (they 401 on the bad key — auth still runs).
	for i := 0; i < 3; i++ {
		if code := post(); code == http.StatusTooManyRequests {
			t.Fatalf("request %d within burst must not be rate-limited, got 429", i)
		}
	}
	// The 4th is refused by the limiter, before the credential check.
	if code := post(); code != http.StatusTooManyRequests {
		t.Fatalf("the 4th login within the window must be 429, got %d", code)
	}
}
