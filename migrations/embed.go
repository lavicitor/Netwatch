// Package migrations embeds the SQL migration files so the binary can carry
// its own schema -- no separate migration step or CLI needed at deploy time.
// See internal/store.runMigrations for how these are applied (via goose).
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
