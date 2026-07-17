// Package dbmigrate applies numbered SQL migrations from an fs.FS in order,
// exactly once, under an advisory lock so concurrent runners cannot interleave.
//
// Lessons baked in (project memory):
//   - fresh-DB validation: the test suite applies every migration from zero on
//     each run, so a migration that only works on an already-populated DB fails
//     immediately (reference_migration_from_zero_lesson);
//   - RLS: tables use ENABLE (not FORCE) row level security, and migrations run
//     as the table owner, so seeds never fight RLS policies
//     (reference_migrator_force_rls).
package dbmigrate

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// advisoryLockKey serialises migration runners across processes.
const advisoryLockKey int64 = 0x7C_C9_2026

type Migration struct {
	Version int
	Name    string
	SQL     string
}

// Load reads *.sql files named NNNN_description.sql from dir, ordered by NNNN.
func Load(fsys fs.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("read migrations dir: %w", err)
	}
	var ms []Migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		base := strings.TrimSuffix(e.Name(), ".sql")
		numPart, _, found := strings.Cut(base, "_")
		if !found {
			return nil, fmt.Errorf("migration %q: name must be NNNN_description.sql", e.Name())
		}
		v, err := strconv.Atoi(numPart)
		if err != nil {
			return nil, fmt.Errorf("migration %q: bad version prefix: %w", e.Name(), err)
		}
		body, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			return nil, fmt.Errorf("read %q: %w", e.Name(), err)
		}
		ms = append(ms, Migration{Version: v, Name: e.Name(), SQL: string(body)})
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].Version < ms[j].Version })
	for i := 1; i < len(ms); i++ {
		if ms[i].Version == ms[i-1].Version {
			return nil, fmt.Errorf("duplicate migration version %d (%s, %s)", ms[i].Version, ms[i-1].Name, ms[i].Name)
		}
	}
	return ms, nil
}

// Apply runs all unapplied migrations, each inside its own transaction.
// It returns the number of migrations applied.
func Apply(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS) (int, error) {
	ms, err := Load(fsys)
	if err != nil {
		return 0, err
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return 0, fmt.Errorf("acquire conn: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", advisoryLockKey); err != nil {
		return 0, fmt.Errorf("advisory lock: %w", err)
	}
	defer func() {
		// Best-effort unlock: the session lock dies with the connection anyway.
		_, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", advisoryLockKey)
	}()

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INT PRIMARY KEY,
		name       TEXT NOT NULL,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`); err != nil {
		return 0, fmt.Errorf("ensure schema_migrations: %w", err)
	}

	applied := map[int]bool{}
	rows, err := conn.Query(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return 0, fmt.Errorf("list applied: %w", err)
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return 0, err
		}
		applied[v] = true
	}
	rows.Close()
	if rows.Err() != nil {
		return 0, rows.Err()
	}

	n := 0
	for _, m := range ms {
		if applied[m.Version] {
			continue
		}
		if err := applyOne(ctx, conn.Conn(), m); err != nil {
			return n, fmt.Errorf("migration %s: %w", m.Name, err)
		}
		n++
	}
	return n, nil
}

func applyOne(ctx context.Context, conn *pgx.Conn, m Migration) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if _, err := tx.Exec(ctx, m.SQL); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		"INSERT INTO schema_migrations (version, name) VALUES ($1, $2)", m.Version, m.Name); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
