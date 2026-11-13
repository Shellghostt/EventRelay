// Package migrations embeds the SQL migration files so that the binary is
// self-contained: there is nothing to copy next to the executable.
package migrations

import "embed"

// FS contains every .sql migration, applied in filename order.
//
//go:embed *.sql
var FS embed.FS
