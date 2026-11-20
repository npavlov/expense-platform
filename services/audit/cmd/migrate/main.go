// Command migrate applies the audit database migrations with goose.
//
// Usage: DATABASE_URL=postgres://... migrate [up|down|status|version]
//
// It connects as audit_migrator, the owner of the audit schema.
package main

import (
	"github.com/npavlov/expense-platform/libs/platform/dbmigrate"
	"github.com/npavlov/expense-platform/services/audit/migrations"
)

func main() { dbmigrate.Main("audit", "audit", migrations.FS) }
