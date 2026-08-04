package handler

// Feed-Health monitor — GET /ops/feed-health. Answers is-data-arriving / is-it-clean /
// is-anything-stuck for the MNO feeds. Every DB read runs through the operatorRead RLS
// chokepoint (telco-scoped rows AND aggregates, fail-closed on no-authority); the only
// non-tx read is the recovery-layer liveness check (A2), a control lookup on the tcp_app
// pool that legitimately sits outside the RLS chokepoint. All thresholds are governed
// config with a zero-config floor: an absent/≤0 threshold REFUSES the alarm verdict
// (raw figure only) rather than asserting a false all-clear.

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
)

type feedHealthData struct {
	freshness   []repo.RechargeFreshnessRow
	stateMix    map[string]int64
	heldCounts  []repo.HeldCountRow
	breakBack   repo.RecoveryBreakBacklog
	dupHolds    []repo.DuplicateHoldRow
	telcos      []string
	runHealth   repo.ReconRunHealth
	suspense    repo.SuspenseSummary
	denials     []repo.FeedDenialRow
	unconfirmed repo.UnconfirmedCloses
}

func (p *Portal) opsFeedHealth(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	scope := sess.OperatorScope()

	data, err := operatorRead(r.Context(), p, scope, func(ctx context.Context, tx pgx.Tx) (feedHealthData, error) {
		var d feedHealthData
		var e error
		if d.freshness, e = repo.RechargeFreshness(ctx, tx, scope); e != nil {
			return d, e
		}
		if d.stateMix, e = repo.RechargeStateMix(ctx, tx, scope); e != nil {
			return d, e
		}
		if d.heldCounts, e = repo.HeldCountsByStatusReason(ctx, tx, scope); e != nil {
			return d, e
		}
		if d.breakBack, e = repo.RecoveryBreakBacklogFor(ctx, tx, scope); e != nil {
			return d, e
		}
		if d.dupHolds, e = repo.DuplicateHolds(ctx, tx, scope); e != nil {
			return d, e
		}
		if d.telcos, e = repo.TelcosInScope(ctx, tx, scope); e != nil {
			return d, e
		}
		if d.runHealth, e = repo.RecoveryReconRunHealth(ctx, tx, scope); e != nil {
			return d, e
		}
		if d.suspense, e = repo.OpenSuspense(ctx, tx, scope); e != nil {
			return d, e
		}
		if d.denials, e = repo.FeedDenials(ctx, tx, scope); e != nil {
			return d, e
		}
		if d.unconfirmed, e = repo.StuckUnconfirmedCloses(ctx, tx, scope); e != nil {
			return d, e
		}
		return d, nil
	})
	if err != nil {
		p.Log.Error("ops feed-health", "err", err)
		writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
		return
	}

	now := time.Now().UTC()

	// A2 — recovery-layer live per telco (armed AND freshly confirmed). Control lookup on
	// the tcp_app pool; recon_layer_arming is deliberately not granted to the operator role.
	arming := repo.ReconArming{Pool: p.Pool}
	layerByTelco := make([]map[string]any, 0, len(data.telcos))
	for _, tid := range data.telcos {
		live, e := arming.IsLayerLive(r.Context(), tid, "RECOVERY")
		if e != nil {
			p.Log.Warn("feed-health: recovery layer liveness", "telco", tid, "err", e)
		}
		layerByTelco = append(layerByTelco, map[string]any{"telco_id": tid, "live": live})
	}

	// A1 + A3 — recharge freshness + governed silence verdict per telco. Absent threshold ⇒
	// no `silent` field (the client shows the raw timestamp, never a false "not silent").
	recharge := make([]map[string]any, 0, len(data.freshness))
	for _, fr := range data.freshness {
		row := map[string]any{"telco_id": fr.TelcoID, "events_today": fr.EventsToday}
		if fr.LastReceived != nil {
			row["last_received"] = *fr.LastReceived
		}
		if secs, ok := p.rechargeSilenceSeconds(r.Context(), fr.TelcoID, now); ok {
			row["silence_threshold_seconds"] = secs
			row["silent"] = fr.LastReceived == nil || now.Sub(*fr.LastReceived) > time.Duration(secs)*time.Second
		}
		recharge = append(recharge, row)
	}

	// B4 — latest ACTIVE RECOVERY recon run per telco.
	runRows := make([]map[string]any, 0, len(data.runHealth.LatestActive))
	for _, rr := range data.runHealth.LatestActive {
		runRows = append(runRows, map[string]any{
			"telco_id":      rr.TelcoID,
			"source_count":  rr.SourceCount,
			"matched_count": rr.MatchedCount,
			"break_count":   rr.BreakCount,
			"created_at":    rr.CreatedAt,
		})
	}

	// B1/B2 — held queue by status+reason, with the OPEN (HELD) count surfaced.
	heldRows := make([]map[string]any, 0, len(data.heldCounts))
	var heldOpen int64
	for _, hc := range data.heldCounts {
		heldRows = append(heldRows, map[string]any{"status": hc.Status, "reason": hc.Reason, "count": hc.Count})
		if hc.Status == "HELD" {
			heldOpen += hc.Count
		}
	}

	// B6 — feed denials today (platform-scope audit rows → a '*'-admin estate view; empty for
	// a telco operator, which the client labels).
	denialRows := make([]map[string]any, 0, len(data.denials))
	for _, dn := range data.denials {
		denialRows = append(denialRows, map[string]any{"action": dn.Action, "count": dn.Count})
	}

	// C3 — duplicate holds (unstable telco txn id). MSISDN masked at egress.
	dupRows := make([]map[string]any, 0, len(data.dupHolds))
	for _, dh := range data.dupHolds {
		dupRows = append(dupRows, map[string]any{
			"telco_id":           dh.TelcoID,
			"msisdn_masked":      maskToken(dh.MSISDNToken),
			"amount":             toMoneyView(entity.MustMoney(dh.AmountMinor, entity.NGN)),
			"occurred_at":        dh.OccurredAt,
			"distinct_event_ids": dh.DistinctEventIDs,
		})
	}

	// C1/C2 aging — governed break-aging threshold (recon.recovery). Zero-config floor:
	// absent/≤0 ⇒ no aging verdict.
	agingHours, agingOK := p.breakAgingHours(r.Context(), now)

	writeJSON(w, http.StatusOK, map[string]any{
		"arriving": map[string]any{
			"recharge_by_telco":       recharge,     // A1 + A3
			"recovery_layer_by_telco": layerByTelco, // A2
			"recon_runs_by_telco":     runRows,      // B4 (latest active)
			"rejected_recovery_runs":  data.runHealth.RejectedCount,
		},
		"clean": map[string]any{
			"held_open_count":       heldOpen,      // B1
			"held_by_status_reason": heldRows,      // B2
			"recharge_state_mix":    data.stateMix, // B3
			"open_suspense": map[string]any{ // B5
				"count": data.suspense.OpenCount,
				"total": toMoneyView(data.suspense.OpenTotal),
			},
			"feed_denials_today": denialRows, // B6 — platform/estate scope
		},
		"stuck": map[string]any{
			"recovery_break_backlog": agingTile(data.breakBack.OpenCount, data.breakBack.OldestCreatedAt, agingHours, agingOK, now),     // C1
			"unconfirmed_closes":     agingTile(data.unconfirmed.OpenCount, data.unconfirmed.OldestCreatedAt, agingHours, agingOK, now), // C2
			"duplicate_holds":        dupRows,                                                                                           // C3
		},
	})
}

// rechargeSilenceSeconds resolves the governed recharge silence threshold for a telco
// (telco.recharge_feed, scope telco:<id> → falls back global). ok=false when the config is
// unresolvable, the key is absent, or ≤0 — the zero-config floor: the caller then refuses
// the silence verdict rather than asserting "never silent".
func (p *Portal) rechargeSilenceSeconds(ctx context.Context, telcoID string, now time.Time) (int, bool) {
	cv, err := p.Config.ActiveAt(ctx, "telco.recharge_feed", "telco:"+telcoID, now)
	if err != nil {
		return 0, false
	}
	var c struct {
		SilenceAlarmSeconds *int `json:"silence_alarm_seconds"`
	}
	if json.Unmarshal(cv.Content, &c) != nil || c.SilenceAlarmSeconds == nil || *c.SilenceAlarmSeconds <= 0 {
		return 0, false
	}
	return *c.SilenceAlarmSeconds, true
}

// breakAgingHours resolves the governed RECOVERY break-aging threshold (recon.recovery,
// global). ok=false when unresolvable/absent/≤0 (zero-config floor).
func (p *Portal) breakAgingHours(ctx context.Context, now time.Time) (int, bool) {
	cv, err := p.Config.ActiveAt(ctx, "recon.recovery", "global", now)
	if err != nil {
		return 0, false
	}
	var c struct {
		BreakAgingAlertHours *int `json:"break_aging_alert_hours"`
	}
	if json.Unmarshal(cv.Content, &c) != nil || c.BreakAgingAlertHours == nil || *c.BreakAgingAlertHours <= 0 {
		return 0, false
	}
	return *c.BreakAgingAlertHours, true
}

// agingTile renders a backlog tile: the open count, the oldest item's age, and — only when a
// governed threshold exists — whether the oldest has breached it. No threshold ⇒ no
// aging_breached field (the client shows the count + age without an alarm colour).
func agingTile(count int64, oldest *time.Time, hours int, hoursOK bool, now time.Time) map[string]any {
	m := map[string]any{"open_count": count}
	if oldest != nil {
		m["oldest_created_at"] = *oldest
	}
	if hoursOK {
		m["aging_alert_hours"] = hours
		m["aging_breached"] = oldest != nil && now.Sub(*oldest) > time.Duration(hours)*time.Hour
	}
	return m
}
