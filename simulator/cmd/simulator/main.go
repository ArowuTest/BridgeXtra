// cmd/simulator — standing telco simulator service (V2-SIM-001..012).
// All behaviour lives in simulator/sim so tests exercise the exact
// handler the service runs.
package main

import (
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/ArowuTest/telco-credit-platform/simulator/sim"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	hold, err := time.ParseDuration(env("SIM_HOLD", "30s"))
	if err != nil {
		log.Error("bad SIM_HOLD", "err", err)
		os.Exit(1)
	}
	s := sim.New(log, env("SIM_SEED", "m1"), hold)

	addr := env("SIM_ADDR", ":8091")
	srv := &http.Server{Addr: addr, Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	log.Info("simulator listening", "addr", addr, "seed", s.Seed, "timeout_hold", hold.String())
	if err := srv.ListenAndServe(); err != nil {
		log.Error("simulator stopped", "err", err)
		os.Exit(1)
	}
}
