package settlement_test

// BX-HIGH-011: settlement.Generate resolved settlement.terms at time.Now(), so a statement
// generated (or backfilled) after a terms renegotiation applied the NEW terms to an OLD
// period. It now resolves at periodStart — the contractual instant the period's obligations
// began. (Verify already re-uses the pinned TermsVersionID, so only Generate was affected.)

import (
	"context"
	"testing"
	"time"
)

func TestBXHIGH011_TermsResolvedAtPeriodStart_NotGenerationTime(t *testing.T) {
	set, _, _, _ := setup(t, "settle_h011")

	// The seed terms are in force from 2020 (migration 0075). A settlement for a period
	// ENTIRELY BEFORE that has no terms in force during it — Generate resolves at periodStart
	// and must REFUSE, never silently apply today's (currently-active) terms to an old period.
	// Mutation proof: revert Generate to time.Now() and this period resolves the live terms
	// and no longer errors.
	pStart := time.Date(2019, 3, 1, 0, 0, 0, 0, time.UTC)
	pEnd := time.Date(2019, 4, 1, 0, 0, 0, 0, time.UTC)
	if _, err := set.Generate(context.Background(), "SIM_NG", "prg_sim_airtime01", pStart, pEnd); err == nil {
		t.Fatal("a settlement for a period before any terms were in force must refuse (BX-HIGH-011: resolve terms at periodStart, not generation time)")
	}
}
