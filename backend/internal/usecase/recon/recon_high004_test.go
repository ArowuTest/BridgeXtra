package recon_test

// BX-HIGH-004: the worker's exit-code / clean-log gate summed only 3 of the 7 recon
// break classes (main.go:557/578), so a fulfilment CurrencyMismatch / Malformed /
// DuplicateTelco / Contradictory break could not fail the run — recon could certify
// "clean" with open breaks. Summary.BreakCount() is now the single source of truth for
// all sites (worker exit, per-run tally, re-sweep tally, recovery tally). This unit test
// pins that it counts ALL seven; that the fulfilment layer actually produces the four
// previously-dropped classes is covered by recon_rp06b / recon_rp06d1 / recon_rp34.

import (
	"testing"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/recon"
)

func TestBXHIGH004_BreakCountCoversAllSevenClasses(t *testing.T) {
	for _, c := range []struct {
		name string
		sum  recon.Summary
		want int
	}{
		{"missing_platform", recon.Summary{MissingPlatform: 1}, 1},
		{"missing_telco", recon.Summary{MissingTelco: 1}, 1},
		{"amount_mismatch", recon.Summary{AmountMismatch: 1}, 1},
		{"currency_mismatch (was dropped from the exit gate)", recon.Summary{CurrencyMismatch: 1}, 1},
		{"malformed (was dropped)", recon.Summary{Malformed: 1}, 1},
		{"duplicate_telco (was dropped)", recon.Summary{DuplicateTelco: 1}, 1},
		{"contradictory (was dropped)", recon.Summary{Contradictory: 1}, 1},
		{"all seven", recon.Summary{MissingPlatform: 1, MissingTelco: 1, AmountMismatch: 1, CurrencyMismatch: 1, Malformed: 1, DuplicateTelco: 1, Contradictory: 1}, 7},
		{"matched-only is clean", recon.Summary{Matched: 100}, 0},
	} {
		if got := c.sum.BreakCount(); got != c.want {
			t.Errorf("%s: BreakCount()=%d, want %d", c.name, got, c.want)
		}
	}
}
