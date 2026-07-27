//go:build simseed_loop

package main

import (
	"context"
	"fmt"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/simseed"
	"github.com/jackc/pgx/v5/pgxpool"
)

// drivePopulation runs the real feature → score → originate → recover engine (via
// simseed.RunLoop) across the given seeds to produce a varied loan population. Each
// seed is a disjoint synthetic cohort; RunLoop runs VerifySyntheticOnly + a BYPASSRLS
// refusal as its first statements, so this is structurally confined to a SIM_NG-only
// dev DB and can never touch a real telco. No business row is INSERTed directly — every
// advance and recovery flows through the real usecases.
func drivePopulation(ctx context.Context, appPool *pgxpool.Pool, programme string, seeds []string, repeat int) (string, error) {
	// The RunLoop scoring window rejects any business day older than ~40h, so use today
	// in Lagos (never a hardcoded date).
	businessDay := time.Now().In(lagos()).Format("2006-01-02")

	var subs, adv, dec, rec int
	for _, s := range seeds {
		res, err := simseed.RunLoop(ctx, appPool, simseed.LoopPlan{
			Seed:        s,
			BusinessDay: businessDay,
			Repeat:      repeat,
			ProgrammeID: programme,
		})
		if err != nil {
			return "", fmt.Errorf("run loop seed %q: %w", s, err)
		}
		subs += res.Subscribers
		adv += res.Advances
		dec += res.Declined
		rec += res.Recovered
	}
	return fmt.Sprintf(
		"population: %d subscribers, %d advances, %d declined, %d recovered (across %d seeds, business-day %s, programme %s)",
		subs, adv, dec, rec, len(seeds), businessDay, programme,
	), nil
}

// lagos is Africa/Lagos (WAT, UTC+1, no DST); falls back to a fixed +1 zone if the
// tzdata is unavailable in the runtime image.
func lagos() *time.Location {
	if loc, err := time.LoadLocation("Africa/Lagos"); err == nil {
		return loc
	}
	return time.FixedZone("WAT", 1*60*60)
}
