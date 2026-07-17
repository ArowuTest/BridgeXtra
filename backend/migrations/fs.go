// Package migrations embeds the SQL migration files so every binary (api,
// worker, migrate, tests) carries the exact schema it was built against —
// the boot-time self-migration lesson (project memory: boot-migrate).
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
