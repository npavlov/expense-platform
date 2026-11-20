//go:build integration

package storage

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// checkViolation is the PostgreSQL SQLSTATE for a failed CHECK constraint.
const checkViolation = "23514"

// connect opens a connection to the database from TEST_DATABASE_URL,
// which must already have the migrations applied (see make test-integration).
func connect(t *testing.T) *pgx.Conn {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("TEST_DATABASE_URL is not set; run make test-integration")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect to test database: %v", err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	return conn
}

func TestClaimsTableEnforcesRules(t *testing.T) {
	conn := connect(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	const insert = `
		INSERT INTO expenses.claims (id, employee_id, amount_cents, description)
		VALUES ($1, $2, $3, $4)
		RETURNING status, created_at`

	t.Run("valid claim gets defaults from the database", func(t *testing.T) {
		var (
			status    string
			createdAt time.Time
		)
		err := conn.QueryRow(ctx, insert, uuid.New(), "alice", 1200, "Taxi").
			Scan(&status, &createdAt)
		if err != nil {
			t.Fatalf("insert claim: %v", err)
		}

		if status != "pending" {
			t.Errorf("status = %q, want pending", status)
		}
		if createdAt.IsZero() {
			t.Error("created_at is zero, want it set by the database")
		}
	})

	t.Run("zero amount is rejected by the database", func(t *testing.T) {
		_, err := conn.Exec(ctx, insert, uuid.New(), "alice", 0, "Free taxi")

		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) {
			t.Fatalf("err = %v, want a PostgreSQL error", err)
		}
		if pgErr.Code != checkViolation {
			t.Errorf("SQLSTATE = %s, want %s (check_violation)", pgErr.Code, checkViolation)
		}
		if pgErr.ConstraintName != "claims_amount_positive" {
			t.Errorf("constraint = %q, want claims_amount_positive", pgErr.ConstraintName)
		}
	})
}
