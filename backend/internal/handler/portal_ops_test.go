package handler_test

// M4e-1 pack: the ambiguity queues through the portal. Proves the queue
// listing (UNKNOWN + stale-SENT with the governed threshold), the C2 guards
// on both actions (an attempt the resolver settled and a reversal the ingest
// path applied both refuse loudly, never double-act), the retry's reuse of
// the money core's own decision tree (ORIGINAL_UNSEEN refresh), and the
// scope bounds: programme-scoped operators see fulfilments only via the
// advances join and see NO reversal queue at all (telco-grained resource).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
)

// seedAmbiguousChain inserts the full FK chain for one fulfilment attempt via
// the admin (owner) pool: subscriber -> snapshot -> offer -> advance ->
// attempt. State and submitted-at age are the test's knobs.
func seedAmbiguousChain(t *testing.T, f *portalFixture, n int, attemptState string, submittedAgo time.Duration) (attemptID string) {
	t.Helper()
	ctx := context.Background()
	sub := fmt.Sprintf("sub_m4e_%d", n)
	snap := fmt.Sprintf("dsn_m4e_%d", n)
	offer := fmt.Sprintf("ofr_m4e_%d", n)
	adv := fmt.Sprintf("adv_m4e_%d", n)
	attemptID = fmt.Sprintf("fat_m4e_%d", n)
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO subscriber_accounts (subscriber_account_id, telco_id, msisdn_token, status)
		  VALUES ($1,'SIM_NG',$2,'ACTIVE')`, []any{sub, "tok_m4e_" + fmt.Sprint(n)}},
		{`INSERT INTO decision_snapshots (decision_snapshot_id, telco_id, subscriber_account_id,
		    max_face_value_minor, currency, config_version_id)
		  VALUES ($1,'SIM_NG',$2,50000,'NGN','cfg_seed_scoring_policy_v3')`, []any{snap, sub}},
		{`INSERT INTO offers (offer_id, telco_id, programme_id, subscriber_account_id,
		    decision_snapshot_id, face_value_minor, fee_minor, disbursed_minor, repayment_minor,
		    currency, fee_model, product_config_version_id, state, expires_at)
		  VALUES ($1,'SIM_NG','prg_sim_airtime01',$2,$3,10000,1000,9000,10000,'NGN',
		    'DEDUCTED_UPFRONT','cfg_seed_product_airtime_v1','ACCEPTED', now() + interval '1 day')`,
			[]any{offer, sub, snap}},
		{`INSERT INTO advances (advance_id, telco_id, programme_id, subscriber_account_id, offer_id,
		    funding_pool_id, idempotency_key, correlation_id, state, face_value_minor, fee_minor,
		    disbursed_minor, outstanding_minor, currency)
		  VALUES ($1,'SIM_NG','prg_sim_airtime01',$2,$3,'pool_sim_01',$1,$1,'FULFILMENT_UNKNOWN',
		    10000,1000,9000,10000,'NGN')`, []any{adv, sub, offer}},
		{`INSERT INTO fulfilment_attempts (attempt_id, advance_id, attempt_no, telco_idempotency_key,
		    state, request_evidence, submitted_at)
		  VALUES ($1,$2,1,$1,$3,'{}'::jsonb, now() - $4::interval)`,
			[]any{attemptID, adv, attemptState, fmt.Sprintf("%d seconds", int(submittedAgo.Seconds()))}},
	} {
		if _, err := f.db.Admin.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatalf("seed chain: %v", err)
		}
	}
	return attemptID
}

func TestM4E_FulfilmentQueue_StalenessScopeAndEnquireNow(t *testing.T) {
	f := newPortalFixture(t, "m4e_fulfil")
	opsSess := f.login(t, roleKeys["OPS"])

	// Three attempts: UNKNOWN (in queue), stale SENT (in queue — seeded
	// threshold is 600s), fresh SENT (NOT in queue).
	unknownID := seedAmbiguousChain(t, f, 1, "UNKNOWN", 2*time.Hour)
	staleID := seedAmbiguousChain(t, f, 2, "SENT", time.Hour)
	seedAmbiguousChain(t, f, 3, "SENT", 5*time.Second)

	code, body := f.callBody(t, &opsSess, "GET", "/v1/portal/ops/fulfilments", "")
	if code != http.StatusOK {
		t.Fatalf("ops list: %d %s", code, body)
	}
	var list struct {
		Attempts []struct {
			AttemptID string `json:"attempt_id"`
			State     string `json:"state"`
		} `json:"attempts"`
		StaleSentAfterSeconds int `json:"stale_sent_after_seconds"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatal(err)
	}
	if list.StaleSentAfterSeconds != 600 {
		t.Fatalf("governed threshold must surface (600), got %d", list.StaleSentAfterSeconds)
	}
	if len(list.Attempts) != 2 {
		t.Fatalf("queue must hold UNKNOWN + stale SENT only, got %d: %s", len(list.Attempts), body)
	}
	// Oldest first: the 2h UNKNOWN before the 1h stale SENT.
	if list.Attempts[0].AttemptID != unknownID || list.Attempts[1].AttemptID != staleID {
		t.Fatalf("queue must order oldest-first: %s", body)
	}

	// FINANCE reads the same queue (C7); the RBAC matrix covers denial roles.
	finSess := f.login(t, roleKeys["FINANCE"])
	if code, _ := f.callBody(t, &finSess, "GET", "/v1/portal/ops/fulfilments", ""); code != http.StatusOK {
		t.Fatalf("finance read: %d", code)
	}

	// A telco-scoped operator for ANOTHER telco sees an empty queue — the
	// scope bound rides the advances join.
	ctx := context.Background()
	if err := (&repo.Admins{Pool: f.db.Admin}).CreateWithRole(ctx, "adm_m4e_o", "ops_other", "portal-key-ops-other-01", "OPS", "telco:OTHER_NG"); err != nil {
		t.Fatal(err)
	}
	otherSess := f.login(t, "portal-key-ops-other-01")
	code, body = f.callBody(t, &otherSess, "GET", "/v1/portal/ops/fulfilments", "")
	if code != http.StatusOK {
		t.Fatalf("scoped list: %d", code)
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Attempts) != 0 {
		t.Fatalf("out-of-scope operator must see an empty queue, got %s", body)
	}

	// enquire-now pulls the attempt forward: next_enquiry_at becomes ~now.
	if code, body := f.callBody(t, &opsSess, "POST", "/v1/portal/ops/fulfilments/"+unknownID+"/enquire-now", ""); code != http.StatusOK {
		t.Fatalf("enquire-now: %d %s", code, body)
	}
	var due bool
	if err := f.db.Admin.QueryRow(ctx, `
		SELECT next_enquiry_at <= now() FROM fulfilment_attempts WHERE attempt_id=$1`, unknownID).Scan(&due); err != nil {
		t.Fatal(err)
	}
	if !due {
		t.Fatal("enquire-now must schedule the attempt due immediately")
	}
	// An audit row records the operator action.
	var audits int
	if err := f.db.Admin.QueryRow(ctx, `
		SELECT count(*) FROM audit_events WHERE action='fulfilment.enquire_now' AND target_id=$1`, unknownID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Fatalf("enquire-now must audit, got %d rows", audits)
	}

	// C2: the resolver settles the attempt; a stale enquire-now must 409,
	// never touch a resolved attempt.
	if _, err := f.db.Admin.Exec(ctx,
		`UPDATE fulfilment_attempts SET state='CONFIRMED', resolved_at=now() WHERE attempt_id=$1`, unknownID); err != nil {
		t.Fatal(err)
	}
	if code := f.call(t, &opsSess, "POST", "/v1/portal/ops/fulfilments/"+unknownID+"/enquire-now", ""); code != http.StatusNotFound {
		// Settled attempts leave the scoped read too — the no-oracle 404
		// fires at load, before the C2 write guard even runs.
		t.Fatalf("enquire-now on a settled attempt must refuse, got %d", code)
	}
}

func TestM4E_ReversalQueue_RetryBlockedRefreshAndConflict(t *testing.T) {
	f := newPortalFixture(t, "m4e_rev")
	opsSess := f.login(t, roleKeys["OPS"])
	ctx := context.Background()

	// A reversal parked before its original arrived (EDG-019 shape) — seeded
	// directly; park_reason defaults to ORIGINAL_UNSEEN (0013).
	if _, err := f.db.Admin.Exec(ctx, `
		INSERT INTO pending_reversals (pending_reversal_id, telco_id, original_source_event_id,
		  reversal_source_event_id, amount_minor, currency, state)
		VALUES ('prv_m4e_1','SIM_NG','evt_orig_m4e_1','evt_rev_m4e_1',5000,'NGN','PARKED')`); err != nil {
		t.Fatal(err)
	}

	code, body := f.callBody(t, &opsSess, "GET", "/v1/portal/ops/reversals", "")
	if code != http.StatusOK {
		t.Fatalf("reversal list: %d", code)
	}
	var list struct {
		Reversals []struct {
			PendingReversalID string `json:"pending_reversal_id"`
			ParkReason        string `json:"park_reason"`
		} `json:"reversals"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Reversals) != 1 || list.Reversals[0].ParkReason != "ORIGINAL_UNSEEN" {
		t.Fatalf("queue must show the parked reversal with its blocker: %s", body)
	}

	// Retry re-runs the money core's decision tree: original still unseen ->
	// blocked, reason refreshed, row still PARKED. Applied=false is a result.
	code, body = f.callBody(t, &opsSess, "POST", "/v1/portal/ops/reversals/prv_m4e_1/retry", "")
	if code != http.StatusOK {
		t.Fatalf("retry: %d %s", code, body)
	}
	var res struct {
		Applied    bool   `json:"applied"`
		ParkReason string `json:"park_reason"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		t.Fatal(err)
	}
	if res.Applied || res.ParkReason != "ORIGINAL_UNSEEN" {
		t.Fatalf("retry without the original must stay blocked with the reason: %+v", res)
	}
	var audits int
	if err := f.db.Admin.QueryRow(ctx, `
		SELECT count(*) FROM audit_events WHERE action='reversal.retry' AND target_id='prv_m4e_1'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Fatalf("retry must audit, got %d rows", audits)
	}

	// C2 loser path: the ingest auto-apply wins the race (simulated by the
	// owner closing the row); a stale operator retry must land 409 off the
	// FOR UPDATE claim — never a second apply.
	if _, err := f.db.Admin.Exec(ctx,
		`UPDATE pending_reversals SET state='APPLIED', applied_at=now() WHERE pending_reversal_id='prv_m4e_1'`); err != nil {
		t.Fatal(err)
	}
	if code := f.call(t, &opsSess, "POST", "/v1/portal/ops/reversals/prv_m4e_1/retry", ""); code != http.StatusNotFound {
		// The applied row leaves the scoped PARKED read: no-oracle 404 at
		// load. (A race INSIDE the tx would surface as the claim's 409 —
		// same refusal, different door.)
		t.Fatalf("retry on an applied reversal must refuse, got %d", code)
	}

	// Telco-grained resource: a PROGRAMME-scoped operator has no telco-level
	// bound and must see an EMPTY queue — not every telco's money events.
	if err := (&repo.Admins{Pool: f.db.Admin}).CreateWithRole(ctx, "adm_m4e_p", "ops_prog", "portal-key-ops-prog-01", "OPS", "programme:prg_sim_airtime01"); err != nil {
		t.Fatal(err)
	}
	progSess := f.login(t, "portal-key-ops-prog-01")
	code, body = f.callBody(t, &progSess, "GET", "/v1/portal/ops/reversals", "")
	if code != http.StatusOK {
		t.Fatalf("programme-scoped list: %d", code)
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Reversals) != 0 {
		t.Fatalf("programme-scoped operator must see an empty reversal queue (TelcoLevelBound), got %s", body)
	}
}
