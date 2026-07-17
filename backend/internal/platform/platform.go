// Package platform holds cross-cutting technical primitives: ID generation,
// tenant context propagation, and DB pool construction. No business logic.
package platform

import (
	"context"
	"crypto/rand"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
)

// ---------------------------------------------------------------------------
// IDs — ULIDs with a process-wide monotonic entropy source. Time-ordered
// (index-friendly inserts); NOT relied on for cross-process ordering — any
// ordering-critical path (outbox) uses a DB-assigned sequence (ADR-0001 SF-4).
// ---------------------------------------------------------------------------

var (
	entropyMu sync.Mutex
	entropy   = ulid.Monotonic(rand.Reader, 0)
)

func NewID(prefix string) string {
	entropyMu.Lock()
	id := ulid.MustNew(ulid.Timestamp(time.Now().UTC()), entropy)
	entropyMu.Unlock()
	if prefix == "" {
		return id.String()
	}
	return prefix + "_" + id.String()
}

// ---------------------------------------------------------------------------
// Tenant context (V2-TEN-002: resolved from authenticated identity, carried in
// context; the repo layer stamps it into the DB session for RLS).
// ---------------------------------------------------------------------------

type tenantKey struct{}

// WithTenant returns a context carrying the authenticated telco context.
func WithTenant(ctx context.Context, telcoID string) context.Context {
	return context.WithValue(ctx, tenantKey{}, telcoID)
}

// TenantFrom extracts the authenticated telco context. Absence is an error,
// never a wildcard: missing tenant context fails closed.
func TenantFrom(ctx context.Context) (string, error) {
	v, _ := ctx.Value(tenantKey{}).(string)
	if v == "" {
		return "", fmt.Errorf("no tenant context: request was not bound to an authenticated telco")
	}
	return v, nil
}

// ---------------------------------------------------------------------------
// Correlation context (BC-6: one correlation_id from the channel edge through
// every command, event, journal and audit row).
// ---------------------------------------------------------------------------

type correlationKey struct{}

func WithCorrelation(ctx context.Context, correlationID string) context.Context {
	return context.WithValue(ctx, correlationKey{}, correlationID)
}

// CorrelationFrom returns the request's correlation id, or generates one —
// a financial event without lineage is a defect, so absence never propagates.
func CorrelationFrom(ctx context.Context) string {
	if v, _ := ctx.Value(correlationKey{}).(string); v != "" {
		return v
	}
	return NewID("cor")
}

// ---------------------------------------------------------------------------
// DB pools
// ---------------------------------------------------------------------------

func NewPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	cfg.MaxConns = 10 // V2-INF-007: pooled and capped per service; tuned later per workload
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return pool, nil
}
