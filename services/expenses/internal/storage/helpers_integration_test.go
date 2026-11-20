//go:build integration

package storage

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/npavlov/expense-platform/services/expenses/internal/outbox"
)

// Integration tests run against a database prepared by scripts/test-integration.sh:
// roles bootstrapped, all service migrations applied.
//
//	TEST_DATABASE_URL   expenses_app (the runtime role, used by all store tests)
//	TEST_SUPERUSER_URL  postgres superuser (test setup only)

func testPool(t *testing.T, env string) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv(env)
	if dsn == "" {
		t.Fatalf("%s is not set; run make test-integration", env)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := Open(ctx, dsn, "expenses-test")
	if err != nil {
		t.Fatalf("open %s: %v", env, err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping %s: %v", env, err)
	}
	return pool
}

func appPool(t *testing.T) *pgxpool.Pool { return testPool(t, "TEST_DATABASE_URL") }

func superPool(t *testing.T) *pgxpool.Pool { return testPool(t, "TEST_SUPERUSER_URL") }

func testCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func newTestMetrics() *outbox.Metrics { return outbox.NewMetrics(prometheus.NewRegistry()) }
