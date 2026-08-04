package handler_test

// Wave B.4 Feed-Health monitor. Adversarial assertions, one per way the monitor could lie:
//   1. wh:% discriminator (false-premise #4): recharge counts must EXCLUDE the EOD feed's
//      rows in the shared recovery_events table — omit the filter and every count doubles.
//   2. Silence verdict is governed (A3): the `silent` field appears only because the seeded
//      global silence_alarm_seconds resolved, and reads false for a just-received feed.
//   3. Duplicate-hold detection (C3): same (msisdn, amount, occurred_at) under two txn ids.
//   4. Scope fail-closed: a no-authority operator sees empty everything, no error.

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
)

// seedRecoveryEvent inserts a recovery_events row. A source_event_id starting "wh:" is a
// RECHARGE-feed row; anything else (e.g. "eod:") is the EOD feed — the ONLY discriminator.
func seedRecoveryEvent(t *testing.T, f *portalFixture, id, sourceEventID, state string, amount int64) {
	t.Helper()
	if _, err := f.db.Admin.Exec(context.Background(), `
		INSERT INTO recovery_events (recovery_event_id, telco_id, source_event_id, amount_minor, currency, state, occurred_at)
		VALUES ($1,'SIM_NG',$2,$3,'NGN',$4, now())`, id, sourceEventID, amount, state); err != nil {
		t.Fatalf("seed recovery event: %v", err)
	}
}

// seedHeld inserts a held_recharge_events row (default status HELD).
func seedHeld(t *testing.T, f *portalFixture, id, sourceEventID, token string, amount int64, occurredShift string) {
	t.Helper()
	if _, err := f.db.Admin.Exec(context.Background(), `
		INSERT INTO held_recharge_events (held_id, telco_id, source_event_id, msisdn_token, amount_minor, currency,
		  occurred_at, reason, status, held_at)
		VALUES ($1,'SIM_NG',$2,$3,$4,'NGN', date_trunc('minute', now()) `+occurredShift+`, 'PER_EVENT_CLAMP','HELD', now())`,
		id, sourceEventID, token, amount); err != nil {
		t.Fatalf("seed held: %v", err)
	}
}

type feedHealthResp struct {
	Arriving struct {
		RechargeByTelco []struct {
			TelcoID                 string `json:"telco_id"`
			EventsToday             int64  `json:"events_today"`
			SilenceThresholdSeconds *int   `json:"silence_threshold_seconds"`
			Silent                  *bool  `json:"silent"`
		} `json:"recharge_by_telco"`
	} `json:"arriving"`
	Clean struct {
		HeldOpenCount    int64            `json:"held_open_count"`
		RechargeStateMix map[string]int64 `json:"recharge_state_mix"`
	} `json:"clean"`
	Stuck struct {
		DuplicateHolds []struct {
			DistinctEventIDs int64 `json:"distinct_event_ids"`
		} `json:"duplicate_holds"`
	} `json:"stuck"`
}

func feedHealthGET(t *testing.T, f *portalFixture, s *session) feedHealthResp {
	t.Helper()
	code, body := f.callBody(t, s, "GET", "/v1/portal/ops/feed-health", "")
	if code != http.StatusOK {
		t.Fatalf("feed-health: %d %s", code, body)
	}
	var resp feedHealthResp
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal feed-health: %v — %s", err, body)
	}
	return resp
}

func TestFeedHealth_Discriminator_Silence_Duplicates_AndScope(t *testing.T) {
	f := newPortalFixture(t, "feedhealth")
	ops := f.login(t, roleKeys["OPS"])

	// Recharge feed (wh:%): 1 ALLOCATED + 1 QUARANTINED. EOD feed (eod:): 1 ALLOCATED — must
	// NOT be counted as a recharge.
	seedRecoveryEvent(t, f, "re_1", "wh:re_1", "ALLOCATED", 1000)
	seedRecoveryEvent(t, f, "re_2", "wh:re_2", "QUARANTINED", 2000)
	seedRecoveryEvent(t, f, "re_eod", "eod:re_eod", "ALLOCATED", 3000)

	// Two HELD rows: same (msisdn, amount, occurred_at) under different source ids = a duplicate.
	seedHeld(t, f, "h_1", "wh:h_1", "tok_dup", 5000, "")
	seedHeld(t, f, "h_2", "wh:h_2", "tok_dup", 5000, "")

	r := feedHealthGET(t, f, &ops)

	// (1) wh:% discriminator — ALLOCATED must be 1 (the recharge one), NOT 2 (would include EOD).
	if r.Clean.RechargeStateMix["ALLOCATED"] != 1 || r.Clean.RechargeStateMix["QUARANTINED"] != 1 {
		t.Fatalf("recharge state mix must exclude the EOD event (ALLOCATED=1, QUARANTINED=1), got %+v — wh:%% filter dropped?", r.Clean.RechargeStateMix)
	}
	// events_today likewise counts recharge rows only (2, not 3).
	var sim *struct {
		TelcoID                 string `json:"telco_id"`
		EventsToday             int64  `json:"events_today"`
		SilenceThresholdSeconds *int   `json:"silence_threshold_seconds"`
		Silent                  *bool  `json:"silent"`
	}
	for i := range r.Arriving.RechargeByTelco {
		if r.Arriving.RechargeByTelco[i].TelcoID == "SIM_NG" {
			sim = &r.Arriving.RechargeByTelco[i]
		}
	}
	if sim == nil {
		t.Fatalf("SIM_NG must appear in recharge_by_telco; got %+v", r.Arriving.RechargeByTelco)
	}
	if sim.EventsToday != 2 {
		t.Fatalf("events_today must count recharge rows only (2), got %d — EOD leaked in?", sim.EventsToday)
	}

	// (2) Silence verdict is governed: the seeded global silence_alarm_seconds (0069 = 3600)
	// resolves, so the verdict is PRESENT and false (a just-received feed is not silent).
	if sim.SilenceThresholdSeconds == nil || *sim.SilenceThresholdSeconds != 3600 {
		t.Fatalf("silence threshold must come from governed config (3600), got %v", sim.SilenceThresholdSeconds)
	}
	if sim.Silent == nil || *sim.Silent {
		t.Fatalf("a feed received just now must not be silent, got %v", sim.Silent)
	}

	// (3) Duplicate holds: two txn ids for the same recharge → one duplicate with 2 ids.
	if r.Clean.HeldOpenCount != 2 {
		t.Fatalf("held_open_count must be 2, got %d", r.Clean.HeldOpenCount)
	}
	if len(r.Stuck.DuplicateHolds) != 1 || r.Stuck.DuplicateHolds[0].DistinctEventIDs != 2 {
		t.Fatalf("duplicate holds must find 1 tuple with 2 distinct ids, got %+v", r.Stuck.DuplicateHolds)
	}

	// (4) Scope fail-closed: a no-authority (global) operator sees empty everything, no error.
	ctx := context.Background()
	if err := (&repo.Admins{Pool: f.db.Admin}).CreateWithRole(ctx, "adm_fh_g", "ops_global_fh", "portal-key-ops-global-fh", "OPS", "global"); err != nil {
		t.Fatal(err)
	}
	g := f.login(t, "portal-key-ops-global-fh")
	gr := feedHealthGET(t, f, &g)
	if len(gr.Arriving.RechargeByTelco) != 0 || len(gr.Clean.RechargeStateMix) != 0 || gr.Clean.HeldOpenCount != 0 || len(gr.Stuck.DuplicateHolds) != 0 {
		t.Fatalf("no-authority operator must see empty feed-health, got recharge=%d mix=%d held=%d dup=%d",
			len(gr.Arriving.RechargeByTelco), len(gr.Clean.RechargeStateMix), gr.Clean.HeldOpenCount, len(gr.Stuck.DuplicateHolds))
	}
}
