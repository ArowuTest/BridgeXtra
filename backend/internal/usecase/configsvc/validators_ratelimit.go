package configsvc

// R-P0-8: the inbound rate-limit thresholds are governed config (no
// hardcoding). Every named surface must carry a positive sustained rate and
// burst; an empty set is refused (a rate limiter that permits everything is
// not a control). The consumer additionally requires the login + channel
// surfaces to be present, and fails to BOOT if this config is absent.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
)

func init() {
	validators["platform.ratelimit"] = validateRateLimit
}

func validateRateLimit(_ context.Context, _ pgx.Tx, content json.RawMessage) error {
	var v struct {
		TrustedProxyCount *int `json:"trusted_proxy_count"`
		Surfaces          map[string]struct {
			RequestsPerMinute *float64 `json:"requests_per_minute"`
			Burst             *float64 `json:"burst"`
		} `json:"surfaces"`
	}
	if err := strictUnmarshal(content, &v); err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	// R-P2-7: how many proxies in front of us to trust for X-Forwarded-For.
	// Required and non-negative — an absent value would silently pick between
	// "trust nothing" and "trust the client", and IP-keying depends on it.
	if v.TrustedProxyCount == nil || *v.TrustedProxyCount < 0 {
		return fmt.Errorf("trusted_proxy_count is required and must be >= 0 (R-P2-7)")
	}
	if len(v.Surfaces) == 0 {
		return fmt.Errorf("surfaces must be non-empty — an unlimited limiter is not a control")
	}
	for name, s := range v.Surfaces {
		if s.RequestsPerMinute == nil || *s.RequestsPerMinute <= 0 {
			return fmt.Errorf("surface %q: requests_per_minute must be > 0", name)
		}
		if s.Burst == nil || *s.Burst < 1 {
			return fmt.Errorf("surface %q: burst must be >= 1", name)
		}
	}
	// The inbound edges this control exists for: login (IP), the channel
	// per-telco fairness bucket, and channel_ip — the always-on pre-auth IP
	// throttle that a rotating-invalid-key flood cannot bypass (R-P0-8a-F1).
	for _, required := range []string{"login", "channel", "channel_ip"} {
		if _, ok := v.Surfaces[required]; !ok {
			return fmt.Errorf("surface %q is required (R-P0-8 inbound edge)", required)
		}
	}
	return nil
}
