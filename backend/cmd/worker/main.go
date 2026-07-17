// cmd/worker — the platform's background plane: outbox dispatcher
// (per-aggregate FIFO) and the fulfilment resolver (EDG-005/007/008 —
// FULFILMENT_UNKNOWN is resolved by status enquiry, never blind retry).
// Also hosts the on-demand operator jobs: -invariants (BC-3 sweep) and
// -recon (fulfilment-layer reconciliation).
//
// Role note: locally the dispatcher runs as tcp_worker (BYPASSRLS). On
// managed Postgres (Render class) BYPASSRLS is superuser-only, so
// TCP_WORKER_DSN points at the database owner instead — ENABLE-RLS does not
// apply to the table owner, which yields the same cross-tenant dispatch
// capability while tcp_app remains fully RLS-enforced.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/invariants"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/ledger"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/mno"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/fulfilmentresolver"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/origination"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/outboxdispatch"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/recon"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/replay"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envDur(k string, def time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return def
}

func main() {
	invariantsOnce := flag.Bool("invariants", false,
		"run the BC-3 invariant sweep once and exit (exit 1 on any violation) — the V3-BOP-006 operator job")
	reconOnce := flag.Bool("recon", false,
		"run fulfilment reconciliation once for every active telco/programme and exit (exit 1 on any break)")
	replayRun := flag.String("replay", "",
		"BC-4 operator job: replay-verify every decision of the given scoring run id and exit (exit 1 on any divergence); requires -telco")
	replayTelco := flag.String("telco", "SIM_NG", "tenant for -replay")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	workerDSN := env("TCP_WORKER_DSN", "postgres://tcp_worker:devlocal_worker@localhost:5434/telco_credit")
	appDSN := env("TCP_APP_DSN", "postgres://tcp_app:devlocal_app@localhost:5434/telco_credit")

	workerPool, err := platform.NewPool(ctx, workerDSN)
	if err != nil {
		log.Error("worker db connect failed", "err", err)
		os.Exit(1)
	}
	defer workerPool.Close()

	if *invariantsOnce {
		violations, err := (&invariants.Checker{Pool: workerPool}).Check(ctx)
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

	appPool, err := platform.NewPool(ctx, appDSN)
	if err != nil {
		log.Error("app db connect failed", "err", err)
		os.Exit(1)
	}
	defer appPool.Close()

	appCfg := configsvc.New(appPool)
	telcos := &repo.Telcos{Pool: workerPool}

	if *reconOnce {
		runRecon(ctx, log, appPool, appCfg, telcos)
		return
	}

	if *replayRun != "" {
		res, err := replay.New(appPool, appCfg, log).VerifyRun(ctx, *replayTelco, *replayRun)
		if err != nil {
			log.Error("replay verification failed to run", "err", err)
			os.Exit(1)
		}
		for _, m := range res.Mismatches {
			fmt.Println("DIVERGENCE:", m.DecisionSnapshotID, "—", m.Reason)
		}
		if len(res.Mismatches) > 0 {
			os.Exit(1)
		}
		fmt.Printf("replay verified: %d decisions reproduce bit-exactly (run %s)\n", res.Checked, res.RunID)
		return
	}

	// --- standing services ---
	adapter := mno.NewHTTPAdapter(appCfg)
	led := ledger.New(appCfg)
	orig := origination.New(appPool, appCfg, led, adapter, log)
	resolver := fulfilmentresolver.New(appPool, appCfg, adapter, orig, log)

	d := outboxdispatch.New(workerPool, configsvc.New(workerPool), log)
	// M1 event consumers: downstream systems (notifications, analytics, bureau
	// feeds) arrive in later milestones; until then the contract is proven by
	// consuming each event exactly once into the structured log.
	for _, et := range []string{
		"advance.FulfilmentConfirmed", "advance.FulfilmentFailed",
		"advance.FulfilmentUnknown", "advance.RecoveryApplied",
		"M0.Ping",
	} {
		d.Register(et, func(ctx context.Context, e entity.OutboxEvent) error {
			log.Info("outbox event consumed", "type", e.EventType, "event_id", e.ID,
				"aggregate", e.AggregateID, "telco", e.TelcoID)
			return nil
		})
	}

	dispatchEvery := envDur("TCP_DISPATCH_INTERVAL", 2*time.Second)
	resolveEvery := envDur("TCP_RESOLVER_INTERVAL", 5*time.Second)

	// Resolver loop: iterate active telcos from the registry (never a
	// hardcoded tenant list) and resolve due enquiries for each.
	go func() {
		t := time.NewTicker(resolveEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				ts, err := telcos.ListActive(ctx)
				if err != nil {
					log.Error("resolver: list telcos", "err", err)
					continue
				}
				for _, tc := range ts {
					if n, err := resolver.RunOnce(ctx, tc.TelcoID, 50); err != nil {
						log.Error("resolver run failed", "telco", tc.TelcoID, "err", err)
					} else if n > 0 {
						log.Info("resolver resolved attempts", "telco", tc.TelcoID, "count", n)
					}
				}
			}
		}
	}()

	log.Info("worker running", "dispatch_interval", dispatchEvery.String(),
		"resolver_interval", resolveEvery.String())
	if err := d.Run(ctx, dispatchEvery); err != nil && ctx.Err() == nil {
		log.Error("worker stopped", "err", err)
		os.Exit(1)
	}
}

// runRecon executes fulfilment reconciliation for every active telco and each
// of its ACTIVE programmes; any break exits non-zero so operators can alert
// on the exit code (V2-REC-012: breaks demand attention, never auto-resolve).
func runRecon(ctx context.Context, log *slog.Logger, appPool *pgxpool.Pool, appCfg *configsvc.Service, telcos *repo.Telcos) {
	svc := recon.New(appPool, appCfg, log)
	programmes := repo.Programmes{}

	ts, err := telcos.ListActive(ctx)
	if err != nil {
		log.Error("recon: list telcos", "err", err)
		os.Exit(1)
	}
	totalBreaks := 0
	for _, tc := range ts {
		tctx := platform.WithTenant(ctx, tc.TelcoID)
		var progs []entity.Programme
		if err := repo.WithTenantTx(tctx, appPool, func(tx pgx.Tx) error {
			var e error
			progs, e = programmes.ListForTenant(tctx, tx)
			return e
		}); err != nil {
			log.Error("recon: list programmes", "telco", tc.TelcoID, "err", err)
			os.Exit(1)
		}
		for _, p := range progs {
			if p.Status != entity.ProgrammeActive {
				continue
			}
			sum, err := svc.RunFulfilment(ctx, tc.TelcoID, p.ProgrammeID)
			if err != nil {
				log.Error("recon run failed", "telco", tc.TelcoID, "programme", p.ProgrammeID, "err", err)
				os.Exit(1)
			}
			breaks := sum.MissingPlatform + sum.MissingTelco + sum.AmountMismatch
			totalBreaks += breaks
			fmt.Printf("recon %s/%s run=%s matched=%d missing_platform=%d missing_telco=%d amount_mismatch=%d\n",
				tc.TelcoID, p.ProgrammeID, sum.RunID, sum.Matched,
				sum.MissingPlatform, sum.MissingTelco, sum.AmountMismatch)
		}
	}
	if totalBreaks > 0 {
		log.Error("reconciliation breaks found", "total", totalBreaks)
		os.Exit(1)
	}
	log.Info("reconciliation clean across all active telcos/programmes")
}
