package handler

// R-P0-8 inbound rate limiting. The limiter is loaded from governed config at
// boot (LoadRateLimiter); the API refuses to start if it is absent, so there
// is never an unlimited running surface. Applied to portal /login (by client
// IP) and the telco channel API (by credential where present, else IP).

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform/ratelimit"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
)

// LoadRateLimiter reads the governed platform.ratelimit config and builds the
// limiter. A missing or invalid config is a fatal boot error (fail-closed).
func LoadRateLimiter(ctx context.Context, cfg *configsvc.Service) (*ratelimit.Limiter, error) {
	cv, err := cfg.ActiveAt(ctx, "platform.ratelimit", "global", time.Now().UTC())
	if err != nil {
		return nil, fmt.Errorf("platform.ratelimit config (required at boot): %w", err)
	}
	var raw struct {
		Surfaces map[string]struct {
			RequestsPerMinute float64 `json:"requests_per_minute"`
			Burst             float64 `json:"burst"`
		} `json:"surfaces"`
	}
	if err := json.Unmarshal(cv.Content, &raw); err != nil {
		return nil, fmt.Errorf("platform.ratelimit parse: %w", err)
	}
	limits := make(map[string]ratelimit.Limit, len(raw.Surfaces))
	for name, s := range raw.Surfaces {
		limits[name] = ratelimit.Limit{RatePerMinute: s.RequestsPerMinute, Burst: s.Burst}
	}
	if _, ok := limits["login"]; !ok {
		return nil, fmt.Errorf("platform.ratelimit missing required surface 'login'")
	}
	if _, ok := limits["channel"]; !ok {
		return nil, fmt.Errorf("platform.ratelimit missing required surface 'channel'")
	}
	return ratelimit.New(limits), nil
}

// clientIP extracts the peer IP without the port. Proxy-aware client-IP
// derivation (trusted X-Forwarded-For) is a separate hardening (R-P2-7).
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// rateLimited wraps a handler with the limiter under a scope, keyed by keyFn.
// A refused request is 429 with Retry-After — no business logic runs.
func rateLimited(l *ratelimit.Limiter, scope string, keyFn func(*http.Request) string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !l.Allow(scope, keyFn(r)) {
			w.Header().Set("Retry-After", "60")
			writeErr(w, http.StatusTooManyRequests, "RATE_LIMITED", "too many requests; slow down and retry shortly")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// channelRateKey keys the channel limiter by the telco credential (the api key
// header) when present, so one noisy telco cannot exhaust another's budget;
// falls back to client IP for unauthenticated hammering.
func channelRateKey(r *http.Request) string {
	if k := r.Header.Get(headerAPIKey); k != "" {
		return "cred:" + k
	}
	return "ip:" + clientIP(r)
}
