package handler

// MI Overview (dashboard) — GET /ops/overview. Answers the three questions the
// console redesign is about: who owes us / how much · are they paying · is the
// machine healthy. Everything DB-scoped runs through the operatorRead chokepoint
// (RLS tcp_operator pool), so a telco operator sees only their telco's rows AND
// aggregates. The reuse spine (A1–A7, B1) is LoanBookSummary verbatim — zero new
// SQL; the add tiles layer on: A9 ₦-arrears-by-bucket, B3 collected-today, B4
// honest paydown ratio, and per-programme health (C1 daily-cap headroom, A10 pool
// exposure headroom, C2 state, C3 last breach). Governed ceilings (daily cap,
// exposure bps) are read via configsvc.ActiveAt — never hardcoded.

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
)

// overviewData is everything gathered inside the single scoped read tx. Config
// ceilings are resolved AFTER the tx (they live on the config service's own pool,
// keyed by the already-scope-authorized programme id).
type overviewData struct {
	summary    repo.LoanBookSummaryResult
	extras     repo.OverviewExtras
	programmes []repo.ProgrammeHealthRow
	today      map[string]entity.Money // programmeID → today-disbursed (C1)
	pools      map[string]repo.PoolExposure
	breaches   map[string]repo.GuardrailTrip // programmeID → last breach (present ⇒ has one)
}

// guardrailCeilings mirrors the treasury.guardrails config content (the fields the
// dashboard needs) so the headroom tiles read the SAME governed ceilings the
// guardrail itself enforces.
type guardrailCeilings struct {
	MaxDailyDisbursedMinor      int64 `json:"max_daily_disbursed_minor"`
	MaxOpenExposureBpsCommitted int64 `json:"max_open_exposure_bps_of_committed"`
}

func (p *Portal) opsOverview(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	scope := sess.OperatorScope()
	// Optional admin narrowing to one telco (mirrors the loan-book ?telco=). A
	// telco-scoped operator is already pinned by RLS; this only narrows the '*' admin.
	telcoFilter := r.URL.Query().Get("telco")

	data, err := operatorRead(r.Context(), p, scope, func(ctx context.Context, tx pgx.Tx) (overviewData, error) {
		d := overviewData{
			today:    map[string]entity.Money{},
			pools:    map[string]repo.PoolExposure{},
			breaches: map[string]repo.GuardrailTrip{},
		}
		// Reuse spine (A1–A7, A9, B1) + INV-016 cross-foot — zero new SQL.
		sum, e := repo.LoanBookSummary(ctx, tx, scope, repo.AdvanceFilter{Telco: telcoFilter})
		if e != nil {
			return d, e
		}
		d.summary = sum
		// B3 collected-today + B4 written-off, same telco scope as the summary.
		ex, e := repo.OverviewExtrasFor(ctx, tx, scope, telcoFilter)
		if e != nil {
			return d, e
		}
		d.extras = ex
		// Per-programme health inputs (C1/C2/C3/A10). ProgrammesInScope is RLS-scoped;
		// narrow to the admin telco filter here so the health section matches the money.
		progs, e := repo.ProgrammesInScope(ctx, tx, scope)
		if e != nil {
			return d, e
		}
		for _, pr := range progs {
			if telcoFilter != "" && pr.TelcoID != telcoFilter {
				continue
			}
			d.programmes = append(d.programmes, pr)
			td, e := repo.TodayDisbursedForProgramme(ctx, tx, scope, pr.ProgrammeID)
			if e != nil {
				return d, e
			}
			d.today[pr.ProgrammeID] = td
			pe, e := repo.PoolExposureForProgramme(ctx, tx, scope, pr.ProgrammeID)
			if e != nil {
				return d, e
			}
			d.pools[pr.ProgrammeID] = pe
			trip, found, e := (repo.GuardrailTrips{}).LatestForProgramme(ctx, tx, scope, pr.ProgrammeID)
			if e != nil {
				return d, e
			}
			if found {
				d.breaches[pr.ProgrammeID] = trip
			}
		}
		return d, nil
	})
	if err != nil {
		p.Log.Error("ops overview", "err", err)
		writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
		return
	}

	// B4 paydown ratio (a proportion, NOT money, NOT a due-based collections rate —
	// see false-premise #1). recovered / (recovered + still-owed + written-off face). All
	// three terms are fee-inclusive: recovered = Σ recovery allocations, open_outstanding
	// includes unpaid fee, and WrittenOff is the full face written off (D1) — so a
	// written-off loan lowers the ratio by its full owed amount, not just principal.
	// BX-MED-008: the ratio is computed SERVER-SIDE from int64 minor units and shipped as integer
	// BASIS POINTS. It was a float64 division of two int64s: for a portfolio-scale numerator and
	// denominator that silently loses precision, and shipping a float invites the client to do
	// further arithmetic on it. Integer bps is exact, and the invariant "no client-side monetary
	// arithmetic using JS Number" holds because the client never sees a money integer to divide.
	recovered := data.summary.Recovered.Amount()
	openOut := data.summary.OpenOutstanding.Amount()
	writtenOff := data.extras.WrittenOff.Amount()
	denom := recovered + openOut + writtenOff
	paydownBps := ratioBps(recovered, denom)

	// Per-programme health: read the governed ceilings now (config service's own pool),
	// degrading gracefully if a programme has no resolvable treasury.guardrails config.
	now := time.Now().UTC()
	programmes := make([]map[string]any, 0, len(data.programmes))
	for _, pr := range data.programmes {
		td := data.today[pr.ProgrammeID]
		row := map[string]any{
			"programme_id":    pr.ProgrammeID,
			"telco_id":        pr.TelcoID,
			"status":          pr.Status,
			"today_disbursed": toMoneyView(td),
		}
		var gc guardrailCeilings
		capKnown := false
		if cv, e := p.Config.ActiveAt(r.Context(), "treasury.guardrails", "programme:"+pr.ProgrammeID, now); e == nil {
			if e := json.Unmarshal(cv.Content, &gc); e == nil {
				capKnown = true
			} else {
				p.Log.Warn("overview: guardrail config unmarshal", "programme", pr.ProgrammeID, "err", e)
			}
		} else {
			p.Log.Warn("overview: guardrail config unresolved", "programme", pr.ProgrammeID, "err", e)
		}
		row["cap_known"] = capKnown

		if capKnown {
			// C1: today-disbursed vs DAILY_DISBURSED cap (same measure the guardrail trips on).
			if capM, e := entity.NewMoney(gc.MaxDailyDisbursedMinor, td.Currency()); e == nil {
				row["daily_cap"] = toMoneyView(capM)
				if head, e := capM.Sub(td); e == nil {
					row["daily_headroom"] = toMoneyView(head)
				}
				// BX-MED-008: utilisation is computed HERE, from int64 minor units, as exact integer
				// basis points. The dashboard used to divide two amount_minor values in JavaScript.
				row["cap_util_bps"] = ratioBps(td.Amount(), capM.Amount())
			}
		}

		// A10: pool exposure (reserved+utilised) vs committed × governed bps.
		pe := data.pools[pr.ProgrammeID]
		if pe.HasPool {
			exposure, e1 := pe.Reserved.Add(pe.Utilised)
			pool := map[string]any{
				"committed": toMoneyView(pe.Committed),
			}
			if e1 == nil {
				pool["exposure"] = toMoneyView(exposure)
			}
			if capKnown && e1 == nil {
				if limitM, e := pe.Committed.PercentBps(gc.MaxOpenExposureBpsCommitted); e == nil {
					pool["exposure_limit"] = toMoneyView(limitM)
					pool["exposure_bps"] = gc.MaxOpenExposureBpsCommitted
					if head, e := limitM.Sub(exposure); e == nil {
						pool["exposure_headroom"] = toMoneyView(head)
					}
					// BX-MED-008: exposure utilisation as exact integer bps, server-side.
					pool["exposure_util_bps"] = ratioBps(exposure.Amount(), limitM.Amount())
				}
			}
			row["pool"] = pool
		}

		// C3: last guardrail breach line (nil when the programme has never tripped).
		if trip, ok := data.breaches[pr.ProgrammeID]; ok {
			row["last_breach"] = map[string]any{
				"guardrail":  trip.Guardrail,
				"measured":   toMoneyView(trip.Measured),
				"limit":      toMoneyView(trip.Limit),
				"state":      trip.State,
				"tripped_at": trip.TrippedAt,
			}
		}
		programmes = append(programmes, row)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"summary":         loanBookSummaryJSON(data.summary),
		"by_bucket_value": byBucketValueJSON(data.summary.ByBucketValue), // A9
		"collected_today": toMoneyView(data.extras.CollectedToday),       // B3
		"written_off":     toMoneyView(data.extras.WrittenOff),           // B4 denominator term — full face
		"paydown_bps":     paydownBps,                                    // B4 — exact integer basis points (BX-MED-008)
		"paydown_ratio_basis": map[string]any{
			"recovered":        toMoneyView(data.summary.Recovered),
			"open_outstanding": toMoneyView(data.summary.OpenOutstanding),
			"written_off":      toMoneyView(data.extras.WrittenOff),
		},
		"programmes": programmes, // C1/C2/C3/A10
	})
}

// byBucketValueJSON renders the A9 per-bucket ₦-arrears map through toMoneyView so
// the money is server-formatted like every other figure.
func byBucketValueJSON(m map[string]entity.Money) map[string]any {
	out := make(map[string]any, len(m))
	for bucket, v := range m {
		out[bucket] = toMoneyView(v)
	}
	return out
}
