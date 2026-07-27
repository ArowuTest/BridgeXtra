//go:build !simseed_loop

package main

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// drivePopulation is unavailable without the simseed_loop build tag. The synthetic
// engine driver (simseed.RunLoop and its always-CONFIRMED MNO adapter) is fenced out
// of untagged builds so it can never link into a production binary. This stub keeps
// `go build ./...` compiling; run seed-dev with `-tags simseed_loop` to seed a
// population.
func drivePopulation(_ context.Context, _ *pgxpool.Pool, _ string, _ []string, _ int) (string, error) {
	return "", fmt.Errorf("seed-dev: built without -tags simseed_loop; population drive unavailable (rebuild with the tag)")
}
