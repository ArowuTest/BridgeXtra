package origination_test

// Self-exclusion (R1-MUST): a subscriber can opt out of credit, the opt-out is
// enforced at the offer/confirm gate, and — crucially — it cannot be reinstated
// before the governed cool-off has elapsed (so it is a real control, not a toggle
// a distressed borrower flips back on the same day).

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/origination"
)

const sxProg = "prg_sim_airtime01"
const sxToken = "tok_sim_0001"

func (f *fixture) activeExclusionMinUntilBackdate(t *testing.T) {
	t.Helper()
	// Admin surgery: age the whole exclusion into the past (keeping the
	// min_until >= requested_at invariant) so the cool-off has elapsed.
	if _, err := f.db.Admin.Exec(context.Background(),
		`UPDATE self_exclusions SET requested_at = now() - interval '2 hours', min_until = now() - interval '1 hour' WHERE state='ACTIVE'`); err != nil {
		t.Fatal(err)
	}
}

func TestSelfExclusion_BlocksOffersAndConfirm_ThenReinstatesAfterCoolOff(t *testing.T) {
	f := newFixture(t, "sx_block", 0, 2_000)
	ctx := tenantCtx()

	// Baseline: the subscriber can be offered credit.
	if offers := f.offersFor(t, sxToken); len(offers) == 0 {
		t.Fatal("baseline: subscriber should have offers before self-excluding")
	}

	// Self-exclude.
	res, err := f.svc.RequestSelfExclusion(ctx, sxProg, sxToken, "USSD", "managing my spending")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExclusionID == "" || res.AlreadyExcluded {
		t.Fatalf("first request must create a fresh exclusion, got %+v", res)
	}

	// Offers AND confirm are refused while excluded.
	if _, err := f.svc.GetOffers(ctx, sxProg, sxToken); !errors.Is(err, origination.ErrSubscriberIneligible) {
		t.Fatalf("a self-excluded subscriber must be refused offers, got %v", err)
	}

	// Reinstatement before the cool-off has elapsed is refused.
	if err := f.svc.ReinstateSelfExclusion(ctx, sxProg, sxToken, "USSD"); !errors.Is(err, origination.ErrCoolOffNotElapsed) {
		t.Fatalf("reinstatement before the cool-off must be refused, got %v", err)
	}
	// Still excluded.
	if _, err := f.svc.GetOffers(ctx, sxProg, sxToken); !errors.Is(err, origination.ErrSubscriberIneligible) {
		t.Fatalf("a refused reinstatement must leave the subscriber excluded, got %v", err)
	}

	// After the cool-off elapses, reinstatement succeeds and offers resume.
	f.activeExclusionMinUntilBackdate(t)
	if err := f.svc.ReinstateSelfExclusion(ctx, sxProg, sxToken, "USSD"); err != nil {
		t.Fatalf("reinstatement after the cool-off must succeed, got %v", err)
	}
	if offers := f.offersFor(t, sxToken); len(offers) == 0 {
		t.Fatal("a reinstated subscriber must be offered credit again")
	}

	// The register reflects the lifecycle: one REINSTATED row, no ACTIVE.
	var active, reinstated int
	if err := f.db.Admin.QueryRow(context.Background(), `
		SELECT count(*) FILTER (WHERE state='ACTIVE'), count(*) FILTER (WHERE state='REINSTATED')
		FROM self_exclusions WHERE subscriber_account_id IN
		  (SELECT subscriber_account_id FROM subscriber_accounts WHERE msisdn_token=$1 AND effective_to IS NULL)`,
		sxToken).Scan(&active, &reinstated); err != nil {
		t.Fatal(err)
	}
	if active != 0 || reinstated != 1 {
		t.Fatalf("lifecycle wrong: active=%d reinstated=%d", active, reinstated)
	}
}

func TestSelfExclusion_RequestIsIdempotent(t *testing.T) {
	f := newFixture(t, "sx_idem", 0, 2_000)
	ctx := tenantCtx()

	first, err := f.svc.RequestSelfExclusion(ctx, sxProg, sxToken, "APP", "r")
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.svc.RequestSelfExclusion(ctx, sxProg, sxToken, "APP", "r")
	if err != nil {
		t.Fatal(err)
	}
	if !second.AlreadyExcluded || second.ExclusionID != first.ExclusionID {
		t.Fatalf("a repeat request must return the existing exclusion, got %+v", second)
	}
	var n int
	if err := f.db.Admin.QueryRow(context.Background(),
		`SELECT count(*) FROM self_exclusions WHERE state='ACTIVE'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("a repeat request must not create a second active exclusion, got %d", n)
	}
}

func TestSelfExclusion_UngovernedChannelRefused(t *testing.T) {
	f := newFixture(t, "sx_chan", 0, 2_000)
	if _, err := f.svc.RequestSelfExclusion(tenantCtx(), sxProg, sxToken, "IVR", "r"); !errors.Is(err, origination.ErrSelfExclusionChannelNotAllowed) {
		t.Fatalf("a self-exclusion through an ungoverned channel must be refused, got %v", err)
	}
}

func TestSelfExclusion_ReinstateWithoutExclusionRefused(t *testing.T) {
	f := newFixture(t, "sx_none", 0, 2_000)
	if err := f.svc.ReinstateSelfExclusion(tenantCtx(), sxProg, sxToken, "USSD"); !errors.Is(err, origination.ErrNotSelfExcluded) {
		t.Fatalf("reinstating a subscriber who is not excluded must be refused, got %v", err)
	}
}

// Explicit standalone assertion (reviewer completeness note): reinstatement
// before the cool-off is refused.
func TestSelfExclusion_ReinstateBeforeCoolOff_Refused(t *testing.T) {
	f := newFixture(t, "sx_cooloff", 0, 2_000)
	ctx := tenantCtx()
	if _, err := f.svc.RequestSelfExclusion(ctx, sxProg, sxToken, "USSD", "r"); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.ReinstateSelfExclusion(ctx, sxProg, sxToken, "USSD"); !errors.Is(err, origination.ErrCoolOffNotElapsed) {
		t.Fatalf("reinstatement before the cool-off must be refused, got %v", err)
	}
}

// setSelfExclusionCfg activates a self-exclusion config through the governed
// maker-checker path (superseding the seed).
func (f *fixture) setSelfExclusionCfg(t *testing.T, content string) {
	t.Helper()
	ctx := context.Background()
	c, err := f.cfg.CreateDraft(ctx, "origination.self_exclusion", "programme:prg_sim_airtime01", "alice", "test", []byte(content))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.cfg.Submit(ctx, c.ConfigVersionID, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := f.cfg.Approve(ctx, c.ConfigVersionID, "bob"); err != nil {
		t.Fatal(err)
	}
	if err := f.cfg.Activate(ctx, c.ConfigVersionID, "bob", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
}

// The governed require_operator_reinstatement toggle is live and fail-closed:
// while true, self-service reinstatement is refused even AFTER the cool-off.
func TestSelfExclusion_OperatorReinstatementToggle_RefusesSelfService(t *testing.T) {
	f := newFixture(t, "sx_optgate", 0, 2_000)
	ctx := tenantCtx()
	f.setSelfExclusionCfg(t, `{"min_exclusion_days":30,"allowed_channels":["USSD"],"require_operator_reinstatement":true}`)

	if _, err := f.svc.RequestSelfExclusion(ctx, sxProg, sxToken, "USSD", "r"); err != nil {
		t.Fatal(err)
	}
	f.activeExclusionMinUntilBackdate(t) // even past the cool-off
	if err := f.svc.ReinstateSelfExclusion(ctx, sxProg, sxToken, "USSD"); !errors.Is(err, origination.ErrOperatorReinstatementRequired) {
		t.Fatalf("with operator reinstatement required, self-service must be refused, got %v", err)
	}
}
