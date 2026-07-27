//go:build simseed_loop

package simseed

import (
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/mno"
)

// fFile / fRow mirror featureingest's UNEXPORTED fileShape/rowShape (ingest.go) —
// the json tags MUST match exactly or rows silently quarantine. NINVerified=true
// (Build 2) so the loop's synthetic subscribers pass the origination NIN gate;
// pointers are always set and QualityFlags is always non-nil so the file bytes are
// byte-stable across runs (content-hash dedup).
type fFile struct {
	TelcoID string    `json:"telco_id"`
	AsOf    time.Time `json:"as_of"`
	Rows    []fRow    `json:"rows"`
}

type fRow struct {
	MSISDNToken         string   `json:"msisdn_token"`
	TenureDays          *int     `json:"tenure_days"`
	ActivityDays30d     *int     `json:"activity_days_30d"`
	ActiveDays90d       *int     `json:"active_days_90d"`
	WeeklyRechargeMinor []int64  `json:"weekly_recharge_minor"`
	Currency            string   `json:"currency"`
	QualityFlags        []string `json:"quality_flags"`
	NINVerified         *bool    `json:"nin_verified"`
}

// profile is a synthetic subscriber archetype: the feature values that drive it
// through the REAL scoring + origination to a known outcome, plus the telco
// fulfilment outcome the synthetic adapter returns for it. Values are literal
// per-profile constants (auditable), never hashed. The expect* fields are asserted
// against the ACTUAL scored decision (Slice 2) — the engine is the source of truth.
type profile struct {
	name         string
	tenureDays   int
	activity30   int     // must be 0..30 or featureingest quarantines the row
	active90     int     // must be 0..90
	weekly       []int64 // exactly 13 weeks
	qualityFlags []string
	originates   bool        // false = expected decline (no advance booked)
	telcoOutcome mno.Outcome // adapter outcome for a borrower (CONFIRMED for the happy path)

	expectEligible bool   // decision_doc->>'eligible'
	expectMaxFace  int64  // decision_snapshots.max_face_value_minor (0 for ineligible)
	expectReason   string // a reason code that must appear in reason_codes ("" = don't assert)

	// recoverBps is how much of the booked FaceValue this profile recovers on the
	// business day (Slice 3): 10000 = full (advance CLOSED + re-origination hold),
	// 5000 = half (PARTIALLY_RECOVERED, no hold), 0 = none (stays ACTIVE — a defaulter).
	recoverBps int64
}

func flat(v int64, n int) []int64 {
	w := make([]int64, n)
	for i := range w {
		w[i] = v
	}
	return w
}

// profiles is the population the loop drives (design §2). The defaulter carries a
// single spike (200000 then flat 3000) so the anti-gaming WINSORISATION is exercised:
// the raw sum would score a higher tier, but the winsorised total caps it.
var profiles = []profile{
	{name: "good-payer", tenureDays: 720, activity30: 30, active90: 90, weekly: flat(40000, 13), qualityFlags: nil, originates: true, telcoOutcome: mno.OutcomeConfirmed, expectEligible: true, expectMaxFace: 50000, expectReason: "", recoverBps: 10000},
	{name: "varied-limit", tenureDays: 365, activity30: 30, active90: 90, weekly: flat(10000, 13), qualityFlags: nil, originates: true, telcoOutcome: mno.OutcomeConfirmed, expectEligible: true, expectMaxFace: 10000, expectReason: "", recoverBps: 5000},
	{name: "thin-file", tenureDays: 90, activity30: 30, active90: 8, weekly: flat(5000, 13), qualityFlags: []string{"SHORT_HISTORY"}, originates: true, telcoOutcome: mno.OutcomeConfirmed, expectEligible: true, expectMaxFace: 5000, expectReason: "COLD_START_THIN_FILE", recoverBps: 10000},
	{name: "defaulter", tenureDays: 200, activity30: 30, active90: 90, weekly: append([]int64{200000}, flat(3000, 12)...), qualityFlags: nil, originates: true, telcoOutcome: mno.OutcomeConfirmed, expectEligible: true, expectMaxFace: 5000, expectReason: "SPIKE_PATTERN_DETECTED", recoverBps: 0},
	{name: "declined", tenureDays: 400, activity30: 30, active90: 90, weekly: flat(20000, 13), qualityFlags: []string{"MISSING_FIELDS"}, originates: false, expectEligible: false, expectMaxFace: 0, expectReason: "MISSING_DATA_REJECTED", recoverBps: 0},
}

// toRow renders a profile as a featureingest-valid row: exactly 13 weekly values,
// all pointers set, quality flags non-nil, NIN verified.
func (p profile) toRow(token string) fRow {
	qf := p.qualityFlags
	if qf == nil {
		qf = []string{}
	}
	tenure, act30, act90, nin := p.tenureDays, p.activity30, p.active90, true
	return fRow{
		MSISDNToken: token, TenureDays: &tenure, ActivityDays30d: &act30, ActiveDays90d: &act90,
		WeeklyRechargeMinor: p.weekly, Currency: "NGN", QualityFlags: qf, NINVerified: &nin,
	}
}
