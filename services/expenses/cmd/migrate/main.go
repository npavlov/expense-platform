// Command migrate applies the expenses database migrations with goose.
//
// Usage: DATABASE_URL=postgres://... migrate [up|down|status|version]
//
// It connects as expenses_migrator, the owner of the expenses schema.
package main

import (
	"github.com/npavlov/expense-platform/libs/platform/dbmigrate"
	"github.com/npavlov/expense-platform/services/expenses/migrations"
)

func main() { dbmigrate.Main("expenses", "expenses", migrations.FS) }
