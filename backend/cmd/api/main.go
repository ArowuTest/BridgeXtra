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
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform/dbmigrate"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
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
	addr := env("TCP_API_ADDR", ":8090")

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
	adminPool.Close()

	appPool, err := platform.NewPool(ctx, appDSN)
	if err != nil {
		log.Error("app db connect failed", "err", err)
		os.Exit(1)
	}
	defer appPool.Close()

	telcos := &repo.Telcos{Pool: appPool}
	auth := &handler.TenantAuth{Telcos: telcos, Pool: appPool, Log: log}
	programmes := repo.Programmes{}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handler.Health(appPool))
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
