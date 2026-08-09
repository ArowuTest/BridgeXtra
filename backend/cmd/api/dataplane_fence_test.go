package main

import (
	"os"
	"strings"
	"testing"
)

// TestDataPlaneFence_NoWorkerPoolInAPI (BX-HIGH-012 Part B, Stage 1 of the
// control-plane / data-plane split): the request-serving API must NOT construct the
// BYPASSRLS worker/owner database pool. Config governance and the programme->telco
// resolver run on the least-privilege, NON-BYPASSRLS tcp_config role (migration
// 0076), so a compromise of this internet-facing process holds no credential that
// can bypass a tenant RLS policy and read/write another tenant's money data.
//
// This structural guard fails the build if the worker DSN or pool is ever wired
// back into cmd/api, locking in the Stage-1 property until Stage 2 enforces it by
// deployment topology (a public data-plane instance carrying only tcp_app +
// tcp_operator). The forbidden tokens are worker-pool-specific — the word
// "BYPASSRLS" itself appears in comments here that explain its ABSENCE, so it is
// deliberately not a forbidden token.
func TestDataPlaneFence_NoWorkerPoolInAPI(t *testing.T) {
	forbidden := []string{"TCP_WORKER_DSN", "tcp_worker", "workerPool"}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read cmd/api dir: %v", err)
	}
	var scanned int
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		scanned++
		src := string(b)
		for _, tok := range forbidden {
			if strings.Contains(src, tok) {
				t.Errorf("%s references %q — the request-serving API must not hold a BYPASSRLS worker pool (BX-HIGH-012); config + resolver run on the tcp_config role", name, tok)
			}
		}
	}
	if scanned == 0 {
		t.Fatal("data-plane fence scanned no source files (cmd/api layout changed?) — the fence would be vacuously green")
	}
}
