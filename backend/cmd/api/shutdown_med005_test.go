package main

// BX-MED-005 (REOPENED): finish the HTTP lifecycle.
//
// The auditor kept liveness/readiness + timeouts and required: /version, MaxHeaderBytes,
// signal-driven SIGTERM/SIGINT shutdown, Server.Shutdown() with a bounded drain — and PROOF that
// an in-flight money request is allowed to finish during graceful shutdown.
//
// The proof below deliberately drives the REAL binary and sends a REAL SIGTERM. Cancelling a
// context in-process would test the drain logic while asserting nothing about signal handling —
// and "asserted, never proven" is exactly what got this finding reopened.

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
)

// The budget arithmetic, pinned. These are the relationships that make the drain SAFE; a future
// "tidy-up" that breaks one would sever an in-flight confirm, so they are asserted, not assumed.
func TestBXMED005_ShutdownBudgetArithmetic(t *testing.T) {
	if apiWriteTimeout <= apiMaxAdapterTimeout {
		t.Errorf("WriteTimeout (%s) must exceed the governed adapter ceiling (%s)", apiWriteTimeout, apiMaxAdapterTimeout)
	}
	// The drain budget must be at least as long as the server's OWN WriteTimeout: the server
	// authorises a request to run that long, so a shorter drain would cut work it had permitted.
	if defaultShutdownGrace < apiWriteTimeout {
		t.Errorf("shutdown grace (%s) must be >= WriteTimeout (%s), else a drain severs a request the server authorised",
			defaultShutdownGrace, apiWriteTimeout)
	}
	if defaultDrainLead <= 0 {
		t.Error("a drain lead is required so the load balancer removes this instance before connections close")
	}
	if srv := newAPIServer(":0", http.NewServeMux()); srv.MaxHeaderBytes <= 0 {
		t.Error("MaxHeaderBytes must be set (header-size amplification bound)")
	}
}

// The env overrides are a foot-gun without a floor: TCP_API_SHUTDOWN_GRACE=1s would silently
// re-create the severed-confirm bug through a dashboard edit, with no code change and no test
// going red. Mutation proof: delete the clamp in shutdownBudget and the "below floor" case returns
// 1s — RED.
func TestBXMED005_ShutdownBudgetFloor(t *testing.T) {
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	for name, tc := range map[string]struct {
		env       map[string]string
		wantLead  time.Duration
		wantGrace time.Duration
	}{
		"defaults":              {map[string]string{}, defaultDrainLead, defaultShutdownGrace},
		"honours valid values":  {map[string]string{"TCP_API_DRAIN_LEAD": "2s", "TCP_API_SHUTDOWN_GRACE": "300s"}, 2 * time.Second, 300 * time.Second},
		"below floor is raised": {map[string]string{"TCP_API_SHUTDOWN_GRACE": "1s"}, defaultDrainLead, apiWriteTimeout},
		"garbage ignored":       {map[string]string{"TCP_API_DRAIN_LEAD": "soon", "TCP_API_SHUTDOWN_GRACE": "-5s"}, defaultDrainLead, defaultShutdownGrace},
	} {
		t.Run(name, func(t *testing.T) {
			lead, grace := shutdownBudget(func(k string) string { return tc.env[k] }, log)
			if lead != tc.wantLead {
				t.Errorf("lead = %s, want %s", lead, tc.wantLead)
			}
			if grace != tc.wantGrace {
				t.Errorf("grace = %s, want %s", grace, tc.wantGrace)
			}
			if grace < apiWriteTimeout {
				t.Errorf("grace %s fell below the WriteTimeout floor %s", grace, apiWriteTimeout)
			}
		})
	}
}

// Drain ORDERING, proven by happens-before rather than by racing a wall clock: readiness must go
// NOT-ready and the lead must elapse BEFORE Shutdown begins closing anything, and an in-flight
// request must still complete. Mutation proof: call srv.Shutdown before flipping `draining` (or
// drop the lead wait) and the recorded phase order changes — RED.
func TestBXMED005_DrainOrdering_InFlightRequestCompletes(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /readyz", drainAwareReadiness(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	mux.HandleFunc("GET /slow", func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release // still running when the signal arrives
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`done`))
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	draining.Store(false)
	t.Cleanup(func() { draining.Store(false) })

	srv := newAPIServer(addr, mux)
	var phases []string
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runServer(ctx, srv, 150*time.Millisecond, 20*time.Second,
			slog.New(slog.NewJSONHandler(io.Discard, nil)),
			func(p string) { phases = append(phases, p) })
	}()

	base := "http://" + addr
	waitFor(t, func() bool { return statusOfURL(base+"/readyz") == http.StatusOK }, 10*time.Second, "server ready")

	// A request is in flight when the shutdown signal arrives.
	inflight := make(chan int, 1)
	go func() {
		resp, err := http.Get(base + "/slow")
		if err != nil {
			inflight <- -1
			return
		}
		defer func() { _ = resp.Body.Close() }()
		_, _ = io.ReadAll(resp.Body)
		inflight <- resp.StatusCode
	}()
	<-started

	cancel() // stands in for the signal HERE only; the real signal is proven in the subprocess test

	// Once draining, readiness must refuse — before anything is closed.
	waitFor(t, func() bool { return statusOfURL(base+"/readyz") == http.StatusServiceUnavailable },
		10*time.Second, "readiness reports NOT-ready while draining")

	close(release) // let the in-flight request finish
	if code := <-inflight; code != http.StatusOK {
		t.Fatalf("BX-MED-005: the in-flight request must COMPLETE during graceful shutdown, got %d", code)
	}
	if err := <-done; err != nil {
		t.Fatalf("runServer: %v", err)
	}
	want := []string{"drain_begin", "lead_elapsed", "shutdown_begin", "shutdown_done"}
	if strings.Join(phases, ",") != strings.Join(want, ",") {
		t.Fatalf("drain phase order = %v, want %v", phases, want)
	}
}

// THE SIGNAL PROOF. Builds the real binary, boots it, puts a real money request in flight (a
// confirm blocked on the MNO adapter via the simulator's credit-then-hang fault), sends a real
// SIGTERM, and asserts the in-flight confirm still returns its correct response while the process
// exits cleanly. Nothing here stands in for the signal.
func TestBXMED005_SIGTERM_DrainsAndCompletesInFlightConfirm(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGTERM delivery to a child process is not supported on Windows; proven on linux (CI + the Docker test runner)")
	}
	db := testutil.MustSetup(t, "med005_sigterm")
	ctx := context.Background()

	// The simulator holds the fulfilment response, so the confirm is genuinely mid-flight.
	simSrv, simURL := startHoldingSim(t, 3*time.Second)
	defer simSrv()

	cfgW := configsvc.New(db.Worker)
	content := fmt.Sprintf(`{"fulfilment_url":%q,"request_timeout_ms":10000,"retry_budget":0,"circuit_error_threshold_pct":50,"circuit_min_requests":20,"circuit_cooldown_seconds":30,"max_weekly_recharge_minor":100000000}`, simURL)
	c, err := cfgW.CreateDraft(ctx, "telco.adapter", "telco:SIM_NG", "alice", "med005 sim", []byte(content))
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []func() error{
		func() error { return cfgW.Submit(ctx, c.ConfigVersionID, "alice") },
		func() error { return cfgW.Approve(ctx, c.ConfigVersionID, "bob") },
		func() error { return cfgW.Activate(ctx, c.ConfigVersionID, "bob", time.Now().UTC()) },
	} {
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}
	db.SeedTelco(t, "SIM_NG", "sim-api-key")
	for _, s := range []string{
		`INSERT INTO subscriber_accounts (subscriber_account_id, telco_id, msisdn_token, status, nin_verified)
		 VALUES ('sub_m5','SIM_NG','tok_TIMEOUT_m5','ACTIVE',true)`,
		`INSERT INTO decision_snapshots (decision_snapshot_id, telco_id, subscriber_account_id,
		   max_face_value_minor, currency, config_version_id)
		 VALUES ('dec_m5','SIM_NG','sub_m5',50000,'NGN','cfg_seed_product_airtime_v1')`,
	} {
		if _, err := db.Admin.Exec(ctx, s); err != nil {
			t.Fatal(err)
		}
	}

	bin := filepath.Join(t.TempDir(), "tcp-api")
	build := exec.Command("go", "build", "-o", bin, "./cmd/api")
	build.Dir = repoBackendDir(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build cmd/api: %v\n%s", err, out)
	}

	port := freePort(t)
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(),
		"TCP_API_MODE=data",
		"TCP_API_SELF_MIGRATE=false",
		"TCP_API_ADDR="+fmt.Sprintf(":%d", port),
		"TCP_APP_DSN="+appDSNFor(db.Name),
		"TCP_API_DRAIN_LEAD=200ms",
	)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	killed := false
	t.Cleanup(func() {
		if !killed {
			_ = cmd.Process.Kill()
		}
	})

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	waitFor(t, func() bool { return statusOfURL(base+"/readyz") == http.StatusOK }, 60*time.Second, "api ready")

	// /version answers with real build identity.
	if code := statusOfURL(base + "/version"); code != http.StatusOK {
		t.Fatalf("/version = %d, want 200", code)
	}

	offerID, discRef := fetchOffer(t, base, "tok_TIMEOUT_m5")

	// Put the confirm in flight. It will block ~3s inside the adapter.
	type result struct {
		code int
		err  error
	}
	inflight := make(chan result, 1)
	go func() {
		code, err := postConfirm(t, base, offerID, discRef, "tok_TIMEOUT_m5", "med005-sigterm-1")
		inflight <- result{code, err}
	}()
	time.Sleep(600 * time.Millisecond) // the request is now inside the adapter call

	// THE REAL SIGNAL.
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}

	// Draining: readiness refuses so the LB removes this instance; liveness stays OK so an
	// orchestrator does not escalate to a kill mid-drain.
	waitFor(t, func() bool { return statusOfURL(base+"/readyz") == http.StatusServiceUnavailable },
		10*time.Second, "readiness refuses while draining")
	if code := statusOfURL(base + "/healthz"); code != http.StatusOK {
		t.Errorf("liveness must stay OK while draining (a draining process is healthy), got %d", code)
	}

	// THE ASSERTION: the in-flight money request completed with its real answer — not a reset,
	// not a truncated body.
	select {
	case r := <-inflight:
		if r.err != nil {
			t.Fatalf("BX-MED-005: the in-flight confirm must COMPLETE during graceful shutdown, got transport error: %v", r.err)
		}
		if r.code != http.StatusAccepted && r.code != http.StatusCreated {
			t.Fatalf("BX-MED-005: in-flight confirm returned %d, want 202/201", r.code)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("in-flight confirm never returned during graceful shutdown")
	}

	waitErr := cmd.Wait()
	killed = true
	if waitErr != nil {
		t.Fatalf("api must exit cleanly after a graceful drain, got %v", waitErr)
	}
}
