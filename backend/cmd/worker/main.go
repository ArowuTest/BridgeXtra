// cmd/worker — outbox dispatcher (tcp_worker role, BYPASSRLS). M0 scope:
// claims per-aggregate-FIFO batches and dispatches to registered handlers;
// the only M0 handler is a structured-log sink proving the pipeline.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/invariants"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/outboxdispatch"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	invariantsOnce := flag.Bool("invariants", false,
		"run the BC-3 invariant sweep once and exit (exit 1 on any violation) — the V3-BOP-006 operator job")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	workerDSN := env("TCP_WORKER_DSN", "postgres://tcp_worker:devlocal_worker@localhost:5434/telco_credit")
	pool, err := platform.NewPool(ctx, workerDSN)
	if err != nil {
		log.Error("worker db connect failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	if *invariantsOnce {
		violations, err := (&invariants.Checker{Pool: pool}).Check(ctx)
		if err != nil {
			log.Error("invariant sweep failed", "err", err)
			os.Exit(1)
		}
		for _, v := range violations {
			fmt.Println("VIOLATION:", v.String())
		}
		if len(violations) > 0 {
			log.Error("invariant violations found", "count", len(violations))
			os.Exit(1)
		}
		log.Info("all invariants hold — the ledger balances at this instant")
		return
	}

	cfg := configsvc.New(pool)
	d := outboxdispatch.New(pool, cfg, log)
	d.Register("M0.Ping", func(ctx context.Context, e entity.OutboxEvent) error {
		log.Info("outbox event dispatched", "event_id", e.ID, "aggregate", e.AggregateID)
		return nil
	})

	log.Info("worker running")
	if err := d.Run(ctx, 2*time.Second); err != nil && ctx.Err() == nil {
		log.Error("worker stopped", "err", err)
		os.Exit(1)
	}
}
