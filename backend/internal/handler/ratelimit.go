package handler

// R-P0-8 inbound rate limiting + R-P0-8a-F1 (reviewer) + R-P2-7 client-IP.
//
// The limiter is loaded from governed config at boot (LoadRateLimiter); the
// API refuses to start if it is absent, so no surface ever runs unlimited.
//
// F1 fix — the channel surface has TWO limits, not one:
//   - channel_ip (PRE-auth, keyed by real client IP): the security backstop.
//     A rotating-invalid-key flood never resolves a telco, so a per-credential
//     bucket would give each forged key a fresh bucket and never throttle it.
//     Keying the pre-auth throttle on the client IP puts the whole flood in
//     ONE bucket regardless of the key.
//   - channel (POST-auth, keyed by the VALIDATED telco): per-telco fairness so
//     one busy telco cannot exhaust another's budget. Applied only after the
//     credential resolves, so a forged key can never mint a bucket here.
//
// R-P2-7 — the client IP is derived through the trusted proxy chain
// (trusted_proxy_count). Behind Render's LB, RemoteAddr is the proxy for every
// client, so IP-keying without this collapses to a single global bucket.

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform/ratelimit"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
)

// LoadRateLimiter reads platform.ratelimit and builds the limiter plus the
// trusted-proxy count. Missing/invalid config is a fatal boot error.
func LoadRateLimiter(ctx context.Context, cfg *configsvc.Service) (*ratelimit.Limiter, int, error) {
	cv, err := cfg.ActiveAt(ctx, "platform.ratelimit", "global", time.Now().UTC())
	if err != nil {
		return nil, 0, fmt.Errorf("platform.ratelimit config (required at boot): %w", err)
	}
	var raw struct {
		TrustedProxyCount int `json:"trusted_proxy_count"`
		Surfaces          map[string]struct {
			RequestsPerMinute float64 `json:"requests_per_minute"`
			Burst             float64 `json:"burst"`
		} `json:"surfaces"`
	}
	if err := json.Unmarshal(cv.Content, &raw); err != nil {
		return nil, 0, fmt.Errorf("platform.ratelimit parse: %w", err)
	}
	limits := make(map[string]ratelimit.Limit, len(raw.Surfaces))
	for name, s := range raw.Surfaces {
		limits[name] = ratelimit.Limit{RatePerMinute: s.RequestsPerMinute, Burst: s.Burst}
	}
	for _, req := range []string{"login", "channel", "channel_ip"} {
		if _, ok := limits[req]; !ok {
			return nil, 0, fmt.Errorf("platform.ratelimit missing required surface %q", req)
		}
	}
	if raw.TrustedProxyCount < 0 {
		return nil, 0, fmt.Errorf("platform.ratelimit trusted_proxy_count must be >= 0")
	}
	return ratelimit.New(limits), raw.TrustedProxyCount, nil
}

// clientIP derives the real client IP through the trusted proxy chain (R-P2-7).
// trustedHops counts our own proxies (RemoteAddr is the nearest, counted as
// one). We strip trustedHops-1 entries from the right of X-Forwarded-For; the
// rightmost remaining entry is the first untrusted hop — the real client. XFF
// is only consulted when we trust at least one proxy, so a direct client can
// never spoof it.
func clientIP(r *http.Request, trustedHops int) string {
	remote := hostOnly(r.RemoteAddr)
	if trustedHops <= 0 {
		return remote
	}
	xff := r.Header.Get("X-Forwarded-For")
	if xff == "" {
		return remote
	}
	parts := strings.Split(xff, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	idx := len(parts) - (trustedHops - 1) - 1
	if idx < 0 || idx >= len(parts) || parts[idx] == "" {
		return remote
	}
	return parts[idx]
}

func hostOnly(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}

// rateLimited wraps a handler under a scope keyed by keyFn. A refused request
// is 429 + Retry-After; no downstream logic runs.
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

// perTelcoRateLimit is the POST-auth channel fairness throttle. It keys on the
// VALIDATED telco from context (set by TenantAuth), so a forged credential —
// which never resolves a telco — can never reach or mint a bucket here.
func perTelcoRateLimit(l *ratelimit.Limiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		telcoID, err := platform.TenantFrom(r.Context())
		if err != nil || telcoID == "" {
			// No resolved tenant post-auth would be a wiring bug; fail safe by
			// refusing rather than bypassing the fairness control.
			writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
			return
		}
		if !l.Allow("channel", "telco:"+telcoID) {
			w.Header().Set("Retry-After", "60")
			writeErr(w, http.StatusTooManyRequests, "RATE_LIMITED", "too many requests for this telco; slow down")
			return
		}
		next.ServeHTTP(w, r)
	})
}
