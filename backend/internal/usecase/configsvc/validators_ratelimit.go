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
		Surfaces map[string]struct {
			RequestsPerMinute *float64 `json:"requests_per_minute"`
			Burst             *float64 `json:"burst"`
		} `json:"surfaces"`
	}
	if err := strictUnmarshal(content, &v); err != nil {
		return fmt.Errorf("parse: %w", err)
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
	// The two inbound edges this control exists for must be present.
	for _, required := range []string{"login", "channel"} {
		if _, ok := v.Surfaces[required]; !ok {
			return fmt.Errorf("surface %q is required (R-P0-8 inbound edge)", required)
		}
	}
	return nil
}
