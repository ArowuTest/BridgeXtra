package main

// BX-HIGH-012 Part B, Stage 2 — the data-plane / control-plane route split. buildMux
// is the SINGLE place the split is enforced, so these tests pin it directly: a
// data-plane process mounts the channel/webhook/programmes surface and NONE of the
// portal, and needs no operator/config pool to do so (they are passed nil); a
// control-plane process mounts the portal and NONE of the data-plane routes. This is
// the structural guarantee behind deploying the data plane on the public internet and
// the control plane internal-only.

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
)

func testLogger() *slog.Logger { return slog.New(slog.NewJSONHandler(io.Discard, nil)) }

// statusOf issues a request against the mux and returns the status code. A route
// that is MOUNTED but rejects the unauthenticated/empty request answers 4xx (not
// 404); a route that is NOT mounted answers 404. That contrast is what these tests
// assert on — presence vs absence, not the specific auth outcome.
func statusOf(t *testing.T, mux http.Handler, method, path string) int {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec.Code
}

var (
	dataRoutes = []struct{ m, p string }{
		{"POST", "/v1/offers"},
		{"POST", "/v1/advances"},
		{"GET", "/v1/advances/x"},
		{"GET", "/v1/programmes"},
		{"POST", "/v1/telcos/mtn/recharge-webhook"},
	}
	controlRoutes = []struct{ m, p string }{
		{"POST", "/v1/portal/login"},
		{"POST", "/v1/portal/logout"},
	}
)

func TestBuildMux_DataPlane_OmitsControlAndNeedsNoControlPools(t *testing.T) {
	db := testutil.MustSetup(t, "buildmux_data")
	// A pure data plane gets ONLY the app pool; operator/config pools are nil. If
	// buildMux(data) dereferenced them it would panic here — proving the data plane
	// neither opens nor needs the config/resolver privilege.
	mux, err := buildMux(context.Background(), planes{serveData: true},
		serverDeps{AppPool: db.App, OperatorPool: nil, ConfigPool: nil, Log: testLogger()})
	if err != nil {
		t.Fatalf("buildMux(data): %v", err)
	}
	if code := statusOf(t, mux, "GET", "/healthz"); code != http.StatusOK {
		t.Fatalf("/healthz = %d, want 200", code)
	}
	for _, r := range dataRoutes {
		if code := statusOf(t, mux, r.m, r.p); code == http.StatusNotFound {
			t.Errorf("data-plane must mount %s %s, got 404", r.m, r.p)
		}
	}
	for _, r := range controlRoutes {
		if code := statusOf(t, mux, r.m, r.p); code != http.StatusNotFound {
			t.Errorf("data-plane must NOT mount control route %s %s, got %d", r.m, r.p, code)
		}
	}
}

func TestBuildMux_ControlPlane_OmitsDataRoutes(t *testing.T) {
	db := testutil.MustSetup(t, "buildmux_control")
	mux, err := buildMux(context.Background(), planes{serveControl: true},
		serverDeps{AppPool: db.App, OperatorPool: db.Operator, ConfigPool: db.Config, Log: testLogger()})
	if err != nil {
		t.Fatalf("buildMux(control): %v", err)
	}
	if code := statusOf(t, mux, "GET", "/healthz"); code != http.StatusOK {
		t.Fatalf("/healthz = %d, want 200", code)
	}
	for _, r := range controlRoutes {
		if code := statusOf(t, mux, r.m, r.p); code == http.StatusNotFound {
			t.Errorf("control-plane must mount %s %s, got 404", r.m, r.p)
		}
	}
	for _, r := range dataRoutes {
		if code := statusOf(t, mux, r.m, r.p); code != http.StatusNotFound {
			t.Errorf("control-plane must NOT mount data route %s %s, got %d", r.m, r.p, code)
		}
	}
}

// "all" is the default, back-compat mode: both surfaces on one mux (no route
// collision — data is /v1/offers|advances|programmes|telcos/.., control is /v1/portal/*).
func TestBuildMux_AllMode_MountsBoth(t *testing.T) {
	db := testutil.MustSetup(t, "buildmux_all")
	mux, err := buildMux(context.Background(), planes{serveData: true, serveControl: true},
		serverDeps{AppPool: db.App, OperatorPool: db.Operator, ConfigPool: db.Config, Log: testLogger()})
	if err != nil {
		t.Fatalf("buildMux(all): %v", err)
	}
	for _, r := range append(append([]struct{ m, p string }{}, dataRoutes...), controlRoutes...) {
		if code := statusOf(t, mux, r.m, r.p); code == http.StatusNotFound {
			t.Errorf("all-mode must mount %s %s, got 404", r.m, r.p)
		}
	}
}

// deriveRoleDSN swaps the role/password while preserving host, port, database and params —
// so the config pool can be derived from TCP_APP_DSN when TCP_CONFIG_DSN is unset (the fix for
// the Render deploy that crash-looped on the localhost dev default).
func TestDeriveRoleDSN(t *testing.T) {
	got, err := deriveRoleDSN("postgres://tcp_app:apppass@db.internal:5432/telco_credit?sslmode=require", "tcp_config", "cfgpass")
	if err != nil {
		t.Fatal(err)
	}
	const want = "postgres://tcp_config:cfgpass@db.internal:5432/telco_credit?sslmode=require"
	if got != want {
		t.Fatalf("deriveRoleDSN = %q, want %q", got, want)
	}
	if _, err := deriveRoleDSN("://not a dsn", "tcp_config", "x"); err == nil {
		t.Fatal("a malformed template DSN must error, not silently produce a bad DSN")
	}
}

func TestResolvePlanes(t *testing.T) {
	cases := []struct {
		mode       string
		data, ctrl bool
		wantErr    bool
	}{
		{"all", true, true, false},
		{"", true, true, false},
		{"data", true, false, false},
		{"control", false, true, false},
		{"bogus", false, false, true},
	}
	for _, c := range cases {
		p, err := resolvePlanes(c.mode)
		if c.wantErr {
			if err == nil {
				t.Errorf("resolvePlanes(%q): want error, got nil", c.mode)
			}
			continue
		}
		if err != nil {
			t.Errorf("resolvePlanes(%q): unexpected error %v", c.mode, err)
			continue
		}
		if p.serveData != c.data || p.serveControl != c.ctrl {
			t.Errorf("resolvePlanes(%q) = %+v, want data=%v control=%v", c.mode, p, c.data, c.ctrl)
		}
	}
}
