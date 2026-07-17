// cmd/migrate applies backend/migrations against the target database.
// Usage: migrate [-dsn <url>]  (default: TCP_ADMIN_DSN env, then local dev DSN)
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform/dbmigrate"
	"github.com/ArowuTest/telco-credit-platform/backend/migrations"
)

func main() {
	dsnFlag := flag.String("dsn", "", "database url (defaults to TCP_ADMIN_DSN)")
	flag.Parse()

	dsn := *dsnFlag
	if dsn == "" {
		dsn = os.Getenv("TCP_ADMIN_DSN")
	}
	if dsn == "" {
		dsn = "postgres://postgres:devlocal@localhost:5434/telco_credit" // local dev (A-14)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pool, err := platform.NewPool(ctx, dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect:", err)
		os.Exit(1)
	}
	defer pool.Close()

	n, err := dbmigrate.Apply(ctx, pool, migrations.FS)
	if err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
	fmt.Printf("migrations applied: %d\n", n)
}
