package main

// BX-MED-005: (1) liveness (/healthz) is DB-free while readiness (/readyz) is DB-dependent, so a
// transient DB outage drains an instance from the load balancer instead of killing the pod; and
// (2) the HTTP server sets the full connection-lifecycle timeout set, with WriteTimeout above the
// governed MNO adapter ceiling so a slow-but-legitimate confirm is never truncated.

import (
	"context"
	"net/http"
	"testing"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
)

func TestBXMED005_LivenessAndReadinessRoutes(t *testing.T) {
	db := testutil.MustSetup(t, "med005_health")
	// Data plane with a live app pool: both probes present, both 200 (DB is up in the test).
	mux, err := buildMux(context.Background(), planes{serveData: true},
		serverDeps{AppPool: db.App, OperatorPool: nil, ConfigPool: nil, Log: testLogger()})
	if err != nil {
		t.Fatal(err)
	}
	if code := statusOf(t, mux, "GET", "/healthz"); code != http.StatusOK {
		t.Fatalf("/healthz (liveness) = %d, want 200", code)
	}
	if code := statusOf(t, mux, "GET", "/readyz"); code != http.StatusOK {
		t.Fatalf("/readyz (readiness) = %d, want 200", code)
	}
}

// The timeout contract, as a regression fence: every phase is bounded, and WriteTimeout stays above
// the governed adapter ceiling. Mutation proof: drop any timeout (0) or set WriteTimeout <=
// apiMaxAdapterTimeout and this fails — the exact ways MED-005 could silently regress.
func TestBXMED005_ServerTimeoutsBounded(t *testing.T) {
	srv := newAPIServer(":0", http.NewServeMux())
	if srv.ReadHeaderTimeout <= 0 {
		t.Error("ReadHeaderTimeout must be set (slowloris header bound)")
	}
	if srv.ReadTimeout <= 0 {
		t.Error("ReadTimeout must be set (slow-body bound)")
	}
	if srv.IdleTimeout <= 0 {
		t.Error("IdleTimeout must be set (reclaim idle keep-alives)")
	}
	if srv.WriteTimeout <= 0 {
		t.Fatal("WriteTimeout must be set (slow-reader bound)")
	}
	// A confirm blocks synchronously on the governed adapter (<=60s). If WriteTimeout did not exceed
	// that, a legitimate slow confirm would be cut off mid-flight — a correctness bug, not just DoS
	// hardening. This is the load-bearing relationship.
	if srv.WriteTimeout <= apiMaxAdapterTimeout {
		t.Fatalf("WriteTimeout (%s) must exceed the governed adapter ceiling (%s) so a slow confirm is not truncated",
			srv.WriteTimeout, apiMaxAdapterTimeout)
	}
}
