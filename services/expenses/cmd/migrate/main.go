// Command migrate applies the expenses database migrations with goose.
//
// Usage: DATABASE_URL=postgres://... migrate [up|down|status|version]
//
// It connects as expenses_migrator, the owner of the expenses schema. The
// version table lives inside that schema, so each service tracks its own
// migrations in the shared database.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	"github.com/npavlov/expense-platform/libs/platform/logging"
	"github.com/npavlov/expense-platform/libs/platform/server"
	"github.com/npavlov/expense-platform/services/expenses/migrations"
)

const versionTable = "expenses.goose_db_version"

func main() {
	logging.New("expenses-migrate")
	if err := run(context.Background(), os.Args[1:]); err != nil {
		slog.Error("migrate failed", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	dsn, err := server.EnvSecret("DATABASE_URL")
	if err != nil {
		return err
	}
	command := "up"
	if len(args) > 0 {
		command = args[0]
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()
	if err := waitForDB(ctx, db, 2*time.Minute); err != nil {
		return err
	}

	// A session advisory lock serializes concurrent runs (two jobs, a retry).
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return err
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS,
		goose.WithTableName(versionTable),
		goose.WithSessionLocker(locker),
	)
	if err != nil {
		return fmt.Errorf("create goose provider: %w", err)
	}

	switch command {
	case "up":
		results, err := provider.Up(ctx)
		for _, r := range results {
			slog.Info("applied", "migration", r.Source.Path, "duration", r.Duration)
		}
		if err == nil && len(results) == 0 {
			slog.Info("schema is up to date")
		}
		return err
	case "down":
		r, err := provider.Down(ctx)
		if r != nil {
			slog.Info("rolled back", "migration", r.Source.Path)
		}
		return err
	case "status":
		statuses, err := provider.Status(ctx)
		for _, s := range statuses {
			slog.Info("status", "migration", s.Source.Path, "state", s.State)
		}
		return err
	case "version":
		v, err := provider.GetDBVersion(ctx)
		slog.Info("version", "version", v)
		return err
	default:
		return fmt.Errorf("unknown command %q (want up, down, status or version)", command)
	}
}

// waitForDB retries until the database accepts the connection. In Kubernetes
// the migration job may start before PostgreSQL or the role bootstrap is done.
func waitForDB(ctx context.Context, db *sql.DB, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		err := db.PingContext(pctx)
		cancel()
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.Join(errors.New("database not reachable"), err)
		}
		slog.Info("waiting for database", "err", err)
		time.Sleep(2 * time.Second)
	}
}
