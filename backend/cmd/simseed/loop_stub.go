//go:build !simseed_loop

package main

import (
	"context"
	"log"

	"github.com/jackc/pgx/v5/pgxpool"
)

// loopEntry stub for normal builds: the varied-user loop and its synthetic
// always-CONFIRMED telco adapter are fenced behind -tags simseed_loop so they can
// never be compiled into a production binary.
func loopEntry(_ context.Context, _ *pgxpool.Pool, _, _, _ string, _ int) {
	log.Fatal("simseed: -loop requires a build with -tags simseed_loop (the varied-user loop is fenced out of normal builds)")
}
