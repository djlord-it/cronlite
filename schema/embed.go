// Package schema embeds the SQL migration files.
package schema

import "embed"

// FS holds every NNN_*.sql migration.
//
//go:embed *.sql
var FS embed.FS
