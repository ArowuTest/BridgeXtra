package ratelimit

// BX-MED-006: the Guard pairs the per-process Limiter with a shared, cross-replica aggregate.
//
// The per-process limiter alone multiplies: `ratelimit.New` runs once per API process, so N replicas
// each grant one partner the full configured quota. The Guard adds a boundary that is authoritative
// across processes, and KEEPS the local limiter in front of it as defence in depth — a replica that
// loses the shared store still caps itself.
//
// FAILURE POLICY — the part that took the most argument to get right.
//
// An earlier design degraded to a looser local limit whenever the aggregate was unavailable. That
// is wrong in a way that is easy to miss: lock contention on the shared bucket row is ATTACKER-
// INDUCIBLE, so "unavailable => weaker enforcement" hands an attacker a switch that moves the
// control to its weaker setting by attacking it. It was also unsound arithmetically (the resulting
// ceiling is N x degraded, and N — the replica count — exists nowhere in governed config, so no
// validator can check it), and it oscillated (exiting degraded mode on a single success lets an
// attacker duty-cycle between regimes). Implementing it would additionally have required mutating
// the Limiter's limits map at runtime, which Allow reads OUTSIDE the mutex — a concurrent map
// read/write, which Go aborts the process for and no middleware can recover.
//
// So the policy splits by CAUSE, not by count:
//
//   * CONTENDED (lock timeout / cancelled statement / deadlock / deadline) => DENY, 429.
//     Contention on one bucket row means that bucket is being hammered. Refusing is both the
//     correct answer and attributable to the caller. Failing open here would reward the attack.
//
//   * UNREACHABLE (connection-level failure) => fall back to the local limiter alone, count it, and
//     log at most once per interval. This is the honest boundary of the design: while the shared
//     store is down, cross-replica enforcement is IMPOSSIBLE by definition, and the aggregate
//     ceiling is undefined for that window. The local limiter still caps each replica at its
//     configured rate. No invented arithmetic claims otherwise.

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"
)

// AggregateStore is the shared bucket store (Postgres in production).
type AggregateStore interface {
	Take(ctx context.Context, scope, telcoID string, burstMilli, refillMilliPerSec int64, quotaFrom time.Time) (bool, error)
}

// ErrContended reports that the shared bucket row could not be spent because it is being contended.
// It lives HERE, not in the repo layer, so this package can classify a store failure without taking
// a database dependency — and so the distinction cannot be lost in translation between the two.
var ErrContended = errors.New("aggregate rate limit bucket contended")

// AggLimit is one surface's shared quota, resolved from governed config at boot.
type AggLimit struct {
	BurstMilli        int64
	RefillMilliPerSec int64
	QuotaFrom         time.Time // the governing config version's effective_from
}

// aggCounters are per-scope observability counters. The map is built once in NewGuard and never
// written again — only the atomics inside it are mutated — so there is no concurrent map write.
type aggCounters struct {
	denied      atomic.Int64 // refused by the shared quota (expected, healthy)
	contended   atomic.Int64 // refused because the bucket row was contended
	unreachable atomic.Int64 // store unreachable; local-only for this request
	lastLogUnix atomic.Int64
}

// Guard is the single injected rate-limit dependency. Handlers hold ONE of these rather than a
// limiter plus a store: two fields invite a construction where one door gets the local limiter and
// the aggregate is quietly left off the other.
type Guard struct {
	Local   *Limiter
	Store   AggregateStore
	Agg     map[string]AggLimit
	Timeout time.Duration
	Log     *slog.Logger

	counters map[string]*aggCounters
	now      func() time.Time
}

const (
	defaultAggTimeout = 250 * time.Millisecond
	unreachableLogGap = 10 * time.Second
)

// NewGuard builds a Guard. agg may be empty (no surface aggregated); when it is NOT empty a store is
// required, and NewGuard is not the place that enforces it — Mount is, at boot, so a missing store
// is a refusal to start rather than a surprise on the first request.
func NewGuard(local *Limiter, store AggregateStore, agg map[string]AggLimit, timeout time.Duration, log *slog.Logger) *Guard {
	cp := make(map[string]AggLimit, len(agg))
	counters := make(map[string]*aggCounters, len(agg))
	for k, v := range agg {
		cp[k] = v
		counters[k] = &aggCounters{}
	}
	if timeout <= 0 {
		timeout = defaultAggTimeout
	}
	return &Guard{Local: local, Store: store, Agg: cp, Timeout: timeout, Log: log, counters: counters, now: time.Now}
}

// AllowLocal applies only the per-process limiter, for surfaces with no aggregate (the IP-keyed
// pre-auth throttles). Unknown scope is DENIED by Limiter.Allow, so this carries no permissive path.
func (g *Guard) AllowLocal(scope, key string) bool { return g.Local.Allow(scope, key) }

// HasAggregate reports whether a surface is configured for cross-replica enforcement.
func (g *Guard) HasAggregate(scope string) bool { _, ok := g.Agg[scope]; return ok }

// AllowTelco applies the per-process limiter and then the shared aggregate for an identity-keyed
// surface. It returns the decision plus the reason, so the caller can distinguish a quota refusal
// from a contention refusal in its response and its logs.
//
// ORDER IS LOAD-BEARING: local FIRST, aggregate second, and a granted aggregate token is never
// refunded. Spending the shared token before the local check would let one replica burn the
// partner's whole shared quota while serving only its own local fraction — starving every other
// replica of budget it never used.
//
// An unconfigured scope is DENIED, matching this package's documented floor (an unknown scope must
// never run unlimited). Boot guarantees every aggregated surface is present, so reaching this with
// an unknown scope is a wiring bug, and the safe answer to a wiring bug on a money door is no.
func (g *Guard) AllowTelco(ctx context.Context, scope, telcoID string) (bool, Reason) {
	if !g.Local.Allow(scope, "telco:"+telcoID) {
		return false, ReasonLocalQuota
	}
	lim, ok := g.Agg[scope]
	if !ok {
		return false, ReasonUnconfigured
	}
	if g.Store == nil || telcoID == "" {
		return false, ReasonUnconfigured
	}

	c := g.counters[scope]
	tctx, cancel := context.WithTimeout(ctx, g.Timeout)
	defer cancel()

	granted, err := g.Store.Take(tctx, scope, telcoID, lim.BurstMilli, lim.RefillMilliPerSec, lim.QuotaFrom)
	if err != nil {
		if isContended(err) {
			if c != nil {
				c.contended.Add(1)
			}
			return false, ReasonContended
		}
		// Unreachable store: the local limiter has already allowed this request and remains the only
		// active cap. Count it and log sparsely — one ERROR per request would be a log-volume DoS at
		// hundreds of requests per minute per replica during an outage.
		if c != nil {
			c.unreachable.Add(1)
			g.logUnreachable(scope, err, c)
		}
		return true, ReasonStoreUnreachable
	}
	if !granted {
		if c != nil {
			c.denied.Add(1)
		}
		return false, ReasonAggregateQuota
	}
	return true, ReasonAllowed
}

func (g *Guard) logUnreachable(scope string, err error, c *aggCounters) {
	if g.Log == nil {
		return
	}
	now := g.now().UnixNano()
	last := c.lastLogUnix.Load()
	if now-last < int64(unreachableLogGap) {
		return
	}
	if !c.lastLogUnix.CompareAndSwap(last, now) {
		return // another goroutine just logged for this scope
	}
	g.Log.Error("aggregate rate limit store unreachable; falling back to per-process limiting",
		"scope", scope, "err", err, "unreachable_total", c.unreachable.Load())
}

// Counters exposes the per-scope observability counters (denied / contended / unreachable). A
// cross-replica control with no operator-visible signal is unfalsifiable in production.
func (g *Guard) Counters(scope string) (denied, contended, unreachable int64) {
	c, ok := g.counters[scope]
	if !ok {
		return 0, 0, 0
	}
	return c.denied.Load(), c.contended.Load(), c.unreachable.Load()
}

func isContended(err error) bool { return errors.Is(err, ErrContended) }

// Reason explains an AllowTelco decision.
type Reason string

const (
	ReasonAllowed          Reason = "allowed"
	ReasonLocalQuota       Reason = "local_quota"
	ReasonAggregateQuota   Reason = "aggregate_quota"
	ReasonContended        Reason = "aggregate_contended"
	ReasonStoreUnreachable Reason = "aggregate_store_unreachable"
	ReasonUnconfigured     Reason = "aggregate_unconfigured"
)
