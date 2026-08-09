package main

import "testing"

// BX-HIGH-004/005: a reconciliation pass is clean ONLY when there are zero breaks AND no
// armed RECOVERY layer failed to run. A required (armed) layer that could not reconcile
// must not be certified clean just because it produced zero breaks — that silence is
// missing coverage, not a match. Mutation proof: drop the recoveryFailures branch in
// reconExitCode and the "failed armed recovery layer" case returns 0 (falsely clean).
func TestReconExitCode(t *testing.T) {
	for _, c := range []struct {
		name             string
		breaks, recFails int
		wantCode         int
	}{
		{"clean", 0, 0, 0},
		{"breaks fail the run", 5, 0, 1},
		{"a failed armed recovery layer is NOT clean (zero breaks)", 0, 1, 1},
		{"both breaks and a recovery failure", 2, 3, 1},
	} {
		if code, msg := reconExitCode(c.breaks, c.recFails); code != c.wantCode {
			t.Errorf("%s: reconExitCode(%d,%d)=%d (%q), want %d", c.name, c.breaks, c.recFails, code, msg, c.wantCode)
		}
	}
}
