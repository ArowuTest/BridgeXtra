// Package outboxdispatch is the worker-side dispatcher for the transactional
// outbox (ADR-0001 SF-4): per-aggregate FIFO, SKIP LOCKED across aggregates,
// bounded attempts from governed config (V2-EVT-007).
package outboxdispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
)

// Handler consumes one event. Handlers MUST be idempotent (V2-EVT-003):
// dispatch is at-least-once.
type Handler func(ctx context.Context, e entity.OutboxEvent) error

type Dispatcher struct {
	Pool     *pgxpool.Pool // tcp_worker role (BYPASSRLS)
	Config   *configsvc.Service
	Log      *slog.Logger
	handlers map[string]Handler
	outbox   repo.Outbox
}

func New(pool *pgxpool.Pool, cfg *configsvc.Service, log *slog.Logger) *Dispatcher {
	return &Dispatcher{Pool: pool, Config: cfg, Log: log, handlers: map[string]Handler{}}
}

// Register binds an event type to a handler. Unregistered event types are
// left unclaimed (visible as backlog — never silently dropped).
func (d *Dispatcher) Register(eventType string, h Handler) { d.handlers[eventType] = h }

type tuning struct {
	ClaimBatchSize      int `json:"claim_batch_size"`
	MaxAttempts         int `json:"max_attempts"`
	RetryBackoffSeconds int `json:"retry_backoff_seconds"`
}

// RunOnce claims and dispatches one batch; returns events processed.
func (d *Dispatcher) RunOnce(ctx context.Context) (int, error) {
	cfg, err := d.Config.ActiveAt(ctx, "platform.outbox", entity.ScopeGlobal, time.Now().UTC())
	if err != nil {
		return 0, fmt.Errorf("outbox config: %w", err)
	}
	var t tuning
	if err := json.Unmarshal(cfg.Content, &t); err != nil {
		return 0, fmt.Errorf("outbox config parse: %w", err)
	}

	n := 0
	err = repo.WithPlatformTx(ctx, d.Pool, func(tx pgx.Tx) error {
		events, err := d.outbox.ClaimBatch(ctx, tx, t.ClaimBatchSize)
		if err != nil {
			return err
		}
		for _, e := range events {
			h, ok := d.handlers[e.EventType]
			if !ok {
				// leave unclaimed; skip inside this batch
				continue
			}
			if e.Attempts >= t.MaxAttempts {
				d.Log.Error("outbox event exceeded max attempts; parked for operator replay",
					"event_id", e.ID, "event_type", e.EventType, "attempts", e.Attempts)
				continue // stays unpublished: visible backlog, operator-controlled replay (V2-EVT-008/009)
			}
			if err := h(ctx, e); err != nil {
				if mErr := d.outbox.MarkFailed(ctx, tx, e.Seq, err.Error()); mErr != nil {
					return mErr
				}
				d.Log.Warn("outbox handler failed", "event_id", e.ID, "err", err)
				continue
			}
			if err := d.outbox.MarkPublished(ctx, tx, e.Seq, time.Now().UTC()); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

// Run polls until ctx is cancelled.
func (d *Dispatcher) Run(ctx context.Context, idle time.Duration) error {
	for {
		n, err := d.RunOnce(ctx)
		if err != nil {
			d.Log.Error("outbox dispatch cycle failed", "err", err)
		}
		if n == 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(idle):
			}
		}
	}
}
