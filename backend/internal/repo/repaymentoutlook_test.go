package repo

// Wave B.2b — the repayment outlook is a projection shown to operators, so its
// INTEGRITY is the whole point: it must never fabricate a date, must degrade honestly
// on thin history, and its range must come from real variation. These pure tests pin
// every branch and the maths, deterministically (fixed asOf, no clock/DB).

import (
	"testing"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
)

var outlookAsOf = time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)

func ngn(t *testing.T, minor int64) entity.Money {
	t.Helper()
	m, err := entity.NewMoney(minor, entity.NGN)
	if err != nil {
		t.Fatalf("money: %v", err)
	}
	return m
}

// repay builds a repayment applied `daysAgo` before asOf.
func repay(t *testing.T, minor int64, daysAgo int) RepaymentEvent {
	return RepaymentEvent{AdvanceID: "adv", Component: "PRINCIPAL", Amount: ngn(t, minor), AppliedAt: outlookAsOf.AddDate(0, 0, -daysAgo)}
}
func recharge(t *testing.T, minor int64, daysAgo int) RechargeEvent {
	return RechargeEvent{Amount: ngn(t, minor), State: "ALLOCATED", Applied: ngn(t, minor), OccurredAt: outlookAsOf.AddDate(0, 0, -daysAgo)}
}

func TestOutlook_Cleared(t *testing.T) {
	o := RepaymentOutlookFrom(ngn(t, 0), []RepaymentEvent{repay(t, 5000, 3)}, nil, outlookAsOf)
	if o.Status != OutlookCleared {
		t.Fatalf("owed 0 must be CLEARED, got %s", o.Status)
	}
	if o.OptimisticWeeks != 0 || o.PessimisticWeeks != 0 {
		t.Fatalf("CLEARED must project no weeks")
	}
}

func TestOutlook_NoHistory(t *testing.T) {
	o := RepaymentOutlookFrom(ngn(t, 5000), nil, nil, outlookAsOf)
	if o.Status != OutlookNoHistory {
		t.Fatalf("owed>0 + no repayments must be NO_HISTORY, got %s", o.Status)
	}
}

func TestOutlook_Stalled(t *testing.T) {
	// A repayment exists but is older than the 60-day window → not currently paying down.
	o := RepaymentOutlookFrom(ngn(t, 5000), []RepaymentEvent{repay(t, 5000, 90)}, nil, outlookAsOf)
	if o.Status != OutlookStalled {
		t.Fatalf("repaid only outside the window must be STALLED, got %s", o.Status)
	}
	if o.RecoveredInWindow.Amount() != 0 {
		t.Fatalf("STALLED must show 0 recovered in window, got %d", o.RecoveredInWindow.Amount())
	}
}

func TestOutlook_Insufficient(t *testing.T) {
	// Two active weeks only (weeks 0 and 1) → below the 3-week floor to project a range.
	o := RepaymentOutlookFrom(ngn(t, 5000),
		[]RepaymentEvent{repay(t, 2000, 2), repay(t, 2000, 9)}, nil, outlookAsOf)
	if o.Status != OutlookInsufficient {
		t.Fatalf("<3 active weeks must be INSUFFICIENT, got %s", o.Status)
	}
	if o.OptimisticWeeks != 0 || o.PessimisticWeeks != 0 {
		t.Fatalf("INSUFFICIENT must not project a range")
	}
	if o.RecoveredInWindow.Amount() != 4000 || o.ActiveWeeks != 2 {
		t.Fatalf("INSUFFICIENT should still report the facts: recovered=%d activeWeeks=%d", o.RecoveredInWindow.Amount(), o.ActiveWeeks)
	}
}

func TestOutlook_Projected_RangeFromOwnVariation(t *testing.T) {
	// Three active weeks: 1000 (wk2), 2000 (wk1), 3000 (wk0). Owed 6000.
	// sorted weekly = [1000,2000,3000]; p25=1000, p50=2000, p75=3000.
	// optimistic = ceil(6000/3000)=2, pessimistic = ceil(6000/1000)=6.
	o := RepaymentOutlookFrom(ngn(t, 6000),
		[]RepaymentEvent{repay(t, 3000, 3), repay(t, 2000, 10), repay(t, 1000, 17)},
		[]RechargeEvent{recharge(t, 5000, 3), recharge(t, 5000, 9), recharge(t, 5000, 15)},
		outlookAsOf)
	if o.Status != OutlookProjected {
		t.Fatalf("3 active weeks must PROJECT, got %s", o.Status)
	}
	if o.ActiveWeeks != 3 || o.RecoveredInWindow.Amount() != 6000 {
		t.Fatalf("facts: activeWeeks=%d recovered=%d", o.ActiveWeeks, o.RecoveredInWindow.Amount())
	}
	if o.TypicalWeekly.Amount() != 2000 {
		t.Fatalf("typical weekly must be the median (2000), got %d", o.TypicalWeekly.Amount())
	}
	if o.OptimisticWeeks != 2 || o.PessimisticWeeks != 6 {
		t.Fatalf("range must be owed/p75..owed/p25 = 2..6, got %d..%d", o.OptimisticWeeks, o.PessimisticWeeks)
	}
	// The range must never be inverted.
	if o.OptimisticWeeks > o.PessimisticWeeks {
		t.Fatalf("optimistic (%d) must be <= pessimistic (%d)", o.OptimisticWeeks, o.PessimisticWeeks)
	}
	// Recharge context: 3 recharges of 5000 every ~6 days.
	if o.RechargeCount != 3 || o.TypicalRecharge.Amount() != 5000 {
		t.Fatalf("recharge context: count=%d median=%d", o.RechargeCount, o.TypicalRecharge.Amount())
	}
	if o.MedianIntervalDays != 6 {
		t.Fatalf("median recharge interval must be 6 days, got %d", o.MedianIntervalDays)
	}
}

// Calendar anchoring: a subscriber who repays roughly every 3 weeks must clear in more
// CALENDAR weeks than repayment weeks — the headline must never read faster than the
// elapsed time. Three equal repayment weeks at 5/26/47 days ago (weeks 0/3/6, span 47d):
// paying weeks to clear = ceil(6000/2000)=3, but calendar = ceil(3 * 47 / (7*3)) = 7.
func TestOutlook_Projected_AnchoredToCalendarTime(t *testing.T) {
	sparse := RepaymentOutlookFrom(ngn(t, 6000),
		[]RepaymentEvent{repay(t, 2000, 5), repay(t, 2000, 26), repay(t, 2000, 47)}, nil, outlookAsOf)
	if sparse.Status != OutlookProjected {
		t.Fatalf("expected PROJECTED, got %s", sparse.Status)
	}
	// All weeks equal (2000) so p25=p50=p75 → paying weeks = 3; calendar must exceed that.
	if sparse.OptimisticWeeks != 7 || sparse.PessimisticWeeks != 7 {
		t.Fatalf("sparse (~3-weekly) repayer must be anchored to calendar time (7 weeks, not 3 paying weeks), got %d..%d",
			sparse.OptimisticWeeks, sparse.PessimisticWeeks)
	}
	// A weekly repayer with the SAME per-week amounts clears in far fewer calendar weeks —
	// proving the anchor tracks cadence, not just the amounts.
	dense := RepaymentOutlookFrom(ngn(t, 6000),
		[]RepaymentEvent{repay(t, 2000, 3), repay(t, 2000, 10), repay(t, 2000, 17)}, nil, outlookAsOf)
	if dense.PessimisticWeeks >= sparse.PessimisticWeeks {
		t.Fatalf("a weekly repayer (%d wks) must clear sooner in calendar time than a 3-weekly repayer (%d wks)",
			dense.PessimisticWeeks, sparse.PessimisticWeeks)
	}
}

// A future-dated repayment (clock skew) must not create a negative week bucket or crash.
func TestOutlook_FutureDatedRepaymentIsSafe(t *testing.T) {
	rs := []RepaymentEvent{
		{AdvanceID: "a", Component: "PRINCIPAL", Amount: ngn(t, 1000), AppliedAt: outlookAsOf.AddDate(0, 0, 2)}, // future
		repay(t, 2000, 10), repay(t, 3000, 17),
	}
	o := RepaymentOutlookFrom(ngn(t, 6000), rs, nil, outlookAsOf)
	if o.Status != OutlookProjected {
		t.Fatalf("future-dated row folded into week 0 should still project, got %s", o.Status)
	}
	if o.OptimisticWeeks < 0 || o.PessimisticWeeks < 0 {
		t.Fatalf("weeks must never be negative")
	}
}

func TestPercentile_NearestRank(t *testing.T) {
	s := []int64{10, 20, 30, 40}
	cases := []struct {
		p    int
		want int64
	}{{0, 10}, {25, 10}, {50, 20}, {75, 30}, {100, 40}}
	for _, c := range cases {
		if got := percentile(s, c.p); got != c.want {
			t.Fatalf("percentile(%d) = %d, want %d", c.p, got, c.want)
		}
	}
	if percentile(nil, 50) != 0 {
		t.Fatalf("percentile of empty must be 0")
	}
}

func TestCeilDivInt64(t *testing.T) {
	cases := []struct {
		a, b int64
		want int
	}{{7, 2, 4}, {6, 2, 3}, {1, 3, 1}, {0, 5, 0}, {5, 0, 0}, {6000, 1000, 6}}
	for _, c := range cases {
		if got := ceilDivInt64(c.a, c.b); got != c.want {
			t.Fatalf("ceilDivInt64(%d,%d) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
