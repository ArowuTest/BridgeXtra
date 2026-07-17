// Package dbroles rotates the runtime database-role passwords from the
// environment after migrations run. Migration 0001 creates tcp_app/tcp_worker
// with documented local-dev passwords; production deployments MUST set
// TCP_APP_PASSWORD / TCP_WORKER_PASSWORD from the secrets manager so the dev
// values never reach a reachable database (V2-SEC-005: credentials come from
// secrets, never from SQL files). Unset env vars are a no-op, which keeps
// local development untouched.
//
// This is platform bootstrap SQL (like dbmigrate's own bookkeeping), not
// business data access — the ALL-SQL-in-repo rule governs the latter.
package dbroles

import (
	"context"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
)

// envPasswords maps role name -> environment variable carrying its password.
var envPasswords = map[string]string{
	"tcp_app":    "TCP_APP_PASSWORD",
	"tcp_worker": "TCP_WORKER_PASSWORD",
}

// ApplyPasswords sets each role's password from its env var when present.
// Returns the roles whose passwords were applied. The connected role must own
// the roles (their creator) or hold CREATEROLE — true for both the local
// superuser and a managed-Postgres database owner.
func ApplyPasswords(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	var rotated []string
	for role, envKey := range envPasswords {
		pwd := os.Getenv(envKey)
		if pwd == "" {
			continue
		}
		// ALTER ROLE cannot take bind parameters; quote server-side with
		// format() %I/%L so the values are never string-concatenated into SQL.
		var stmt string
		if err := pool.QueryRow(ctx,
			`SELECT format('ALTER ROLE %I WITH PASSWORD %L', $1::text, $2::text)`,
			role, pwd).Scan(&stmt); err != nil {
			return rotated, fmt.Errorf("quote password statement for %s: %w", role, err)
		}
		if _, err := pool.Exec(ctx, stmt); err != nil {
			return rotated, fmt.Errorf("set password for %s: %w", role, err)
		}
		rotated = append(rotated, role)
	}
	return rotated, nil
}
