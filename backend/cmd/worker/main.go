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
	"encoding/json"
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
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform/egress"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/collections"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/fulfilmentresolver"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/notify"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/ops"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/origination"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/outboxdispatch"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/recon"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/replay"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/scoringsched"
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

// workerInstanceID identifies this worker process as a scoring-cycle claimant so
// multi-instance reclaims are attributable (host/pid/uuid).
func workerInstanceID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "worker"
	}
	return fmt.Sprintf("%s/%d/%s", host, os.Getpid(), platform.NewID("wkr"))
}

func main() {
	invariantsOnce := flag.Bool("invariants", false,
		"run the BC-3 invariant sweep once and exit (exit 1 on any violation) — the V3-BOP-006 operator job")
	reconOnce := flag.Bool("recon", false,
		"run fulfilment reconciliation once for every active telco/programme and exit (exit 1 on any break)")
	replayRun := flag.String("replay", "",
		"BC-4 operator job: replay-verify every decision of the given scoring run id and exit (exit 1 on any divergence); requires -telco")
	replayTelco := flag.String("telco", "SIM_NG", "tenant for -replay")
	delinquencyOnce := flag.Bool("delinquency", false,
		"run delinquency classification once for every active telco/programme and exit (V2 §15 daily job)")
	breaksOnce := flag.Bool("breaks", false,
		"report unresolved reconciliation breaks older than the governed aging threshold and exit 1 if any (V2-REC-012)")
	overridePropose := flag.String("recon-override-propose", "",
		"R-P0-6 D3 MAKER: propose a completeness override for the window of the given REJECTED recon run id; requires -actor and -reason")
	overrideApprove := flag.String("recon-override-approve", "",
		"R-P0-6 D3 CHECKER: approve the given completeness override id (must be a DIFFERENT -actor than the proposer)")
	overrideActor := flag.String("actor", "", "operator id for -recon-override-* (maker or checker)")
	overrideReason := flag.String("reason", "", "reason for -recon-override-propose")
	evidenceRun := flag.String("recon-evidence", "",
		"R-P0-6 E2: print the signed, reproducible evidence pack (JSON + pack_hash) for the given recon run id and exit")
	scoreOnce := flag.Bool("score", false,
		"Phase 0: run the durable scoring scheduler once for every active telco/programme and exit (ingest -> score on the config-driven cadence, idempotent per cycle)")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// #44 (VR-32 prod hardening): match the API — block loopback + private-range
	// egress in production. Default off for dev/Render private-network traffic.
	egress.SetBlockPrivate(env("TCP_EGRESS_BLOCK_PRIVATE", "false") == "true")
	log.Info("egress guard", "block_private_ranges", egress.BlockPrivateEnabled())

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

	if *overridePropose != "" {
		if *overrideActor == "" || *overrideReason == "" {
			log.Error("recon-override-propose requires -actor and -reason")
			os.Exit(1)
		}
		runOverridePropose(ctx, log, appPool, workerPool, appCfg, *overridePropose, *overrideActor, *overrideReason)
		return
	}

	if *overrideApprove != "" {
		if *overrideActor == "" {
			log.Error("recon-override-approve requires -actor")
			os.Exit(1)
		}
		runOverrideApprove(ctx, log, appPool, workerPool, appCfg, *overrideApprove, *overrideActor)
		return
	}

	if *evidenceRun != "" {
		runEvidencePack(ctx, log, appPool, workerPool, appCfg, *evidenceRun)
		return
	}

	if *breaksOnce {
		opsSvc := ops.New(appPool, appCfg, log)
		ts, err := telcos.ListActive(ctx)
		if err != nil {
			log.Error("breaks: list telcos", "err", err)
			os.Exit(1)
		}
		programmes := repo.Programmes{}
		total := 0
		for _, tc := range ts {
			tctx := platform.WithTenant(ctx, tc.TelcoID)
			var progs []entity.Programme
			if err := repo.WithTenantTx(tctx, appPool, func(tx pgx.Tx) error {
				var e error
				progs, e = programmes.ListForTenant(tctx, tx)
				return e
			}); err != nil {
				log.Error("breaks: list programmes", "telco", tc.TelcoID, "err", err)
				os.Exit(1)
			}
			for _, p := range progs {
				if p.Status != entity.ProgrammeActive {
					continue
				}
				aged, err := opsSvc.AgedBreaks(tctx, tc.TelcoID, p.ProgrammeID)
				if err != nil {
					log.Error("aged-breaks query failed", "telco", tc.TelcoID, "programme", p.ProgrammeID, "err", err)
					os.Exit(1)
				}
				for _, b := range aged {
					fmt.Printf("AGED BREAK %s: %s assigned=%q age=%dh\n", b.ReconItemID, b.Status, b.AssignedTo, b.AgeHours)
				}
				total += len(aged)
			}
		}
		if total > 0 {
			log.Error("aged reconciliation breaks demand attention", "count", total)
			os.Exit(1)
		}
		fmt.Println("no aged reconciliation breaks")
		return
	}

	if *delinquencyOnce {
		col := collections.New(appPool, appCfg, ledger.New(appCfg), log)
		ts, err := telcos.ListActive(ctx)
		if err != nil {
			log.Error("delinquency: list telcos", "err", err)
			os.Exit(1)
		}
		programmes := repo.Programmes{}
		for _, tc := range ts {
			tctx := platform.WithTenant(ctx, tc.TelcoID)
			var progs []entity.Programme
			if err := repo.WithTenantTx(tctx, appPool, func(tx pgx.Tx) error {
				var e error
				progs, e = programmes.ListForTenant(tctx, tx)
				return e
			}); err != nil {
				log.Error("delinquency: list programmes", "telco", tc.TelcoID, "err", err)
				os.Exit(1)
			}
			for _, p := range progs {
				if p.Status != entity.ProgrammeActive {
					continue
				}
				changed, err := col.Classify(tctx, tc.TelcoID, p.ProgrammeID)
				if err != nil {
					log.Error("delinquency classification failed", "telco", tc.TelcoID, "programme", p.ProgrammeID, "err", err)
					os.Exit(1)
				}
				fmt.Printf("delinquency %s/%s: %d bucket changes\n", tc.TelcoID, p.ProgrammeID, changed)
			}
		}
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

	// Phase 0 scoring scheduler, wired for both the one-shot job and the standing
	// loop. instanceID (host/pid/uuid) is the cycle claimant so multi-instance
	// reclaims are attributable. Scoring writes go through appPool (RLS); the
	// cross-tenant telco list uses workerPool.
	instanceID := workerInstanceID()
	scheduler := scoringsched.New(appPool, workerPool, appCfg, log, instanceID)

	if *scoreOnce {
		log.Info("scoring scheduler one-shot", "instance", instanceID)
		scheduler.RunDueAll(ctx, time.Now().UTC())
		log.Info("scoring scheduler one-shot complete")
		return
	}

	// --- standing services ---
	adapter := mno.NewHTTPAdapter(appCfg)
	led := ledger.New(appCfg)
	orig := origination.New(appPool, appCfg, led, adapter, log)
	resolver := fulfilmentresolver.New(appPool, appCfg, adapter, orig, log)

	d := outboxdispatch.New(workerPool, configsvc.New(workerPool), log)
	// advance.FulfilmentConfirmed drives the M2e subscriber notification with
	// evidence (V2 §10.2). The consumer is replay-safe end to end: the
	// evidence row is idempotent per (advance, kind) and the SMS submit is
	// idempotent at the telco.
	notifier := notify.New(appPool, appCfg, log)
	d.Register("advance.FulfilmentConfirmed", func(ctx context.Context, e entity.OutboxEvent) error {
		return notifier.AdvanceConfirmed(ctx, e.TelcoID, e.AggregateID)
	})
	// Remaining M1 event consumers: downstream systems (analytics, bureau
	// feeds) arrive in later milestones; until then the contract is proven by
	// consuming each event exactly once into the structured log.
	for _, et := range []string{
		"advance.FulfilmentFailed", "advance.FulfilmentUnknown",
		"advance.RecoveryApplied", "M0.Ping",
	} {
		d.Register(et, func(ctx context.Context, e entity.OutboxEvent) error {
			log.Info("outbox event consumed", "type", e.EventType, "event_id", e.ID,
				"aggregate", e.AggregateID, "telco", e.TelcoID)
			return nil
		})
	}

	dispatchEvery := envDur("TCP_DISPATCH_INTERVAL", 2*time.Second)
	resolveEvery := envDur("TCP_RESOLVER_INTERVAL", 5*time.Second)
	scoreEvery := envDur("TCP_SCORING_INTERVAL", 60*time.Second)

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

	// Scoring scheduler loop: on each tick, run the due cycle for every active
	// telco/programme. Most ticks are cheap no-ops (the cycle is already claimed
	// for this cadence window); a due cycle ingests fresh features and re-scores
	// so decisions never expire out from under offers.
	go func() {
		t := time.NewTicker(scoreEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				scheduler.RunDueAll(ctx, time.Now().UTC())
			}
		}
	}()

	log.Info("worker running", "dispatch_interval", dispatchEvery.String(),
		"resolver_interval", resolveEvery.String(), "scoring_interval", scoreEvery.String(),
		"instance", instanceID)
	if err := d.Run(ctx, dispatchEvery); err != nil && ctx.Err() == nil {
		log.Error("worker stopped", "err", err)
		os.Exit(1)
	}
}

// runOverridePropose is the MAKER operator job (R-P0-6 Slice D3): propose a
// completeness override for the window of a REJECTED recon run. The run's scope
// is read via the worker pool (cross-tenant); the override itself is created
// tenant-scoped through the recon service.
func runOverridePropose(ctx context.Context, log *slog.Logger, appPool, workerPool *pgxpool.Pool, appCfg *configsvc.Service, rejectedRunID, actor, reason string) {
	var telco, programme, state string
	var periodStart time.Time
	if err := workerPool.QueryRow(ctx, `
		SELECT telco_id, programme_id, period_start, state FROM recon_runs WHERE run_id=$1`,
		rejectedRunID).Scan(&telco, &programme, &periodStart, &state); err != nil {
		log.Error("recon-override-propose: recon run not found", "run", rejectedRunID, "err", err)
		os.Exit(1)
	}
	if state != "REJECTED" {
		log.Error("recon-override-propose: target run is not REJECTED", "run", rejectedRunID, "state", state)
		os.Exit(1)
	}
	svc := recon.New(appPool, appCfg, log)
	id, err := svc.ProposeCompletenessOverride(ctx, telco, programme, periodStart, actor, reason)
	if err != nil {
		log.Error("recon-override-propose failed", "err", err)
		os.Exit(1)
	}
	fmt.Printf("completeness override proposed: %s (window of rejected run %s) — needs a DIFFERENT actor to approve\n", id, rejectedRunID)
}

// runOverrideApprove is the CHECKER operator job: approve a pending completeness
// override. The four-eyes rule (approver != proposer) is schema-enforced.
func runOverrideApprove(ctx context.Context, log *slog.Logger, appPool, workerPool *pgxpool.Pool, appCfg *configsvc.Service, overrideID, actor string) {
	var telco string
	if err := workerPool.QueryRow(ctx,
		`SELECT telco_id FROM recon_completeness_overrides WHERE override_id=$1`, overrideID).Scan(&telco); err != nil {
		log.Error("recon-override-approve: override not found", "override", overrideID, "err", err)
		os.Exit(1)
	}
	svc := recon.New(appPool, appCfg, log)
	if err := svc.ApproveCompletenessOverride(ctx, telco, overrideID, actor); err != nil {
		log.Error("recon-override-approve failed", "err", err)
		os.Exit(1)
	}
	fmt.Printf("completeness override approved: %s — the next re-reconcile of that window will supersede despite the completeness floor\n", overrideID)
}

// runEvidencePack prints the signed, reproducible evidence pack for one recon
// run (R-P0-6 Slice E2). The run's telco is read cross-tenant via the worker
// pool; the pack is built tenant-scoped via the recon service.
func runEvidencePack(ctx context.Context, log *slog.Logger, appPool, workerPool *pgxpool.Pool, appCfg *configsvc.Service, runID string) {
	var telco string
	if err := workerPool.QueryRow(ctx,
		`SELECT telco_id FROM recon_runs WHERE run_id=$1`, runID).Scan(&telco); err != nil {
		log.Error("recon-evidence: run not found", "run", runID, "err", err)
		os.Exit(1)
	}
	pack, err := recon.New(appPool, appCfg, log).EvidencePack(ctx, telco, runID)
	if err != nil {
		log.Error("recon-evidence: build pack", "run", runID, "err", err)
		os.Exit(1)
	}
	out, err := json.MarshalIndent(pack, "", "  ")
	if err != nil {
		log.Error("recon-evidence: encode", "err", err)
		os.Exit(1)
	}
	fmt.Println(string(out))
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

			// R-P0-6 Slice D2 (VR-50-F1): after the incremental run advances the
			// watermark, re-sweep recent settled windows so a telco credit that
			// arrived late (behind the watermark) is recovered instead of stranded
			// as a missing-telco break. The re-swept windows are strictly earlier
			// than the incremental window just reconciled, so their breaks are not
			// double-counted; an unchanged window is skipped (no trail churn).
			resweeps, err := svc.ReconcileRecentPeriods(ctx, tc.TelcoID, p.ProgrammeID)
			if err != nil {
				log.Error("recon re-sweep failed", "telco", tc.TelcoID, "programme", p.ProgrammeID, "err", err)
				os.Exit(1)
			}
			for _, rs := range resweeps {
				if rs.Unchanged {
					continue
				}
				rsBreaks := rs.MissingPlatform + rs.MissingTelco + rs.AmountMismatch
				totalBreaks += rsBreaks
				fmt.Printf("recon-resweep %s/%s run=%s period=[%s,%s) matched=%d missing_platform=%d missing_telco=%d amount_mismatch=%d\n",
					tc.TelcoID, p.ProgrammeID, rs.RunID,
					rs.PeriodStart.UTC().Format(time.RFC3339), rs.PeriodEnd.UTC().Format(time.RFC3339),
					rs.Matched, rs.MissingPlatform, rs.MissingTelco, rs.AmountMismatch)
			}

			// R-P0-6 E2 (D2 metric-nit): the per-run break tallies above reflect
			// only what each run wrote. The authoritative CURRENT picture is the
			// count of unresolved breaks across the ACTIVE runs — this excludes
			// breaks recovered by a re-sweep (now in a superseded run) and breaks
			// cleared by two-actor resolution. Report it so the operator metric is
			// the live open-break count, not a stale per-run sum.
			var openBreaks int
			if err := repo.WithTenantTx(tctx, appPool, func(tx pgx.Tx) error {
				return tx.QueryRow(ctx, `
					SELECT count(*) FROM recon_items i JOIN recon_runs r ON r.run_id = i.run_id
					WHERE r.programme_id = $1 AND r.state = 'ACTIVE'
					  AND i.status LIKE 'BREAK_%' AND i.resolved_at IS NULL`, p.ProgrammeID).Scan(&openBreaks)
			}); err != nil {
				log.Error("recon: open-break count", "telco", tc.TelcoID, "programme", p.ProgrammeID, "err", err)
				os.Exit(1)
			}
			fmt.Printf("recon-open-breaks %s/%s open=%d\n", tc.TelcoID, p.ProgrammeID, openBreaks)
		}
	}
	if totalBreaks > 0 {
		log.Error("reconciliation breaks found", "total", totalBreaks)
		os.Exit(1)
	}
	log.Info("reconciliation clean across all active telcos/programmes")
}
