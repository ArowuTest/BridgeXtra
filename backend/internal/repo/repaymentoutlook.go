package repo

// Wave B.2b — repayment outlook. An HONEST, explainable projection of when a
// subscriber will clear what they owe, computed only from their OWN behaviour:
// the actual loan paydown (recovery_allocations) over a trailing window. Every
// number shown is measured, never fabricated; the range comes from the subscriber's
// real week-to-week repayment variation, not an invented ±; and the function degrades
// gracefully at every thin point rather than inventing a date:
//
//   CLEARED       owed is zero — nothing to project.
//   NO_HISTORY    owed > 0 but the subscriber has never repaid — no pace exists.
//   STALLED       repaid before, but nothing in the window — not currently paying down.
//   INSUFFICIENT  some repayment, but too few active weeks to establish a range.
//   PROJECTED     enough active weeks — a weekly-pace range with assumptions shown.
//
// It is a PURE function of its inputs (the caller passes asOf), so every branch is
// unit-testable deterministically and nothing here reads the clock or the DB.

import (
	"sort"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
)

// OutlookStatus is the honest disposition of the projection.
type OutlookStatus string

const (
	OutlookCleared      OutlookStatus = "CLEARED"
	OutlookNoHistory    OutlookStatus = "NO_HISTORY"
	OutlookStalled      OutlookStatus = "STALLED"
	OutlookInsufficient OutlookStatus = "INSUFFICIENT"
	OutlookProjected    OutlookStatus = "PROJECTED"
)

// outlookWindowDays is the trailing look-back; outlookMinActiveWeeks is the number of
// distinct weeks with repayment needed before a range is trustworthy.
const (
	outlookWindowDays     = 60
	outlookMinActiveWeeks = 3
)

// RepaymentOutlook is the whole projection. Money fields are server-formatted by the
// handler; weeks are whole weeks (rounded up). The handler adds nothing — it only
// formats — so this struct is the single source of the projection's truth.
type RepaymentOutlook struct {
	Status     OutlookStatus
	Owed       entity.Money // echoed still-owed (the thing being cleared)
	WindowDays int          // the look-back the projection is measured over

	RecoveredInWindow entity.Money // Σ repayments applied in the window
	ActiveWeeks       int          // distinct weeks with a repayment in the window
	TypicalWeekly     entity.Money // median weekly repayment across active weeks (the pace)

	// Range: whole weeks to clear the current balance, bracketed by the subscriber's
	// own week-to-week variation (optimistic = at their busier weeks, pessimistic = at
	// their quieter weeks). Zero unless Status == PROJECTED.
	OptimisticWeeks  int
	PessimisticWeeks int

	// Recharge context (assumptions shown on screen), from recovery_events in the window.
	RechargeCount      int
	TypicalRecharge    entity.Money // median recharge amount
	MedianIntervalDays int          // median days between recharges (0 if < 2 recharges)

	// Note is the plain-language explanation / degradation reason for the UI.
	Note string
}

// RepaymentOutlookFrom computes the outlook from the already-loaded 360 rows (the
// caller passes the same repayments/recharges the profile carries, so there is no extra
// query). repayments/recharges may be in any order; asOf is the reference "now".
func RepaymentOutlookFrom(owed entity.Money, repayments []RepaymentEvent, recharges []RechargeEvent, asOf time.Time) RepaymentOutlook {
	cur := owed.Currency()
	zero, _ := entity.NewMoney(0, cur)
	out := RepaymentOutlook{
		Status:            OutlookCleared,
		Owed:              owed,
		WindowDays:        outlookWindowDays,
		RecoveredInWindow: zero,
		TypicalWeekly:     zero,
		TypicalRecharge:   zero,
	}

	if owed.Amount() <= 0 {
		out.Note = "Fully repaid — nothing outstanding to project."
		return out
	}
	if len(repayments) == 0 {
		out.Status = OutlookNoHistory
		out.Note = "No repayments recorded yet — there's no pace to project from."
		return out
	}

	cutoff := asOf.AddDate(0, 0, -outlookWindowDays)

	// Bucket windowed repayments by 7-day week (week 0 = most recent).
	weekMinor := map[int]int64{}
	var recoveredMinor int64
	for _, r := range repayments {
		if !r.AppliedAt.After(cutoff) {
			continue
		}
		wk := int(asOf.Sub(r.AppliedAt).Hours() / (24 * 7))
		if wk < 0 {
			wk = 0 // a future-dated row (clock skew) counts as this week, never negative
		}
		weekMinor[wk] += r.Amount.Amount()
		recoveredMinor += r.Amount.Amount()
	}

	if recoveredMinor == 0 {
		out.Status = OutlookStalled
		out.Note = "No repayments in the last 60 days — this account isn't currently paying down."
		return out
	}
	out.RecoveredInWindow, _ = entity.NewMoney(recoveredMinor, cur)

	// Active-week weekly amounts, ascending.
	weekly := make([]int64, 0, len(weekMinor))
	for _, v := range weekMinor {
		if v > 0 {
			weekly = append(weekly, v)
		}
	}
	sort.Slice(weekly, func(i, j int) bool { return weekly[i] < weekly[j] })
	out.ActiveWeeks = len(weekly)

	// Recharge context is honest supporting detail regardless of projectability.
	out.RechargeCount, out.TypicalRecharge, out.MedianIntervalDays = rechargeContext(recharges, cutoff, asOf, cur)

	if len(weekly) < outlookMinActiveWeeks {
		out.Status = OutlookInsufficient
		out.Note = "Only a couple of weeks of repayments so far — not enough history to project a reliable clearance range yet."
		return out
	}

	// The pace and its honest range come from the subscriber's OWN weekly variation:
	// median for the headline pace, p25/p75 for the bracket.
	out.TypicalWeekly, _ = entity.NewMoney(percentile(weekly, 50), cur)
	fast := percentile(weekly, 75) // busier weeks -> clears sooner
	slow := percentile(weekly, 25) // quieter weeks -> clears later
	owedMinor := owed.Amount()
	out.OptimisticWeeks = ceilDivInt64(owedMinor, fast)
	out.PessimisticWeeks = ceilDivInt64(owedMinor, slow)
	out.Status = OutlookProjected
	out.Note = "Estimate from the recent repayment pace; the range reflects week-to-week variation and actual timing depends on future recharges."
	return out
}

// rechargeContext summarises the windowed recharges: count, median amount, and the
// median gap in days between consecutive recharges (0 if fewer than two).
func rechargeContext(recharges []RechargeEvent, cutoff, asOf time.Time, cur entity.Currency) (int, entity.Money, int) {
	zero, _ := entity.NewMoney(0, cur)
	var amounts []int64
	var times []time.Time
	for _, rc := range recharges {
		if rc.OccurredAt.After(cutoff) && !rc.OccurredAt.After(asOf) {
			amounts = append(amounts, rc.Amount.Amount())
			times = append(times, rc.OccurredAt)
		}
	}
	if len(amounts) == 0 {
		return 0, zero, 0
	}
	sort.Slice(amounts, func(i, j int) bool { return amounts[i] < amounts[j] })
	medAmt, _ := entity.NewMoney(percentile(amounts, 50), cur)

	interval := 0
	if len(times) >= 2 {
		sort.Slice(times, func(i, j int) bool { return times[i].Before(times[j]) })
		gaps := make([]int64, 0, len(times)-1)
		for i := 1; i < len(times); i++ {
			d := int64(times[i].Sub(times[i-1]).Hours() / 24)
			if d < 0 {
				d = 0
			}
			gaps = append(gaps, d)
		}
		sort.Slice(gaps, func(i, j int) bool { return gaps[i] < gaps[j] })
		interval = int(percentile(gaps, 50))
	}
	return len(amounts), medAmt, interval
}

// percentile returns the nearest-rank percentile of an ascending-sorted slice.
// p in [0,100]. Empty slice returns 0.
func percentile(sortedAsc []int64, p int) int64 {
	n := len(sortedAsc)
	if n == 0 {
		return 0
	}
	if p <= 0 {
		return sortedAsc[0]
	}
	if p >= 100 {
		return sortedAsc[n-1]
	}
	// nearest-rank: rank = ceil(p/100 * n), 1-based.
	rank := (p*n + 99) / 100
	if rank < 1 {
		rank = 1
	}
	if rank > n {
		rank = n
	}
	return sortedAsc[rank-1]
}

// ceilDivInt64 divides rounding up; a non-positive divisor yields 0 (guarded by the
// caller, which only divides by strictly-positive weekly amounts).
func ceilDivInt64(a, b int64) int {
	if b <= 0 {
		return 0
	}
	return int((a + b - 1) / b)
}
