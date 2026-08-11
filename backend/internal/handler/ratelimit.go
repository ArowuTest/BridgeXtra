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
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform/ratelimit"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
)

// aggregatedSurfaces are the surfaces that MUST carry a cross-replica aggregate quota (BX-MED-006).
//
// `channel` is the per-telco surface the audit finding names: without a shared bucket, N replicas
// each grant one partner the full quota. Requiring it HERE, at boot, is what stops the control being
// silently dropped — delete the aggregate block from governed config and the process refuses to
// start, rather than quietly reverting to per-process limiting that looks identical from outside.
//
// `login` and `channel_ip` are deliberately absent, and that is a security decision rather than a
// deferral — see migrations/0083 for the argument and the attack. Aggregating an IP-keyed surface
// requires hashing client IPs into a fixed set of slots, and with a public unsalted hash an attacker
// can compute which slot a chosen victim lands in and hold it empty for well under one request per
// second, locking that victim out across every replica. The config SHAPE permits an aggregate on any
// surface, so nothing here forecloses the owner's choice; only the seeded config omits them.
var aggregatedSurfaces = []string{"channel"}

// LoadGuard reads platform.ratelimit and builds the rate-limit Guard (per-process limiter + the
// shared cross-replica aggregate) plus the trusted-proxy count. Missing/invalid config is a fatal
// boot error, so no surface ever runs unlimited.
func LoadGuard(ctx context.Context, cfg *configsvc.Service, store ratelimit.AggregateStore, log *slog.Logger) (*ratelimit.Guard, int, error) {
	cv, err := cfg.ActiveAt(ctx, "platform.ratelimit", "global", time.Now().UTC())
	if err != nil {
		return nil, 0, fmt.Errorf("platform.ratelimit config (required at boot): %w", err)
	}
	var raw struct {
		TrustedProxyCount int `json:"trusted_proxy_count"`
		Surfaces          map[string]struct {
			RequestsPerMinute float64 `json:"requests_per_minute"`
			Burst             float64 `json:"burst"`
			Aggregate         *struct {
				RequestsPerMinute float64 `json:"requests_per_minute"`
				Burst             float64 `json:"burst"`
			} `json:"aggregate"`
		} `json:"surfaces"`
	}
	if err := json.Unmarshal(cv.Content, &raw); err != nil {
		return nil, 0, fmt.Errorf("platform.ratelimit parse: %w", err)
	}
	limits := make(map[string]ratelimit.Limit, len(raw.Surfaces))
	agg := make(map[string]ratelimit.AggLimit)
	for name, s := range raw.Surfaces {
		limits[name] = ratelimit.Limit{RatePerMinute: s.RequestsPerMinute, Burst: s.Burst}
		if s.Aggregate == nil {
			continue
		}
		// Milli-tokens: the shared store counts in thousandths so a fractional refill is exact
		// integer arithmetic rather than something rounded away.
		burstMilli := int64(s.Aggregate.Burst * 1000)
		refillMilliPerSec := int64(s.Aggregate.RequestsPerMinute * 1000 / 60)
		if burstMilli < 1000 || refillMilliPerSec < 1 {
			// A rate below ~0.06/min cannot resolve to a whole milli-token per second, so the bucket
			// could never refill and the partner would be locked out permanently. Refuse at boot.
			return nil, 0, fmt.Errorf("platform.ratelimit surface %q: aggregate burst/rate too small to resolve (burst_milli=%d refill_milli_per_sec=%d)", name, burstMilli, refillMilliPerSec)
		}
		var quotaFrom time.Time
		if cv.EffectiveFrom != nil {
			quotaFrom = *cv.EffectiveFrom
		}
		if quotaFrom.IsZero() {
			// The shared row adopts a quota only from a strictly newer effective config. A zero
			// timestamp would make every replica's quota look equally old and the adopt rule
			// meaningless, so refuse rather than run an unordered aggregate.
			return nil, 0, fmt.Errorf("platform.ratelimit surface %q: aggregate requires the config version's effective_from", name)
		}
		agg[name] = ratelimit.AggLimit{BurstMilli: burstMilli, RefillMilliPerSec: refillMilliPerSec, QuotaFrom: quotaFrom}
	}
	for _, req := range []string{"login", "channel", "channel_ip"} {
		if _, ok := limits[req]; !ok {
			return nil, 0, fmt.Errorf("platform.ratelimit missing required surface %q", req)
		}
	}
	for _, req := range aggregatedSurfaces {
		if _, ok := agg[req]; !ok {
			return nil, 0, fmt.Errorf("platform.ratelimit surface %q must configure an aggregate block (BX-MED-006 cross-replica quota)", req)
		}
	}
	if len(agg) > 0 && store == nil {
		return nil, 0, fmt.Errorf("platform.ratelimit configures an aggregate but no shared store was provided")
	}
	if raw.TrustedProxyCount < 0 {
		return nil, 0, fmt.Errorf("platform.ratelimit trusted_proxy_count must be >= 0")
	}
	return ratelimit.NewGuard(ratelimit.New(limits), store, agg, 0, log), raw.TrustedProxyCount, nil
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

// mustGuard is the fail-closed boot check every rate-limited door shares.
//
// It refuses on a nil Guard, a nil local limiter, AND a required aggregate scope that is absent or
// storeless. Checking only "the Guard is non-nil" was not enough: a Guard carrying an empty
// aggregate map would mount happily and serve a money door with replica-local quota only — the
// armed-but-dead shape, moved one layer down from the config decoder to the injection site.
func mustGuard(door string, g *ratelimit.Guard, requiredAggregates ...string) {
	if g == nil || g.Local == nil {
		panic(door + ": rate limit guard is required (R-P0-8 fail-closed)")
	}
	for _, scope := range requiredAggregates {
		if !g.HasAggregate(scope) {
			panic(door + ": aggregate quota for scope " + scope + " is required (BX-MED-006 cross-replica)")
		}
		if g.Store == nil {
			panic(door + ": aggregate quota for scope " + scope + " needs a shared store (BX-MED-006)")
		}
	}
}

// rateLimited wraps a handler under a scope keyed by keyFn, applying the PER-PROCESS limiter only.
// Used for the IP-keyed pre-auth throttles, which are deliberately not aggregated across replicas
// (see aggregatedSurfaces above). A refused request is 429 + Retry-After; no downstream logic runs.
func rateLimited(g *ratelimit.Guard, scope string, keyFn func(*http.Request) string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !g.AllowLocal(scope, keyFn(r)) {
			w.Header().Set("Retry-After", "60")
			writeErr(w, http.StatusTooManyRequests, "RATE_LIMITED", "too many requests; slow down and retry shortly")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// perTelcoRateLimit is the POST-auth channel throttle. It keys on the VALIDATED telco from context
// (set by TenantAuth), so a forged credential — which never resolves a telco — can never reach or
// mint a bucket here.
//
// BX-MED-006: this applies the per-process limiter AND the shared cross-replica quota. The telco
// argument is taken ONLY from platform.TenantFrom — never from a request field — which, together
// with the FK on rate_limit_buckets, is what bounds which tenant's bucket a bug could spend. A
// source fence test holds that property.
func perTelcoRateLimit(g *ratelimit.Guard, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		telcoID, err := platform.TenantFrom(r.Context())
		if err != nil || telcoID == "" {
			// No resolved tenant post-auth would be a wiring bug; fail safe by
			// refusing rather than bypassing the fairness control.
			writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
			return
		}
		allowed, reason := g.AllowTelco(r.Context(), "channel", telcoID)
		if !allowed {
			w.Header().Set("Retry-After", "60")
			switch reason {
			case ratelimit.ReasonContended:
				// The shared bucket row is being hammered. That is a rate-limit answer, not a server
				// fault: failing open here would let anyone who can induce contention on their own
				// bucket move the control to its weaker setting by attacking it.
				writeErr(w, http.StatusTooManyRequests, "RATE_LIMITED", "too many concurrent requests for this telco; slow down")
			case ratelimit.ReasonUnconfigured:
				// Reaching an unconfigured aggregate scope is a wiring bug on a money door. Boot
				// requires the block, so this is unreachable in a correctly-built process — and the
				// safe answer to a wiring bug here is refusal, not an unlimited surface.
				writeErr(w, http.StatusServiceUnavailable, "SERVICE_TEMPORARILY_LIMITED", "rate limiting unavailable")
			default:
				writeErr(w, http.StatusTooManyRequests, "RATE_LIMITED", "too many requests for this telco; slow down")
			}
			return
		}
		next.ServeHTTP(w, r)
	})
}
