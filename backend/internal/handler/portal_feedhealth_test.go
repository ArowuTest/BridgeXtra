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

type rechargeRow struct {
	TelcoID                 string `json:"telco_id"`
	EventsToday             int64  `json:"events_today"`
	SilenceThresholdSeconds *int   `json:"silence_threshold_seconds"`
	Silent                  *bool  `json:"silent"`
}

type layerRow struct {
	TelcoID string `json:"telco_id"`
	Live    bool   `json:"live"`
}

type agingTileResp struct {
	OpenCount       int64 `json:"open_count"`
	AgingAlertHours *int  `json:"aging_alert_hours"`
	AgingBreached   *bool `json:"aging_breached"`
}

type feedHealthResp struct {
	Arriving struct {
		RechargeByTelco      []rechargeRow `json:"recharge_by_telco"`
		RecoveryLayerByTelco []layerRow    `json:"recovery_layer_by_telco"`
	} `json:"arriving"`
	Clean struct {
		HeldOpenCount    int64            `json:"held_open_count"`
		RechargeStateMix map[string]int64 `json:"recharge_state_mix"`
	} `json:"clean"`
	Stuck struct {
		RecoveryBreakBacklog agingTileResp `json:"recovery_break_backlog"`
		DuplicateHolds       []struct {
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
	var sim *rechargeRow
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

// F1 — pin the A2 telco guard. TelcosInScope returns a telco-scoped operator's own telco as
// a LITERAL, deliberately never querying the un-RLS'd `telcos` table (which a telco operator
// could otherwise enumerate whole). This asserts a SIM_NG-scoped operator's A2 lists ONLY
// SIM_NG even with an OTHER_NG telco present. Mutation canary: delete the
// `if telco != "" { return []string{telco} }` guard and this goes red (OTHER_NG appears).
func TestFeedHealth_A2_TelcoScoped_ListsOnlyOwnTelco(t *testing.T) {
	f := newPortalFixture(t, "fh_a2scope")
	ctx := context.Background()
	if _, err := f.db.Admin.Exec(ctx,
		`INSERT INTO telcos (telco_id, name, country, status) VALUES ('OTHER_NG','Other','NG','ACTIVE') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	if err := (&repo.Admins{Pool: f.db.Admin}).CreateWithRole(ctx, "adm_fh_ts", "ops_sim_fh", "portal-key-ops-sim-fh", "OPS", "telco:SIM_NG"); err != nil {
		t.Fatal(err)
	}
	sess := f.login(t, "portal-key-ops-sim-fh")
	r := feedHealthGET(t, f, &sess)
	if len(r.Arriving.RecoveryLayerByTelco) != 1 || r.Arriving.RecoveryLayerByTelco[0].TelcoID != "SIM_NG" {
		t.Fatalf("a SIM_NG-scoped operator's A2 must list ONLY SIM_NG, got %+v — the TelcosInScope literal-telco guard regressed (telcos has no RLS)", r.Arriving.RecoveryLayerByTelco)
	}
}

// seedRechargeFeedNoSilence inserts a telco-scoped telco.recharge_feed ACTIVE config with NO
// silence_alarm_seconds. It shadows the global default (which has the key), so ActiveAt
// resolves a config where the silence threshold is absent — exercising the zero-config floor.
// The handler reads only silence_alarm_seconds, so a minimal content is sufficient.
func seedRechargeFeedNoSilence(t *testing.T, f *portalFixture, telco string) {
	t.Helper()
	if _, err := f.db.Admin.Exec(context.Background(), `
		INSERT INTO config_versions (config_version_id, domain, scope, version_no, state, content, content_hash,
		  effective_from, created_by, approved_by, reason)
		VALUES ('cfg_test_rf_'||$1, 'telco.recharge_feed', 'telco:'||$1, 1, 'ACTIVE',
		  '{"enabled":true}'::jsonb, encode(sha256('{"enabled":true}'::text::bytea),'hex'),
		  now(), 'test-maker', 'test-checker', 'test: recharge_feed without silence_alarm_seconds')`, telco); err != nil {
		t.Fatalf("seed recharge feed (no silence): %v", err)
	}
}

// seedRecoveryBreak inserts an OPEN RECOVERY recon break aged by ageExpr (e.g. interval
// '48 hours'). It first ensures a RECOVERY recon_run exists (run_id is FK'd), then the break
// referencing it. resolved_at defaults NULL so it counts as open.
func seedRecoveryBreak(t *testing.T, f *portalFixture, id, telco, ageExpr string) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.db.Admin.Exec(ctx, `
		INSERT INTO recon_runs (run_id, telco_id, programme_id, layer, period_start, period_end,
		  source_record_count, source_control_total_minor, source_hash,
		  platform_record_count, platform_control_total_minor, created_by)
		VALUES ('run_test_fh', $1, 'prg_sim_airtime01', 'RECOVERY', now() - interval '1 day', now(),
		  1, 1000, 'h', 1, 1000, 'test') ON CONFLICT (run_id) DO NOTHING`, telco); err != nil {
		t.Fatalf("seed recon run: %v", err)
	}
	if _, err := f.db.Admin.Exec(ctx, `
		INSERT INTO recon_items (recon_item_id, run_id, telco_id, item_type, status, created_at)
		VALUES ($1, 'run_test_fh', $2, 'RECOVERY', 'BREAK_MISSING_TELCO', now() - `+ageExpr+`)`, id, telco); err != nil {
		t.Fatalf("seed recovery break: %v", err)
	}
}

// removeBreakAgingConfig supersedes the global recon.recovery config, dropping
// break_aging_alert_hours — so breakAgingHours resolves nothing and the aging verdict must be
// omitted (zero-config floor).
func removeBreakAgingConfig(t *testing.T, f *portalFixture) {
	t.Helper()
	if _, err := f.db.Admin.Exec(context.Background(), `
		WITH cur AS (
		  SELECT config_version_id, version_no, content FROM config_versions
		  WHERE domain='recon.recovery' AND scope='global' AND state='ACTIVE'),
		closed AS (
		  UPDATE config_versions c SET state='SUPERSEDED', effective_to=now()
		  FROM cur WHERE c.config_version_id = cur.config_version_id
		  RETURNING cur.version_no AS vno, cur.content AS content)
		INSERT INTO config_versions (config_version_id, domain, scope, version_no, state, content, content_hash,
		  effective_from, created_by, approved_by, reason)
		SELECT 'cfg_test_recon_noaging_v'||(closed.vno+1), 'recon.recovery', 'global', closed.vno+1, 'ACTIVE',
		  (closed.content - 'break_aging_alert_hours'),
		  encode(sha256((closed.content - 'break_aging_alert_hours')::text::bytea),'hex'),
		  now(), 'test-maker', 'test-checker', 'test: drop break_aging_alert_hours'
		FROM closed`); err != nil {
		t.Fatalf("remove break-aging config: %v", err)
	}
}

// F2 — pin the zero-config floor. With no resolvable silence threshold the `silent` verdict
// must be OMITTED (the raw timestamp only), never a false "not silent" all-clear on a feed
// that might be stalled. Mutation canary: make rechargeSilenceSeconds return a default when
// the key is absent and this goes red.
func TestFeedHealth_ZeroConfigFloor_SilenceOmittedWhenAbsent(t *testing.T) {
	f := newPortalFixture(t, "fh_silencefloor")
	ops := f.login(t, roleKeys["OPS"])
	seedRechargeFeedNoSilence(t, f, "SIM_NG")
	seedRecoveryEvent(t, f, "re_s1", "wh:re_s1", "ALLOCATED", 1000)

	r := feedHealthGET(t, f, &ops)
	var sim *rechargeRow
	for i := range r.Arriving.RechargeByTelco {
		if r.Arriving.RechargeByTelco[i].TelcoID == "SIM_NG" {
			sim = &r.Arriving.RechargeByTelco[i]
		}
	}
	if sim == nil {
		t.Fatalf("SIM_NG must appear in recharge_by_telco; got %+v", r.Arriving.RechargeByTelco)
	}
	if sim.Silent != nil || sim.SilenceThresholdSeconds != nil {
		t.Fatalf("with no governed silence config, silent/silence_threshold must be OMITTED (zero-config floor), got silent=%v thr=%v", sim.Silent, sim.SilenceThresholdSeconds)
	}
}

// F2 — the break-aging half of the floor: config present + oldest past threshold ⇒ breached;
// config absent ⇒ the aging verdict is omitted (not a false "within SLA").
func TestFeedHealth_ZeroConfigFloor_AgingBreachedAndOmitted(t *testing.T) {
	t.Run("breached when oldest past the governed threshold", func(t *testing.T) {
		f := newPortalFixture(t, "fh_agingbreach")
		ops := f.login(t, roleKeys["OPS"])
		// The seeded recon.recovery break_aging_alert_hours=24; a 48h-old break is past it.
		seedRecoveryBreak(t, f, "ri_breach", "SIM_NG", "interval '48 hours'")
		b := feedHealthGET(t, f, &ops).Stuck.RecoveryBreakBacklog
		if b.OpenCount != 1 || b.AgingBreached == nil || !*b.AgingBreached {
			t.Fatalf("an open break older than the 24h governed threshold must set aging_breached=true, got %+v", b)
		}
	})
	t.Run("verdict omitted when no governed threshold", func(t *testing.T) {
		f := newPortalFixture(t, "fh_agingabsent")
		ops := f.login(t, roleKeys["OPS"])
		removeBreakAgingConfig(t, f)
		seedRecoveryBreak(t, f, "ri_absent", "SIM_NG", "interval '48 hours'")
		b := feedHealthGET(t, f, &ops).Stuck.RecoveryBreakBacklog
		if b.OpenCount != 1 {
			t.Fatalf("the break must still count, got %+v", b)
		}
		if b.AgingAlertHours != nil || b.AgingBreached != nil {
			t.Fatalf("with no governed aging threshold, aging_alert_hours/aging_breached must be OMITTED (floor), got hours=%v breached=%v", b.AgingAlertHours, b.AgingBreached)
		}
	})
}
