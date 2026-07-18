package origination_test

// M3d pack (V1-TRE, EDG-024/025): the daily-disbursement guardrail trips
// under a mass-approval surge — sequential and CONCURRENT — suspending the
// programme fail-closed with trip evidence; re-arming is a two-person
// decision; recovery is config-driven (a governed limit raise, no deploy).

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/origination"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/treasury"
)

// activateGuardrails pushes a treasury.guardrails version through the FULL
// governed flow — exactly how an operator changes limits in production.
func (f *fixture) activateGuardrails(t *testing.T, dailyCapMinor int64) {
	t.Helper()
	ctx := context.Background()
	content := fmt.Sprintf(`{"max_daily_disbursed_minor":%d,"max_open_exposure_bps_of_committed":8000,"trip_action":"SUSPEND_PROGRAMME","rearm":"MAKER_CHECKER"}`, dailyCapMinor)
	c, err := f.cfg.CreateDraft(ctx, "treasury.guardrails", "programme:prg_sim_airtime01", "alice", "limit change", []byte(content))
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

func (f *fixture) confirmFor(t *testing.T, token, idem string) (origination.ConfirmResult, error) {
	t.Helper()
	offers, err := f.svc.GetOffers(tenantCtx(), "prg_sim_airtime01", token)
	if err != nil {
		return origination.ConfirmResult{}, err
	}
	return f.svc.Confirm(tenantCtx(), origination.ConfirmCmd{
		ProgrammeID: "prg_sim_airtime01", OfferID: offers[0].OfferID, MSISDNToken: token,
		IdemKey: idem, CorrelationID: "cor-" + idem,
	})
}

// EDG-024: sequential mass approval — the third ₦50 confirm (disbursed 4500
// each) pushes the day past a 12,000-kobo cap: trip, suspend, refuse-fast,
// two-person re-arm, config-driven recovery.
func TestEDG024_DailyCapSurge_TripSuspendRearm(t *testing.T) {
	f := newFixture(t, "m3d_surge", 0, 2_000)
	f.activateGuardrails(t, 12_000)
	for i := 1; i <= 4; i++ {
		f.seedSubscriber(t, fmt.Sprintf("sub_g%d", i), fmt.Sprintf("tok_g%d", i), 50_000)
	}
	ctx := context.Background()

	// Two confirms land (9,000 disbursed today).
	for i := 1; i <= 2; i++ {
		if _, err := f.confirmFor(t, fmt.Sprintf("tok_g%d", i), fmt.Sprintf("g-%d", i)); err != nil {
			t.Fatalf("confirm %d: %v", i, err)
		}
	}
	// The third breaches (13,500 > 12,000): declined customer-safe, trip
	// recorded, programme suspended.
	_, err := f.confirmFor(t, "tok_g3", "g-3")
	if !errors.Is(err, treasury.ErrProgrammeSuspended) {
		t.Fatalf("breaching confirm must decline as suspended: %v", err)
	}
	var guardrail, tripState, progStatus, tripID string
	if err := f.db.Admin.QueryRow(ctx, `
		SELECT t.trip_id, t.guardrail, t.state, p.status
		FROM guardrail_trips t JOIN programmes p ON p.programme_id = t.programme_id`).
		Scan(&tripID, &guardrail, &tripState, &progStatus); err != nil {
		t.Fatal(err)
	}
	if guardrail != "DAILY_DISBURSED" || tripState != "TRIPPED" || progStatus != "SUSPENDED" {
		t.Fatalf("trip evidence + suspension required: %s/%s/%s", guardrail, tripState, progStatus)
	}
	// The breaching confirm itself left NO advance behind (clean abort).
	var g3advances int
	if err := f.db.Admin.QueryRow(ctx, `
		SELECT count(*) FROM advances a JOIN subscriber_accounts s USING (subscriber_account_id)
		WHERE s.msisdn_token='tok_g3'`).Scan(&g3advances); err != nil {
		t.Fatal(err)
	}
	if g3advances != 0 {
		t.Fatalf("aborted confirm must leave no advance: %d", g3advances)
	}

	// Fail-fast while suspended: offers AND confirms refuse.
	if _, err := f.svc.GetOffers(tenantCtx(), "prg_sim_airtime01", "tok_g4"); !errors.Is(err, treasury.ErrProgrammeSuspended) {
		t.Fatalf("suspended programme must refuse offers: %v", err)
	}
	if _, err := f.confirmFor(t, "tok_g4", "g-4"); !errors.Is(err, treasury.ErrProgrammeSuspended) {
		t.Fatalf("suspended programme must refuse confirms: %v", err)
	}

	// Re-arm: maker requests; SELF-approval refused by the schema; distinct
	// approver restores the programme.
	tre := treasury.New(f.db.App, configsvc.New(f.db.App), slog.Default())
	if err := tre.RequestRearm(tenantCtx(), "SIM_NG", tripID, "carol", "surge investigated: promo burst"); err != nil {
		t.Fatal(err)
	}
	if err := tre.ApproveRearm(tenantCtx(), "SIM_NG", tripID, "carol"); !errors.Is(err, repo.ErrSelfRearm) {
		t.Fatalf("self re-arm must be refused by the schema: %v", err)
	}
	if err := tre.ApproveRearm(tenantCtx(), "SIM_NG", tripID, "dave"); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Admin.QueryRow(ctx,
		`SELECT status FROM programmes WHERE programme_id='prg_sim_airtime01'`).Scan(&progStatus); err != nil {
		t.Fatal(err)
	}
	if progStatus != "ACTIVE" {
		t.Fatalf("re-armed programme must be ACTIVE: %s", progStatus)
	}

	// The day's total still sits at the cap edge, so recovery is a GOVERNED
	// LIMIT RAISE (no deploy — the no-hardcoding directive under fire):
	// raise to 30,000 and the next confirm lands.
	f.activateGuardrails(t, 30_000)
	if _, err := f.confirmFor(t, "tok_g3", "g-3-retry"); err != nil {
		t.Fatalf("after governed limit raise, lending must resume: %v", err)
	}
}

// EDG-025: CONCURRENT surge — the pool-row lock serializes evaluation, so
// the cap cannot be raced past: at most two of six parallel confirms land,
// at least one breach trips, exactly one open trip converges, and the books
// stay clean.
func TestEDG025_ConcurrentSurge_CapCannotBeRaced(t *testing.T) {
	f := newFixture(t, "m3d_concurrent", 0, 2_000)
	f.activateGuardrails(t, 12_000)
	const n = 6
	for i := 1; i <= n; i++ {
		f.seedSubscriber(t, fmt.Sprintf("sub_c%d", i), fmt.Sprintf("tok_c%d", i), 50_000)
	}

	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = f.confirmFor(t, fmt.Sprintf("tok_c%d", i+1), fmt.Sprintf("c-%d", i+1))
		}(i)
	}
	wg.Wait()

	succeeded := 0
	for _, e := range errs {
		if e == nil {
			succeeded++
		}
	}
	ctx := context.Background()
	var disbursedToday int64
	if err := f.db.Admin.QueryRow(ctx, `
		SELECT COALESCE(SUM(disbursed_minor),0) FROM advances
		WHERE state NOT IN ('DECLINED','FULFILMENT_FAILED')`).Scan(&disbursedToday); err != nil {
		t.Fatal(err)
	}
	if disbursedToday > 12_000 {
		t.Fatalf("EDG-025: the cap was raced past — disbursed %d > 12000", disbursedToday)
	}
	if succeeded > 2 {
		t.Fatalf("at most two confirms fit under the cap, %d succeeded", succeeded)
	}
	var openTrips int
	if err := f.db.Admin.QueryRow(ctx,
		`SELECT count(*) FROM guardrail_trips WHERE state <> 'REARMED'`).Scan(&openTrips); err != nil {
		t.Fatal(err)
	}
	if openTrips != 1 {
		t.Fatalf("concurrent detection must converge on ONE open trip, got %d", openTrips)
	}
	var progStatus string
	if err := f.db.Admin.QueryRow(ctx,
		`SELECT status FROM programmes WHERE programme_id='prg_sim_airtime01'`).Scan(&progStatus); err != nil {
		t.Fatal(err)
	}
	if progStatus != "SUSPENDED" {
		t.Fatalf("programme must be suspended after the surge: %s", progStatus)
	}
	// Books: reserved+utilised only for the survivors (aborts rolled back).
	var reserved, utilised int64
	if err := f.db.Admin.QueryRow(ctx,
		`SELECT reserved_minor, utilised_minor FROM funding_pools WHERE pool_id='pool_sim_01'`).
		Scan(&reserved, &utilised); err != nil {
		t.Fatal(err)
	}
	if reserved+utilised != int64(succeeded)*5_000 {
		t.Fatalf("pool must fund exactly the survivors: reserved=%d utilised=%d succeeded=%d",
			reserved, utilised, succeeded)
	}
}
