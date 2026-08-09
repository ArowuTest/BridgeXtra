package origination_test

// BX-HIGH-010: a pre-send adapter failure (a missing outbound-auth secret — the request is
// NEVER dialled) must FAIL the advance and RELEASE the reservation, not park it as a zombie
// FULFILMENT_UNKNOWN that holds exposure awaiting an enquiry for a request never sent. The
// adapter classifies the non-send as FAILED+NotSent; ResolveOutcome then releases the pool.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
)

func TestBXHIGH010_PreSendFailure_ReleasesReservation_NotZombieUnknown(t *testing.T) {
	f := newFixture(t, "orig_h010", 0, 2_000)
	ctx := context.Background()

	// Reconfigure the SIM_NG adapter to require an apikey whose secret env is UNSET, so
	// SubmitFulfilment fails at applyAuth BEFORE dialling — a definite non-send. (SIM_NG is
	// synthetic, so the BX-HIGH-009 realism check does not require https here.)
	content := fmt.Sprintf(`{"fulfilment_url":%q,"request_timeout_ms":2000,"retry_budget":0,"circuit_error_threshold_pct":50,"circuit_min_requests":20,"circuit_cooldown_seconds":30,"max_weekly_recharge_minor":100000000,"auth":{"scheme":"apikey","header":"X-Api-Key","secret_env":"TCP_H010_ABSENT"}}`, f.simURL)
	c, err := f.cfg.CreateDraft(ctx, "telco.adapter", "telco:SIM_NG", "alice", "h010 apikey missing secret", []byte(content))
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []func() error{
		func() error { return f.cfg.Submit(ctx, c.ConfigVersionID, "alice") },
		func() error { return f.cfg.Approve(ctx, c.ConfigVersionID, "bob") },
		func() error { return f.cfg.Activate(ctx, c.ConfigVersionID, "bob", time.Now().UTC()) },
	} {
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}

	f.seedSubscriber(t, "sub_h010", "tok_h010", 50_000)
	offer := f.offersFor(t, "tok_h010")[0]
	res, err := f.svc.Confirm(tenantCtx(), acceptFor(offer, "tok_h010", "h010-1", "cor-h010"))
	if err != nil {
		t.Fatal(err)
	}

	// The advance FAILED (released), NOT a zombie FULFILMENT_UNKNOWN.
	if res.Advance.State != entity.AdvFulfilmentFailed {
		t.Fatalf("a pre-send auth failure must FAIL the advance (release), got %s", res.Advance.State)
	}
	// The reservation is released — the pool holds nothing for this never-sent advance.
	if reserved, utilised := f.poolState(t); reserved != 0 || utilised != 0 {
		t.Fatalf("a not-sent advance must release its reservation: reserved=%d utilised=%d", reserved, utilised)
	}
}
