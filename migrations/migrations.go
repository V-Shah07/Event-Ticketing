// Package migrations embeds the SQL migration files so they can be applied
// from any binary without depending on the working directory.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
