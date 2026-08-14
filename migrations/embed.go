// Package migrations embeds the SQL migration files next to this file so the
// binary can migrate a fresh database at startup, while the same files stay
// usable from the golang-migrate CLI for dev/ops (AGENTS.md §9).
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
