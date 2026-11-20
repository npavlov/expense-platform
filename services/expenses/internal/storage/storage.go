// Package storage persists expense claims in PostgreSQL.
//
// Every query is a constant SQL string with positional parameters; no SQL is
// built from input. Tables are schema-qualified, so the search_path does not
// matter.
package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Open creates a connection pool. Connections are established lazily, so the
// service starts even when the database is briefly unavailable; readiness
// reports the real state.
func Open(ctx context.Context, dsn, appName string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse database URL: %w", err)
	}
	cfg.MaxConns = 10
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.ConnConfig.ConnectTimeout = 5 * time.Second
	cfg.ConnConfig.RuntimeParams["application_name"] = appName
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "5000"
	cfg.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = "10000"
	return pgxpool.NewWithConfig(ctx, cfg)
}

// Ready checks that the database answers and the schema is migrated.
func Ready(ctx context.Context, pool *pgxpool.Pool) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var applied bool
	err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM expenses.goose_db_version WHERE version_id >= $1 AND is_applied)`,
		RequiredSchemaVersion,
	).Scan(&applied)
	if err != nil {
		return err
	}
	if !applied {
		return fmt.Errorf("schema version %d not applied", RequiredSchemaVersion)
	}
	return nil
}

// RequiredSchemaVersion is the newest migration this build depends on.
const RequiredSchemaVersion = 4
