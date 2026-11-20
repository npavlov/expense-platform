// Package migrations embeds the SQL migrations of the expenses service,
// so the migrate binary carries them and needs no files at runtime.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
