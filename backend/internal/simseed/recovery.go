package simseed

// Seeder-B/C — the deterministic recovery-DAY generator. From ONE synthetic ground
// truth it emits BOTH sides of the RECOVERY reconciliation for a Lagos business day:
//
//   - platform side: the telco's wh:%-namespaced recovery_events (what MTN's
//     recharge webhook would have booked), 1–2 per subscriber that day; and
//   - telco side: the matching recovery_eod_feed row per subscriber-token, whose
//     recovery_deducted_minor is the SUM of that subscriber's events.
//
// Because both sides derive from the same amounts, a CLEAN day reconciles exactly
// through the real engine (all MATCHED) — the contract's core promise
// (build/PHASE1_S3_SEEDER_CONTRACT.md §2). The integration seam (Seeder-D / #70)
// then perturbs this clean world to inject a phantom (event, no feed row),
// a drop (feed row, no event) or a mutation (amounts differ) and asserts the recon
// catches each — against DB/engine truth, never the seeder's own intent.
//
// Every property Seeder-A established holds here: DETERMINISTIC (same seed =>
// byte-identical rows: stable ids, fixed within-day wall-clock offsets, NO
// time.Now()), IDEMPOTENT (ON CONFLICT DO NOTHING on the natural keys, so a re-run
// creates 0 rows), and RLS-BOUND to SIM_NG (writes flow through a tenant tx).
//
// Scope note: reversals (negative recovery_allocations) are intentionally NOT
// generated here. A reversal row references advances(advance_id) NOT NULL, which
// would couple the seeder to the origination pipeline; and the reversal-aware NET
// is already unit-proven in recon_recovery_test.go. This generator produces plain
// days (platform NET == gross == feed deduction), which is exactly what the
// integration seam needs to prove MATCH + the three break classes.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"

	_ "time/tzdata" // Africa/Lagos must load in every build (CI image lacks the zoneinfo db)
)

// businessTZ is the fixed business zone both recon and the seeder bucket by. It is
// the same value the recon.recovery config seeds; loading it here (not hardcoding an
// offset) keeps the seeder's day window byte-identical to the engine's
// businessDayWindow, so seeded events land in the day the recon reconciles.
const businessTZ = "Africa/Lagos"

// eventWallHours are the Lagos wall-clock hours the k-th event of a subscriber-day
// is stamped at. All strictly inside [00:00, 24:00) so every event windows into the
// civil day on BOTH sides. Bounds the events-per-subscriber-day at len(this).
var eventWallHours = []int{10, 14}

// RecoveryDayPlan is a deterministic recovery-day request for the SIM_NG cohort.
// The cohort (SeedCohort) must already exist for [0,Count): each event references
// its subscriber_account_id (a nullable FK) so the FK proves the linkage.
type RecoveryDayPlan struct {
	Seed         string // same seed as the cohort — identities must line up
	BusinessDate string // YYYY-MM-DD, interpreted as a Lagos business day
	Count        int    // cohort members [0,Count) that recovered this day
}

// RecoveryDaySeedResult reports only what a run CREATED (idempotent proof: a re-run
// reports zeros) plus the day's monetary control total.
type RecoveryDaySeedResult struct {
	EventsCreated   int
	FeedRowsCreated int
	TotalMinor      int64 // Σ recovery_deducted_minor over the day (the control total)
}

// eventsForSubscriber is the deterministic count (1 or 2) of recovery events a
// subscriber books on a day — exercises the platform-side per-token SUM.
func eventsForSubscriber(seed string, i int) int {
	h := stableHash64(fmt.Sprintf("%s/recn/%06d", seed, i))
	// #nosec G115 -- h % len(eventWallHours) is in [0, len-1] (len is a small
	// compile-time constant, currently 2), so the int() conversion is always exact
	// and cannot overflow.
	return 1 + int(h%uint64(len(eventWallHours)))
}

// eventAmountMinor is the deterministic minor-unit amount of one event, in
// [10000, 500000] (₦100–₦5000) — well under the recon max_amount_minor ceiling, and
// small enough that a day's total never overflows.
func eventAmountMinor(seed, date string, i, k int) int64 {
	return 10000 + int64(stableHash64(fmt.Sprintf("%s/amt/%s/%06d/%d", seed, date, i, k))%490001)
}

// recoveryEventID is the deterministic id for the k-th event of subscriber i on a
// day. NEVER platform.NewID — byte-identical across runs, so source_event_id dedup
// is not tripped on re-run.
func recoveryEventID(seed, date string, i, k int) string {
	return stableID("recev", seed, fmt.Sprintf("%s|%06d|%d", date, i, k))
}

// SeedRecoveryDay writes, from one ground truth, the platform-side wh: recovery
// events and the matching per-subscriber EOD feed rows for one Lagos business day,
// idempotently and RLS-bound to SIM_NG. It reconciles exactly through the real
// RECOVERY engine (all MATCHED). Returns the rows CREATED and the day's control total.
func SeedRecoveryDay(ctx context.Context, appPool *pgxpool.Pool, plan RecoveryDayPlan) (RecoveryDaySeedResult, error) {
	var res RecoveryDaySeedResult
	if plan.Count <= 0 {
		return res, fmt.Errorf("simseed: recovery-day count must be positive, got %d", plan.Count)
	}
	if plan.Seed == "" {
		return res, fmt.Errorf("simseed: recovery-day seed must be non-empty (determinism)")
	}
	loc, err := time.LoadLocation(businessTZ)
	if err != nil {
		return res, fmt.Errorf("simseed: load %s: %w", businessTZ, err)
	}
	d, err := time.ParseInLocation("2006-01-02", plan.BusinessDate, loc)
	if err != nil {
		return res, fmt.Errorf("simseed: recovery-day business_date must be YYYY-MM-DD: %w", err)
	}
	y, m, day := d.Date()

	tctx := platform.WithTenant(ctx, SyntheticTelco)
	err = repo.WithTenantTx(tctx, appPool, func(tx pgx.Tx) error {
		for i := 0; i < plan.Count; i++ {
			token := msisdnToken(plan.Seed, i)
			sub := subscriberID(plan.Seed, i)
			n := eventsForSubscriber(plan.Seed, i)
			var subjTotal int64
			for k := 0; k < n; k++ {
				amt := eventAmountMinor(plan.Seed, plan.BusinessDate, i, k)
				evID := recoveryEventID(plan.Seed, plan.BusinessDate, i, k)
				// Wall-clock hour in Lagos -> UTC instant inside [dayStart, nextMidnight).
				occ := time.Date(y, m, day, eventWallHours[k], 0, 0, 0, loc).UTC()
				ct, err := tx.Exec(ctx, `
					INSERT INTO recovery_events (recovery_event_id, telco_id, source_event_id,
					  subscriber_account_id, msisdn_token, amount_minor, currency, state, occurred_at)
					VALUES ($1,$2,$3,$4,$5,$6,'NGN','ALLOCATED',$7)
					ON CONFLICT (telco_id, source_event_id) DO NOTHING`,
					evID, SyntheticTelco, "wh:"+evID, sub, token, amt, occ)
				if err != nil {
					return fmt.Errorf("simseed: insert recovery event (sub %d, k %d): %w", i, k, err)
				}
				if ct.RowsAffected() == 1 {
					res.EventsCreated++
				}
				subjTotal += amt
			}
			// One EOD feed row per subscriber-day = the SUM of that subscriber's
			// events (the reversal-free NET the recon compares against).
			ct, err := tx.Exec(ctx, `
				INSERT INTO recovery_eod_feed (telco_id, business_date, msisdn_token, recovery_deducted_minor, currency)
				VALUES ($1, $2::date, $3, $4, 'NGN')
				ON CONFLICT (telco_id, business_date, msisdn_token) DO NOTHING`,
				SyntheticTelco, plan.BusinessDate, token, subjTotal)
			if err != nil {
				return fmt.Errorf("simseed: insert EOD feed row (sub %d): %w", i, err)
			}
			if ct.RowsAffected() == 1 {
				res.FeedRowsCreated++
			}
			res.TotalMinor += subjTotal
		}
		return nil
	})
	if err != nil {
		return RecoveryDaySeedResult{}, err
	}
	return res, nil
}
