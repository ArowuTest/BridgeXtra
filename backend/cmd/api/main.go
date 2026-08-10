// cmd/api — platform API service: boot-time self-migration, tenant middleware,
// health, the channel + recharge-webhook money surface, the operator portal, and a
// tenant-scoped programmes endpoint.
//
// BX-HIGH-012 Part B (control-plane / data-plane split): TCP_API_MODE selects which
// surface this process serves —
//   - "data"    — the PUBLIC data plane: channel API + recharge webhook + tenant
//     programmes. Runs on tcp_app only; opens NO tcp_config/operator pool
//     and mounts NO portal/config routes. This is the internet-facing tier.
//   - "control" — the INTERNAL control plane: operator portal + config governance +
//     the programme->telco resolver, on tcp_app + tcp_operator + tcp_config.
//     Deploy internal-only (not reachable from the public internet).
//   - "all"     — both surfaces in one process (default; local dev + single-service
//     deployments). Non-breaking: identical to the pre-split wiring.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/handler"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/ledger"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/mno"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform/buildinfo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform/dbmigrate"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform/dbroles"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform/egress"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/rechargewebhook"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/collections"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/operatormgmt"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/ops"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/origination"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/rechargehold"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/recovery"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/settlement"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/treasury"
	"github.com/ArowuTest/telco-credit-platform/backend/migrations"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// deriveRoleDSN rebuilds a DSN for a different role from a template DSN — same host, port,
// database and query parameters, with the userinfo swapped to (role, password). Used to derive
// the tcp_config connection from TCP_APP_DSN when TCP_CONFIG_DSN is not set, so the control
// plane boots against the real database without a second DSN having to be wired first.
func deriveRoleDSN(templateDSN, role, password string) (string, error) {
	u, err := url.Parse(templateDSN)
	if err != nil {
		return "", fmt.Errorf("parse template DSN: %w", err)
	}
	u.User = url.UserPassword(role, password)
	return u.String(), nil
}

// planes records which surfaces this process serves.
type planes struct {
	serveData    bool
	serveControl bool
}

// resolvePlanes maps TCP_API_MODE to the surfaces to mount. An unknown mode is a
// fatal misconfiguration (fail-closed), never a silent default.
func resolvePlanes(mode string) (planes, error) {
	switch mode {
	case "", "all":
		return planes{serveData: true, serveControl: true}, nil
	case "data":
		return planes{serveData: true}, nil
	case "control":
		return planes{serveControl: true}, nil
	default:
		return planes{}, fmt.Errorf("invalid TCP_API_MODE %q (want: all | data | control)", mode)
	}
}

// serverDeps are the process-lifetime dependencies buildMux mounts routes onto.
// OperatorPool and ConfigPool are nil for a pure data-plane process — it neither
// opens nor needs them (BX-HIGH-012: config/resolver privilege is absent from the
// public surface).
type serverDeps struct {
	AppPool      *pgxpool.Pool
	OperatorPool *pgxpool.Pool // control plane only
	ConfigPool   *pgxpool.Pool // control plane only
	Log          *slog.Logger
}

// buildMux constructs the HTTP mux for the requested planes. Route mounting is the
// SINGLE place the split is enforced: a data-plane process mounts channel + webhook +
// programmes; a control-plane process mounts the portal. /healthz is always present.
func buildMux(ctx context.Context, p planes, d serverDeps) (*http.ServeMux, error) {
	// R-P0-8: inbound rate limiter from governed config — fail-closed (the process
	// refuses to boot without it, so no surface ever runs unlimited). Read via the app
	// role, which has SELECT on config_versions — no config/BYPASSRLS pool needed here,
	// which is what lets a data-plane process load its limiter without a config pool.
	limiter, trustedProxies, err := handler.LoadRateLimiter(ctx, configsvc.New(d.AppPool))
	if err != nil {
		return nil, fmt.Errorf("rate limiter (required at boot): %w", err)
	}

	// Money core on the app role — shared usecase logic over the same DB, used by the
	// channel (data plane) and by the portal's re-arm/demo paths (control plane).
	appCfg := configsvc.New(d.AppPool)
	led := ledger.New(appCfg)
	rec := recovery.New(d.AppPool, appCfg, led, d.Log)
	orig := origination.New(d.AppPool, appCfg, led, mno.NewHTTPAdapter(appCfg), d.Log)

	mux := http.NewServeMux()
	// BX-MED-005: split liveness from readiness. /healthz is DB-free liveness (is the process up?)
	// so a transient DB blip drains readiness without triggering a restart storm; /readyz is the
	// DB-dependent readiness probe, and it also reports NOT-ready once a graceful shutdown begins
	// so the load balancer drains this instance BEFORE connections start closing. /version reports
	// build identity. All three are present on every plane.
	mux.HandleFunc("GET /healthz", handler.Live())
	mux.HandleFunc("GET /readyz", drainAwareReadiness(handler.Health(d.AppPool)))
	mux.HandleFunc("GET /version", handler.Version())

	if p.serveControl {
		// M4a portal: session auth (httpOnly + CSRF) with deny-by-default RBAC. Config is
		// managed ONLY through the portal — RBAC- (ADMIN-only for mutations) and scope-
		// gated; config governance + the programme->telco resolver run on tcp_config. The
		// former header-authenticated admin config API was removed in EXT-1 (a role-unaware
		// parallel door). Re-arm actions run as the app role in a tenant tx; operator reads
		// run on the RLS-enforced tcp_operator pool inside a scope-set tx (OperatorReader),
		// with the tcp_config pool as the trusted programme->telco resolver only.
		portal := &handler.Portal{
			Admins:            &repo.Admins{Pool: d.AppPool},
			Sessions:          &repo.PortalSessions{Pool: d.AppPool},
			Config:            configsvc.New(d.ConfigPool),
			Treasury:          treasury.New(d.AppPool, configsvc.New(d.AppPool), d.Log),
			Ops:               ops.New(d.AppPool, configsvc.New(d.AppPool), d.Log),
			Settlement:        settlement.New(d.AppPool, configsvc.New(d.AppPool), d.Log),
			Recovery:          rec,
			Collections:       collections.New(d.AppPool, appCfg, led, d.Log),
			Demo:              ops.NewDemo(d.AppPool, appCfg, orig, d.Log),
			Operator:          repo.OperatorReader{Pool: d.OperatorPool, Resolve: d.ConfigPool},
			Audit:             repo.Audit{},
			Pool:              d.AppPool,
			Operators:         operatormgmt.New(d.AppPool, d.Log),
			Held:              rechargehold.New(d.AppPool, rec, d.Log),
			Limiter:           limiter,
			TrustedProxyCount: trustedProxies,
			Log:               d.Log,
		}
		portal.Mount(mux)
	}

	if p.serveData {
		telcos := &repo.Telcos{Pool: d.AppPool}
		auth := &handler.TenantAuth{Telcos: telcos, Pool: d.AppPool, Log: d.Log}
		programmes := repo.Programmes{}

		channel := &handler.Channel{Origination: orig, Recovery: rec, Limiter: limiter, TrustedProxyCount: trustedProxies, Log: d.Log}
		channel.Mount(mux, auth)

		// Phase 1 S2: the inbound MNO recharge webhook (HMAC-authenticated, its own
		// middleware chain, feeding the same recovery money core). DORMANT until a telco
		// is armed (feed enabled at both scopes + its RECOVERY recon layer live).
		rechargeHook := &handler.RechargeWebhook{
			Recovery: rec, Config: appCfg,
			Creds: &repo.WebhookCredentials{Pool: d.AppPool}, Recon: &repo.ReconArming{Pool: d.AppPool},
			Pool: d.AppPool, Auth: rechargewebhook.NewHMACSHA256Adapter(), Mapper: rechargewebhook.NewJSONMapper(),
			Limiter: limiter, TrustedProxyCount: trustedProxies, Log: d.Log,
		}
		rechargeHook.Mount(mux)

		mux.Handle("GET /v1/programmes", auth.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			var out []entity.Programme
			err := repo.WithTenantTx(ctx, d.AppPool, func(tx pgx.Tx) error {
				var e error
				out, e = programmes.ListForTenant(ctx, tx)
				return e
			})
			if err != nil {
				http.Error(w, `{"error_code":"SYSTEM_TEMPORARILY_UNAVAILABLE"}`, http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(out)
		})))
	}

	return mux, nil
}

// apiMaxAdapterTimeout is the governed ceiling on the MNO adapter's request_timeout_ms
// (configsvc validator: request_timeout_ms must be 100..60000). The confirm saga blocks
// synchronously on that call, so the server WriteTimeout must stay above it.
const apiMaxAdapterTimeout = 60 * time.Second

// apiWriteTimeout bounds a whole handler+write. It MUST exceed apiMaxAdapterTimeout so a slow but
// legitimate confirm is never truncated mid-flight.
const apiWriteTimeout = apiMaxAdapterTimeout + 60*time.Second

// Graceful-shutdown budget (BX-MED-005).
//
//	defaultDrainLead    — how long readiness reports NOT-ready BEFORE connections start closing,
//	                      so the load balancer stops sending new work first.
//	defaultShutdownGrace— how long in-flight requests may take to finish. It MUST be at least
//	                      apiWriteTimeout: the server itself allows a request to run that long, so
//	                      a smaller drain budget would sever a confirm the server had authorised —
//	                      exactly the money-critical case this fix exists to protect.
const (
	defaultDrainLead     = 5 * time.Second
	defaultShutdownGrace = apiWriteTimeout + 15*time.Second
)

// draining flips once a graceful shutdown begins; /readyz then reports NOT-ready while /healthz
// keeps reporting alive (a draining process is healthy, just no longer accepting new work).
var draining atomic.Bool

func drainAwareReadiness(ready http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if draining.Load() {
			writeShuttingDown(w)
			return
		}
		ready(w, r)
	}
}

func writeShuttingDown(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte(`{"error":{"code":"SHUTTING_DOWN","message":"draining; not accepting new work"}}`))
}

// shutdownBudget resolves the drain lead and grace from the environment, with a FLOOR.
//
// The knobs are environment variables rather than governed config on purpose: a shutdown budget
// must be resolvable when the database is already gone. But an unfenced knob is a foot-gun — a
// dashboard edit setting the grace to 1s would silently re-create the severed-confirm bug with no
// code change and no test going red. So a grace below apiWriteTimeout is RAISED to the floor with
// a loud warning naming the consequence. Refusing to boot was the alternative; raising is chosen
// because a process that will not start is a worse outage than one that drains for longer.
func shutdownBudget(getenv func(string) string, log *slog.Logger) (lead, grace time.Duration) {
	lead, grace = defaultDrainLead, defaultShutdownGrace
	if v := getenv("TCP_API_DRAIN_LEAD"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			lead = d
		} else if log != nil {
			log.Warn("TCP_API_DRAIN_LEAD ignored (not a positive duration)", "value", v)
		}
	}
	if v := getenv("TCP_API_SHUTDOWN_GRACE"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			grace = d
		} else if log != nil {
			log.Warn("TCP_API_SHUTDOWN_GRACE ignored (not a positive duration)", "value", v)
		}
	}
	if grace < apiWriteTimeout {
		if log != nil {
			log.Warn("shutdown grace below the server's own WriteTimeout — raising to the floor; a shorter budget would sever an in-flight confirm the server had already authorised",
				"requested", grace, "floor", apiWriteTimeout)
		}
		grace = apiWriteTimeout
	}
	return lead, grace
}

// runServer serves until ctx is cancelled (SIGTERM/SIGINT), then drains gracefully:
// mark NOT-ready -> wait the lead so the load balancer removes this instance -> Shutdown, which
// stops accepting new connections and lets in-flight requests finish within the grace.
//
// onPhase (nil in production) reports the ordered phases so tests can assert HAPPENS-BEFORE
// ordering instead of racing a wall clock.
func runServer(ctx context.Context, srv *http.Server, lead, grace time.Duration, log *slog.Logger, onPhase func(string)) error {
	phase := func(p string) {
		if onPhase != nil {
			onPhase(p)
		}
	}
	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	draining.Store(true)
	phase("drain_begin")
	log.Info("shutdown signal received — draining", "lead", lead, "grace", grace)
	timer := time.NewTimer(lead)
	defer timer.Stop()
	<-timer.C
	phase("lead_elapsed")

	// context.WithoutCancel, not context.Background: the drain must NOT inherit the cancellation
	// that just fired (deriving from ctx would abort Shutdown immediately, cutting the very
	// in-flight requests this exists to protect), but it SHOULD keep the parent's values so
	// anything context-scoped during the drain still resolves.
	shutCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), grace)
	defer cancel()
	phase("shutdown_begin")
	err := srv.Shutdown(shutCtx)
	phase("shutdown_done")
	if err != nil {
		log.Error("graceful shutdown did not complete within the grace — in-flight work may have been cut", "err", err)
		return err
	}
	log.Info("graceful shutdown complete — all in-flight requests finished")
	return <-errCh
}

// newAPIServer builds the HTTP server with the full connection-lifecycle timeout set
// (BX-MED-005), so a slow or idle client cannot pin a goroutine/connection indefinitely.
// ReadHeaderTimeout/ReadTimeout bound the request read (bodies are tiny — 64KB
// MaxBytesReader); IdleTimeout reclaims idle keep-alives; WriteTimeout bounds the whole
// handler+write and is deliberately kept ABOVE apiMaxAdapterTimeout so a legitimate but
// slow confirm (blocked on the governed adapter) is never truncated mid-flight.
func newAPIServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      apiWriteTimeout,
		IdleTimeout:       120 * time.Second,
		// BX-MED-005: bound header SIZE as well as header TIME. Go's default is 1MB; this API's
		// largest legitimate header set is a handful of small values (Idempotency-Key,
		// X-Correlation-Id, an HMAC signature, a session cookie), so 64KiB is generous while
		// denying a cheap memory-amplification vector. Over the limit answers 431.
		MaxHeaderBytes: 64 << 10,
	}
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx := context.Background()

	mode := env("TCP_API_MODE", "all")
	p, err := resolvePlanes(mode)
	if err != nil {
		log.Error("api mode", "err", err)
		os.Exit(1)
	}
	log.Info("api mode", "mode", mode, "serve_data", p.serveData, "serve_control", p.serveControl)

	adminDSN := env("TCP_ADMIN_DSN", "postgres://postgres:devlocal@localhost:5434/telco_credit")
	appDSN := env("TCP_APP_DSN", "postgres://tcp_app:devlocal_app@localhost:5434/telco_credit")
	// BX-HIGH-012 Stage 1: config governance + the programme->telco resolver run on the
	// dedicated NON-BYPASSRLS tcp_config role (migration 0076), whose only cross-tenant reach
	// is a SELECT-only two-column programmes policy. Opened only in control mode. When
	// TCP_CONFIG_DSN is not explicitly set, DERIVE it from TCP_APP_DSN — same host/database,
	// user swapped to tcp_config — so any environment that already configures the app DSN boots
	// on the least-privilege role without a second DSN having to be wired first (a missing
	// TCP_CONFIG_DSN otherwise fell back to the localhost dev default and crash-looped on a
	// remote database). The password is TCP_CONFIG_PASSWORD, or the migration-seeded default.
	configDSN := env("TCP_CONFIG_DSN", "")
	if configDSN == "" {
		derived, derr := deriveRoleDSN(appDSN, "tcp_config", env("TCP_CONFIG_PASSWORD", "devlocal_config"))
		if derr != nil {
			log.Error("derive config DSN from app DSN failed; set TCP_CONFIG_DSN explicitly", "err", derr)
			os.Exit(1)
		}
		configDSN = derived
		log.Info("TCP_CONFIG_DSN not set — derived from TCP_APP_DSN with the tcp_config role (BX-HIGH-012)")
	}
	operatorDSN := env("TCP_OPERATOR_DSN", "postgres://tcp_operator:devlocal_operator@localhost:5434/telco_credit")
	addr := env("TCP_API_ADDR", ":8090")

	// #44 (VR-32 prod hardening): in production, block loopback + private-range egress
	// too — every legitimate outbound target (the real telco) is public. Default off so
	// dev/Render private-network traffic keeps working.
	egress.SetBlockPrivate(env("TCP_EGRESS_BLOCK_PRIVATE", "false") == "true")
	log.Info("egress guard", "block_private_ranges", egress.BlockPrivateEnabled())

	// Boot-time self-migration is the DEFAULT (project lesson: never depend on an external
	// deploy hook having run). BX-HIGH-012: in production the migrator + role-password
	// rotation run as a SEPARATE privileged job (cmd/migrate) so the request-serving API
	// process never opens the owner DSN — set TCP_API_SELF_MIGRATE=false there and run
	// cmd/migrate as a pre-start job / init container.
	if env("TCP_API_SELF_MIGRATE", "true") == "true" {
		adminPool, err := platform.NewPool(ctx, adminDSN)
		if err != nil {
			log.Error("admin db connect failed", "err", err)
			os.Exit(1)
		}
		if n, err := dbmigrate.Apply(ctx, adminPool, migrations.FS); err != nil {
			log.Error("migrate failed", "err", err)
			os.Exit(1)
		} else if n > 0 {
			log.Info("migrations applied", "count", n)
		}
		// Rotate role passwords from the environment (production must never run on the
		// dev passwords baked into 0001 — V2-SEC-005).
		if rotated, err := dbroles.ApplyPasswords(ctx, adminPool); err != nil {
			log.Error("role password rotation failed", "err", err)
			os.Exit(1)
		} else if len(rotated) > 0 {
			log.Info("role passwords applied from environment", "roles", rotated)
		}
		adminPool.Close()
	} else {
		log.Info("boot-time self-migration DISABLED (BX-HIGH-012) — migrations + role passwords must be applied by the separate cmd/migrate job before this API starts")
	}

	appPool, err := platform.NewPool(ctx, appDSN)
	if err != nil {
		log.Error("app db connect failed", "err", err)
		os.Exit(1)
	}
	defer appPool.Close()

	// Control-plane pools: the operator (RLS read-only) and config (least-privilege
	// governance/resolver) pools are opened ONLY when this process serves the control
	// plane. A pure data-plane process (TCP_API_MODE=data) never holds them — the
	// public surface carries no config/resolver privilege at all (BX-HIGH-012 Stage 2).
	var operatorPool, configPool *pgxpool.Pool
	if p.serveControl {
		operatorPool, err = platform.NewPool(ctx, operatorDSN)
		if err != nil {
			log.Error("operator db connect failed", "err", err)
			os.Exit(1)
		}
		defer operatorPool.Close()
		configPool, err = platform.NewPool(ctx, configDSN)
		if err != nil {
			log.Error("config db connect failed", "err", err)
			os.Exit(1)
		}
		defer configPool.Close()
	}

	// Governed money formatting: load the currency display scale (decimals + symbol)
	// ONCE from the seeded `currencies` table and hand it to the handler layer, so
	// toMoneyView renders major units (₦50.00) from a governed source, not a hardcoded
	// /100. Fail closed at boot — the reference table must be seeded (migration 0064).
	if formats, err := repo.LoadCurrencyFormats(ctx, appPool); err != nil {
		log.Error("load currency formats (required at boot)", "err", err)
		os.Exit(1)
	} else {
		handler.SetCurrencyFormats(formats)
		log.Info("currency formats loaded", "count", len(formats))
	}

	mux, err := buildMux(ctx, p, serverDeps{
		AppPool:      appPool,
		OperatorPool: operatorPool,
		ConfigPool:   configPool,
		Log:          log,
	})
	if err != nil {
		log.Error("build mux failed", "err", err)
		os.Exit(1)
	}

	// Gate B: baseline security headers on every response (SSRF-adjacent pen-test
	// hardening — a JSON API that serves no active content of its own).
	srv := newAPIServer(addr, handler.SecurityHeaders(mux))
	// BX-MED-005: signal-driven graceful shutdown. SIGTERM is what an orchestrator sends before it
	// kills a pod; without handling it, an in-flight confirm — which may already have instructed
	// the telco to credit a customer — is severed mid-flight. The handler is installed here,
	// AFTER migrations and pool setup: a SIGTERM arriving during boot still takes the default
	// disposition and kills the process, which is the correct answer while nothing is in flight
	// and a partially-applied migration must not be interrupted by our own drain logic.
	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	lead, grace := shutdownBudget(os.Getenv, log)
	bi := buildinfo.Info()
	log.Info("api listening", "addr", addr, "mode", mode, "version", bi.Version, "commit", bi.Commit)
	if err := runServer(sigCtx, srv, lead, grace, log, nil); err != nil {
		log.Error("server stopped", "err", err)
		os.Exit(1)
	}
}
