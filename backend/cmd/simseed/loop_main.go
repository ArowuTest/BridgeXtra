//go:build simseed_loop

package main

import (
	"context"
	"log"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/simseed"
)

// loopEntry (built only with -tags simseed_loop) runs the varied-user loop against
// the real usecases. simseed.RunLoop enforces its own prod-safety guards first.
func loopEntry(ctx context.Context, pool *pgxpool.Pool, seed, businessDay, programmeID string, repeat int) {
	res, err := simseed.RunLoop(ctx, pool, simseed.LoopPlan{
		Seed: seed, BusinessDay: businessDay, ProgrammeID: programmeID, Repeat: repeat,
	})
	if err != nil {
		log.Fatalf("simseed loop: %v", err)
	}
	// #nosec G706 -- seed/businessDay are validated (seedRe/dateRe) in main; profile
	// names and states are internal constants; advance ids are ULIDs. No user free text.
	log.Printf("simseed loop done — %d subscriber(s), %d advance(s) ACTIVE, %d declined (seed=%q day=%s)",
		res.Subscribers, res.Advances, res.Declined, seed, businessDay)
	for _, m := range res.Members {
		log.Printf("  %s [%s] advance=%s state=%s", m.Token, m.Profile, m.AdvanceID, m.State)
	}
}
