package settlement_test

// BX-HIGH-011: settlement.Generate resolved settlement.terms at time.Now(), so a statement
// generated (or backfilled) after a terms renegotiation applied the NEW terms to an OLD
// period. It now resolves at periodStart — the contractual instant the period's obligations
// began — and refuses open/future (not-yet-closed) periods. (Verify already re-uses the
// pinned TermsVersionID, so only Generate was affected.)

import (
	"context"
	"testing"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
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

func TestBXHIGH011_RejectsOpenFuturePeriod(t *testing.T) {
	set, _, _, _ := setup(t, "settle_h011_future")

	// A statement is a deterministic function of the ledger IN the period. A period whose end
	// is in the future is still accumulating, so a statement generated now would not reproduce
	// once more events land in it — Generate must refuse it. Mutation proof: drop the
	// period-closed guard and this future period is accepted.
	end := time.Now().UTC().Add(48 * time.Hour)
	start := end.Add(-24 * time.Hour)
	if _, err := set.Generate(context.Background(), "SIM_NG", "prg_sim_airtime01", start, end); err == nil {
		t.Fatal("a settlement for an open/future (not-yet-closed) period must be refused (BX-HIGH-011)")
	}
}

// The terms-change boundary: after a renegotiation, an OLD closed period must still pin the
// version in force DURING it — not the currently-active version — and the half-open boundary
// belongs to the new version. Both Generate calls run "now", so any generation-time leakage
// would collapse both to v2.
func TestBXHIGH011_TermsAnchoredAcrossRenegotiationBoundary(t *testing.T) {
	set, _, _, db := setup(t, "settle_h011_boundary")
	ctx := context.Background()
	cfgW := configsvc.New(db.Worker)

	// Renegotiate: activate a v2 of the programme's settlement.terms at a boundary instant T
	// in mid-2021 (the seed v1 is effective from 2020 per migration 0075).
	boundary := time.Date(2021, 6, 1, 0, 0, 0, 0, time.UTC)
	v2, err := cfgW.CreateDraft(ctx, "settlement.terms", "programme:prg_sim_airtime01", "alice",
		"renegotiated share",
		[]byte(`{"cycle":"MONTHLY","telco_share_bps":4000,"platform_share_bps":6000,"taxes":[{"code":"VAT","bps":750}],"tolerance_minor":0}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfgW.Submit(ctx, v2.ConfigVersionID, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := cfgW.Approve(ctx, v2.ConfigVersionID, "bob"); err != nil {
		t.Fatal(err)
	}
	if err := cfgW.Activate(ctx, v2.ConfigVersionID, "bob", boundary); err != nil {
		t.Fatal(err)
	}

	// A CLOSED period entirely BEFORE the boundary pins v1 (the terms in force during it), NOT
	// v2 — even though v2 is the currently-active version at generation time.
	before, err := set.Generate(ctx, "SIM_NG", "prg_sim_airtime01",
		time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2021, 2, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if before.TermsVersionID != "cfg_seed_settlement_terms_v1" {
		t.Fatalf("period before the renegotiation must pin v1 (in force during it), got %s", before.TermsVersionID)
	}

	// A CLOSED period entirely AFTER the boundary pins v2.
	after, err := set.Generate(ctx, "SIM_NG", "prg_sim_airtime01",
		time.Date(2021, 7, 1, 0, 0, 0, 0, time.UTC), time.Date(2021, 8, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if after.TermsVersionID != v2.ConfigVersionID {
		t.Fatalf("period after the renegotiation must pin v2 (%s), got %s", v2.ConfigVersionID, after.TermsVersionID)
	}

	// The two periods resolve DIFFERENT versions — the boundary is real and period-anchored,
	// not generation-time (both calls ran just now, when v2 is active).
	if before.TermsVersionID == after.TermsVersionID {
		t.Fatal("terms must differ across the renegotiation boundary (period-anchored, not generation-time)")
	}
}
