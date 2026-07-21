// cmd/api — platform API service. M0 scope: boot-time self-migration, tenant
// middleware, health, and a tenant-scoped programmes endpoint proving the
// context flows end to end.
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/handler"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/ledger"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/mno"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform/dbmigrate"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform/dbroles"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform/egress"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/ops"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/origination"
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

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx := context.Background()

	adminDSN := env("TCP_ADMIN_DSN", "postgres://postgres:devlocal@localhost:5434/telco_credit")
	appDSN := env("TCP_APP_DSN", "postgres://tcp_app:devlocal_app@localhost:5434/telco_credit")
	workerDSN := env("TCP_WORKER_DSN", "postgres://tcp_worker:devlocal_worker@localhost:5434/telco_credit")
	addr := env("TCP_API_ADDR", ":8090")

	// #44 (VR-32 prod hardening): in production, block loopback + private-range
	// egress too — every legitimate outbound target (the real telco) is public.
	// Default off so dev/Render private-network traffic keeps working.
	egress.SetBlockPrivate(env("TCP_EGRESS_BLOCK_PRIVATE", "false") == "true")
	log.Info("egress guard", "block_private_ranges", egress.BlockPrivateEnabled())

	// Boot-time self-migration (project lesson: never depend on an external
	// deploy hook having run).
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
	// Rotate role passwords from the environment (production must never run
	// on the dev passwords baked into 0001 — V2-SEC-005).
	if rotated, err := dbroles.ApplyPasswords(ctx, adminPool); err != nil {
		log.Error("role password rotation failed", "err", err)
		os.Exit(1)
	} else if len(rotated) > 0 {
		log.Info("role passwords applied from environment", "roles", rotated)
	}
	adminPool.Close()

	appPool, err := platform.NewPool(ctx, appDSN)
	if err != nil {
		log.Error("app db connect failed", "err", err)
		os.Exit(1)
	}
	defer appPool.Close()

	// Config writes run under the worker role (INSERT/UPDATE on config_versions
	// granted there in M0; dedicated admin role arrives with M4 RBAC).
	workerPool, err := platform.NewPool(ctx, workerDSN)
	if err != nil {
		log.Error("worker db connect failed", "err", err)
		os.Exit(1)
	}
	defer workerPool.Close()

	telcos := &repo.Telcos{Pool: appPool}
	auth := &handler.TenantAuth{Telcos: telcos, Pool: appPool, Log: log}
	programmes := repo.Programmes{}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handler.Health(appPool))

	// Config is managed ONLY through the portal — RBAC- (ADMIN-only for
	// mutations) and scope-gated. The former header-authenticated admin config
	// API was removed in EXT-1: it was a role-unaware parallel door to the same
	// configsvc. Any future config automation gets a distinctly-classified
	// service principal on this same RBAC chain, never a header bypass.

	// Channel + recovery services (M1 walking skeleton; recovery is shared
	// with the portal's M4e parked-reversal retry — ONE money core, not two).
	appCfg := configsvc.New(appPool)
	led := ledger.New(appCfg)
	orig := origination.New(appPool, appCfg, led, mno.NewHTTPAdapter(appCfg), log)
	rec := recovery.New(appPool, appCfg, led, log)

	// R-P0-8: inbound rate limiter from governed config — fail-closed: the API
	// refuses to boot without it, so no surface ever runs unlimited.
	limiter, trustedProxies, err := handler.LoadRateLimiter(ctx, configsvc.New(workerPool))
	if err != nil {
		log.Error("rate limiter (required at boot)", "err", err)
		os.Exit(1)
	}

	// M4a portal: session auth (httpOnly + CSRF) with deny-by-default RBAC.
	portal := &handler.Portal{
		Admins:   &repo.Admins{Pool: appPool},
		Sessions: &repo.PortalSessions{Pool: appPool},
		Config:   configsvc.New(workerPool),
		// Re-arm actions run as the app role in a tenant tx; operator reads span
		// telcos on the worker pool (BYPASSRLS) bounded by the operator's scope.
		Treasury:          treasury.New(appPool, configsvc.New(appPool), log),
		Ops:               ops.New(appPool, configsvc.New(appPool), log),
		Settlement:        settlement.New(appPool, configsvc.New(appPool), log),
		Recovery:          rec,
		Demo:              ops.NewDemo(appPool, appCfg, orig, log),
		ReadPool:          workerPool,
		Limiter:           limiter,
		TrustedProxyCount: trustedProxies,
		Log:               log,
	}
	portal.Mount(mux)
	channel := &handler.Channel{Origination: orig, Recovery: rec, Limiter: limiter, TrustedProxyCount: trustedProxies, Log: log}
	channel.Mount(mux, auth)
	mux.Handle("GET /v1/programmes", auth.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		var out []entity.Programme
		err := repo.WithTenantTx(ctx, appPool, func(tx pgx.Tx) error {
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

	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Info("api listening", "addr", addr)
	if err := srv.ListenAndServe(); err != nil {
		log.Error("server stopped", "err", err)
		os.Exit(1)
	}
}
