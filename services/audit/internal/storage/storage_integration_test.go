//go:build integration

package storage

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/npavlov/expense-platform/services/audit/internal/events"
)

// TEST_DATABASE_URL is audit_app; TEST_SUPERUSER_URL is the postgres superuser.

func pool(t *testing.T, env string) *pgxpool.Pool {
	t.Helper()
	p, err := Open(context.Background(), os.Getenv(env), "audit-test")
	if err != nil {
		t.Fatalf("%s: %v", env, err)
	}
	t.Cleanup(p.Close)
	return p
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return c
}

func decision() events.Decision {
	now := time.Now().UTC().Truncate(time.Microsecond)
	return events.Decision{
		EventID: uuid.New(), SchemaVersion: 1, OccurredAt: now, CorrelationID: "req-1",
		ClaimID: uuid.New(), EmployeeID: "emp", AmountCents: 1200, Currency: "EUR",
		Decision: "approved", DecidedBy: "rev", DecidedAt: now,
	}
}

func TestRecordDeduplicates(t *testing.T) {
	s := NewDecisions(pool(t, "TEST_DATABASE_URL"))
	d := decision()

	if ins, err := s.Record(ctx(t), d); err != nil || !ins {
		t.Fatalf("first record: %v %v", ins, err)
	}
	if ins, err := s.Record(ctx(t), d); err != nil || ins {
		t.Fatalf("duplicate: inserted=%v err=%v", ins, err)
	}
	changed := d
	changed.AmountCents = 999
	if _, err := s.Record(ctx(t), changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting content: err = %v", err)
	}
	h, err := s.ByClaim(ctx(t), d.ClaimID)
	if err != nil || len(h) != 1 || h[0].AmountCents != 1200 {
		t.Fatalf("history = %+v, %v", h, err)
	}
}

// TestConcurrentRedelivery: many copies of one event arriving at once still
// produce exactly one history entry.
func TestConcurrentRedelivery(t *testing.T) {
	s := NewDecisions(pool(t, "TEST_DATABASE_URL"))
	d := decision()
	var wg sync.WaitGroup
	var mu sync.Mutex
	inserted := 0
	for range 20 {
		wg.Go(func() {
			ins, err := s.Record(ctx(t), d)
			if err != nil {
				t.Errorf("record: %v", err)
			}
			if ins {
				mu.Lock()
				inserted++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	h, _ := s.ByClaim(ctx(t), d.ClaimID)
	if inserted != 1 || len(h) != 1 {
		t.Fatalf("inserted=%d entries=%d, want 1 and 1", inserted, len(h))
	}
}

func TestHistoryIsAppendOnly(t *testing.T) {
	app := pool(t, "TEST_DATABASE_URL")
	super := pool(t, "TEST_SUPERUSER_URL")
	d := decision()
	if _, err := NewDecisions(app).Record(ctx(t), d); err != nil {
		t.Fatal(err)
	}
	for _, p := range []struct {
		name string
		pool *pgxpool.Pool
	}{{"app role", app}, {"superuser (trigger)", super}} {
		for name, sql := range map[string]string{
			"update":   `UPDATE audit.decisions SET decision = 'rejected' WHERE event_id = $1`,
			"delete":   `DELETE FROM audit.decisions WHERE event_id = $1`,
			"truncate": `TRUNCATE audit.decisions`,
		} {
			t.Run(p.name+"/"+name, func(t *testing.T) {
				var err error
				if name == "truncate" {
					_, err = p.pool.Exec(ctx(t), sql)
				} else {
					_, err = p.pool.Exec(ctx(t), sql, d.EventID)
				}
				var pgErr *pgconn.PgError
				if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
					t.Fatalf("err = %v, want insufficient_privilege", err)
				}
			})
		}
	}
}

func TestRuntimeRoleIsolation(t *testing.T) {
	app := pool(t, "TEST_DATABASE_URL")
	for name, sql := range map[string]string{
		"read claims":        `SELECT count(*) FROM expenses.claims`,
		"read outbox":        `SELECT count(*) FROM expenses.outbox`,
		"create table":       `CREATE TABLE audit.evil (id int)`,
		"drop trigger":       `DROP TRIGGER decisions_no_update ON audit.decisions`,
		"rewrite migrations": `DELETE FROM audit.goose_db_version`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := app.Exec(ctx(t), sql)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
				t.Fatalf("err = %v, want insufficient_privilege", err)
			}
		})
	}
}
