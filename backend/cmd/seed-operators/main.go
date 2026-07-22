// cmd/seed-operators — CI/dev-only operator provisioning for the portal RBAC
// end-to-end journeys. It reads a JSON operator list from TCP_SEED_OPERATORS and
// creates any that do not yet exist, through the SAME bootstrap path the tests
// use (repo.Admins.CreateWithRole). It is:
//   - GUARDED by TCP_SEED_ALLOW=1 so it can never run by accident (e.g. against
//     a real database) — provisioning operators is a privilege-granting act.
//   - IDEMPOTENT — an operator whose actor already exists is skipped, so a
//     re-run is a no-op.
//
// This is the CI seed harness only. How the FIRST real operator is provisioned
// in production is a separate pre-pilot operating-model decision (it ties to the
// Gate B #2 out-of-band-lifecycle conclusion), not this tool.
package main

import (
	"context"
	"encoding/json"
	"log"
	"os"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
)

type seedOperator struct {
	AdminID string `json:"admin_id"`
	Actor   string `json:"actor"`
	Key     string `json:"key"`
	Role    string `json:"role"`
	Scope   string `json:"scope"`
}

func main() {
	if os.Getenv("TCP_SEED_ALLOW") != "1" {
		log.Fatal("seed-operators: refusing to run without TCP_SEED_ALLOW=1 (CI/dev only)")
	}
	raw := os.Getenv("TCP_SEED_OPERATORS")
	if raw == "" {
		log.Fatal("seed-operators: TCP_SEED_OPERATORS (JSON array of {actor,key,role,scope}) is required")
	}
	var ops []seedOperator
	if err := json.Unmarshal([]byte(raw), &ops); err != nil {
		log.Fatalf("seed-operators: bad TCP_SEED_OPERATORS JSON: %v", err)
	}

	dsn := os.Getenv("TCP_ADMIN_DSN")
	if dsn == "" {
		dsn = "postgres://postgres:devlocal@localhost:5434/telco_credit"
	}
	ctx := context.Background()
	pool, err := platform.NewPool(ctx, dsn)
	if err != nil {
		log.Fatalf("seed-operators: db connect: %v", err)
	}
	defer pool.Close()

	admins := &repo.Admins{Pool: pool}
	created := 0
	for _, o := range ops {
		if o.Actor == "" || o.Key == "" || o.Role == "" {
			log.Fatalf("seed-operators: each operator needs actor, key and role (got %+v)", o)
		}
		scope := o.Scope
		if scope == "" {
			scope = "*"
		}
		var exists bool
		if err := pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM admin_credentials WHERE actor=$1)`, o.Actor).Scan(&exists); err != nil {
			log.Fatalf("seed-operators: existence check %q: %v", o.Actor, err)
		}
		if exists {
			log.Printf("seed-operators: %s (%s) exists — skipping", o.Actor, o.Role)
			continue
		}
		adminID := o.AdminID
		if adminID == "" {
			adminID = "seed_" + o.Actor
		}
		if err := admins.CreateWithRole(ctx, adminID, o.Actor, o.Key, o.Role, scope); err != nil {
			log.Fatalf("seed-operators: create %q: %v", o.Actor, err)
		}
		created++
		log.Printf("seed-operators: created %s role=%s scope=%s", o.Actor, o.Role, scope)
	}
	log.Printf("seed-operators: done — %d created, %d total in list", created, len(ops))
}
