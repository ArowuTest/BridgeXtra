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
// per-profile constants (auditable), never hashed.
type profile struct {
	name         string
	tenureDays   int
	activity30   int // must be 0..30 or featureingest quarantines the row
	active90     int // must be 0..90
	weeklyMinor  int64
	qualityFlags []string
	originates   bool        // false = expected decline (no advance booked)
	telcoOutcome mno.Outcome // adapter outcome for a borrower (CONFIRMED for the happy path)
}

// profiles is the population the loop drives. Slice 1: one good-payer (flat ₦400/wk
// history -> winsorised total 520000 -> TIER_04, top rung 50000). Slice 2 adds
// varied-limit / thin-file / defaulter / declined.
var profiles = []profile{
	{name: "good-payer", tenureDays: 720, activity30: 30, active90: 90, weeklyMinor: 40000, qualityFlags: nil, originates: true, telcoOutcome: mno.OutcomeConfirmed},
}

// toRow renders a profile as a featureingest-valid row for a token: exactly 13 flat
// weekly values, all pointers set, quality flags non-nil, NIN verified.
func (p profile) toRow(token string) fRow {
	weekly := make([]int64, 13)
	for i := range weekly {
		weekly[i] = p.weeklyMinor
	}
	qf := p.qualityFlags
	if qf == nil {
		qf = []string{}
	}
	tenure, act30, act90, nin := p.tenureDays, p.activity30, p.active90, true
	return fRow{
		MSISDNToken: token, TenureDays: &tenure, ActivityDays30d: &act30, ActiveDays90d: &act90,
		WeeklyRechargeMinor: weekly, Currency: "NGN", QualityFlags: qf, NINVerified: &nin,
	}
}
