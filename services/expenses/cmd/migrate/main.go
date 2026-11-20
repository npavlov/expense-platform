// Command migrate applies the expenses database migrations with goose.
//
// Usage: DATABASE_URL=postgres://... migrate [up|down|status|version]
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/npavlov/expense-platform/services/expenses/migrations"
)

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		slog.Error("migrate failed", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return errors.New("DATABASE_URL is not set")
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

	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		return fmt.Errorf("create goose provider: %w", err)
	}

	switch command {
	case "up":
		results, err := provider.Up(ctx)
		for _, r := range results {
			slog.Info("applied", "migration", r.Source.Path, "duration", r.Duration)
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
