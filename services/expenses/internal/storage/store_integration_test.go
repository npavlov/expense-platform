//go:build integration

package storage

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/npavlov/expense-platform/services/expenses/internal/claims"
)

const insufficientPrivilege = "42501"

func submit(t *testing.T, s *Claims, employee string) claims.Claim {
	t.Helper()
	n := claims.NewClaim{AmountCents: 1200, Description: "Taxi"}
	c, _, err := s.Create(testCtx(t), uuid.Must(uuid.NewV7()), employee, uuid.NewString(), n.Hash(), n)
	if err != nil {
		t.Fatalf("create claim: %v", err)
	}
	return c
}

func TestCreateIsIdempotent(t *testing.T) {
	s := NewClaims(appPool(t))
	ctx := testCtx(t)
	emp := "emp-" + uuid.NewString()
	key := "key-" + uuid.NewString()
	n := claims.NewClaim{AmountCents: 1200, Description: "Taxi"}

	first, replayed, err := s.Create(ctx, uuid.New(), emp, key, n.Hash(), n)
	if err != nil || replayed {
		t.Fatalf("first create: replayed=%v err=%v", replayed, err)
	}
	again, replayed, err := s.Create(ctx, uuid.New(), emp, key, n.Hash(), n)
	if err != nil || !replayed || again.ID != first.ID {
		t.Fatalf("retry: id=%v replayed=%v err=%v, want id %v", again.ID, replayed, err, first.ID)
	}
	other := claims.NewClaim{AmountCents: 9900, Description: "Taxi"}
	if _, _, err := s.Create(ctx, uuid.New(), emp, key, other.Hash(), other); !errors.Is(err, claims.ErrIdempotencyKeyReused) {
		t.Fatalf("reuse with other body: err = %v", err)
	}
	// Same key, different employee: independent.
	if c, replayed, err := s.Create(ctx, uuid.New(), "emp-"+uuid.NewString(), key, n.Hash(), n); err != nil || replayed || c.ID == first.ID {
		t.Fatalf("other employee: id=%v replayed=%v err=%v", c.ID, replayed, err)
	}
}

// TestConcurrentCreateWithSameKey simulates a client that retries before the
// first response arrived: in-flight duplicates must not create a second claim.
func TestConcurrentCreateWithSameKey(t *testing.T) {
	pool := appPool(t)
	s := NewClaims(pool)
	ctx := testCtx(t)
	emp := "emp-" + uuid.NewString()
	key := "key-" + uuid.NewString()
	n := claims.NewClaim{AmountCents: 1200, Description: "Taxi"}

	const workers = 20
	ids := make(chan uuid.UUID, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			c, _, err := s.Create(ctx, uuid.New(), emp, key, n.Hash(), n)
			if err != nil {
				t.Errorf("create: %v", err)
				return
			}
			ids <- c.ID
		})
	}
	wg.Wait()
	close(ids)

	distinct := map[uuid.UUID]bool{}
	for id := range ids {
		distinct[id] = true
	}
	var stored int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM expenses.claims WHERE employee_id = $1`, emp).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if len(distinct) != 1 || stored != 1 {
		t.Fatalf("got %d distinct IDs and %d stored claims, want 1 and 1", len(distinct), stored)
	}
}

func TestDecideWritesClaimAndEventTogether(t *testing.T) {
	pool := appPool(t)
	s := NewClaims(pool)
	ctx := testCtx(t)
	c := submit(t, s, "emp-"+uuid.NewString())

	decided, replayed, err := s.Decide(ctx, c.ID, "rev-1", claims.DecisionApproved, "req-123")
	if err != nil || replayed {
		t.Fatalf("decide: replayed=%v err=%v", replayed, err)
	}
	if decided.Status != claims.StatusApproved || decided.DecidedBy == nil || *decided.DecidedBy != "rev-1" || decided.DecidedAt == nil {
		t.Fatalf("decided claim = %+v", decided)
	}

	var payload []byte
	var requestID string
	err = pool.QueryRow(ctx, `SELECT payload, request_id FROM expenses.outbox WHERE aggregate_id = $1`, c.ID).Scan(&payload, &requestID)
	if err != nil {
		t.Fatalf("outbox row: %v", err)
	}
	var e claims.ExpenseDecidedV1
	if err := json.Unmarshal(payload, &e); err != nil {
		t.Fatal(err)
	}
	if e.Data.ClaimID != c.ID || e.Data.Decision != claims.DecisionApproved || e.Data.AmountCents != 1200 || e.CorrelationID != "req-123" || requestID != "req-123" {
		t.Fatalf("event = %+v", e)
	}

	if _, replayed, err := s.Decide(ctx, c.ID, "rev-1", claims.DecisionApproved, "req-124"); err != nil || !replayed {
		t.Fatalf("retry: replayed=%v err=%v", replayed, err)
	}
	if _, _, err := s.Decide(ctx, c.ID, "rev-2", claims.DecisionRejected, ""); !errors.Is(err, claims.ErrAlreadyDecided) {
		t.Fatalf("second reviewer: err = %v", err)
	}
	if _, _, err := s.Decide(ctx, uuid.New(), "rev-1", claims.DecisionApproved, ""); !errors.Is(err, claims.ErrNotFound) {
		t.Fatalf("unknown claim: err = %v", err)
	}
	var events int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM expenses.outbox WHERE aggregate_id = $1`, c.ID).Scan(&events)
	if events != 1 {
		t.Fatalf("outbox has %d events for the claim, want 1", events)
	}
}

// TestConcurrentDecisions races approvals and rejections from two reviewers.
// Exactly one decision may win; the claim and the outbox must agree with it.
func TestConcurrentDecisions(t *testing.T) {
	pool := appPool(t)
	s := NewClaims(pool)
	ctx := testCtx(t)
	c := submit(t, s, "emp-"+uuid.NewString())

	type result struct {
		reviewer string
		decision claims.Decision
		replayed bool
		err      error
	}
	results := make(chan result, 40)
	var wg sync.WaitGroup
	for i := range 40 {
		reviewer := []string{"rev-a", "rev-b"}[i%2]
		decision := []claims.Decision{claims.DecisionApproved, claims.DecisionRejected}[(i/2)%2]
		wg.Go(func() {
			_, replayed, err := s.Decide(ctx, c.ID, reviewer, decision, "")
			results <- result{reviewer, decision, replayed, err}
		})
	}
	wg.Wait()
	close(results)

	var winners []result
	for r := range results {
		switch {
		case r.err == nil && !r.replayed:
			winners = append(winners, r)
		case r.err == nil && r.replayed, errors.Is(r.err, claims.ErrAlreadyDecided):
		default:
			t.Errorf("unexpected error: %v", r.err)
		}
	}
	if len(winners) != 1 {
		t.Fatalf("%d decisions won, want exactly 1", len(winners))
	}
	final, err := s.Get(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(final.Status) != string(winners[0].decision) || *final.DecidedBy != winners[0].reviewer {
		t.Fatalf("final state %s by %s, winner %+v", final.Status, *final.DecidedBy, winners[0])
	}
	var events int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM expenses.outbox WHERE aggregate_id = $1`, c.ID).Scan(&events)
	if events != 1 {
		t.Fatalf("outbox has %d events, want 1", events)
	}
}

func TestListQueries(t *testing.T) {
	s := NewClaims(appPool(t))
	ctx := testCtx(t)
	emp := "emp-" + uuid.NewString()
	reviewer := "rev-" + uuid.NewString()
	a := submit(t, s, emp)
	b := submit(t, s, emp)
	own := submit(t, s, reviewer)

	mine, err := s.ListByEmployee(ctx, emp, 10)
	if err != nil || len(mine) != 2 || mine[0].ID != b.ID || mine[1].ID != a.ID {
		t.Fatalf("ListByEmployee = %v, %v (want newest first)", ids(mine), err)
	}
	if _, _, err := s.Decide(ctx, a.ID, reviewer, claims.DecisionRejected, ""); err != nil {
		t.Fatal(err)
	}
	queue, err := s.ListPending(ctx, reviewer, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range queue {
		if c.ID == a.ID || c.ID == own.ID {
			t.Fatalf("queue contains decided or own claim %v", c.ID)
		}
	}
}

func ids(cs []claims.Claim) []uuid.UUID {
	out := make([]uuid.UUID, len(cs))
	for i, c := range cs {
		out[i] = c.ID
	}
	return out
}

// TestDatabaseEnforcesRules bypasses the service and writes directly with the
// runtime role: the constraints and column privileges still hold.
func TestDatabaseEnforcesRules(t *testing.T) {
	pool := appPool(t)
	ctx := testCtx(t)
	c := submit(t, NewClaims(pool), "emp-"+uuid.NewString())

	cases := []struct {
		name       string
		sql        string
		wantCode   string
		constraint string
	}{
		{"self approval", `UPDATE expenses.claims SET status = 'approved', decided_by = employee_id, decided_at = now() WHERE id = $1`, checkViolation, "claims_no_self_decision"},
		{"approved without reviewer", `UPDATE expenses.claims SET status = 'approved' WHERE id = $1`, checkViolation, "claims_decision_consistent"},
		{"amount changed after submit", `UPDATE expenses.claims SET amount_cents = 1 WHERE id = $1`, insufficientPrivilege, ""},
		{"description changed after submit", `UPDATE expenses.claims SET description = 'Dinner' WHERE id = $1`, insufficientPrivilege, ""},
		{"delete claim", `DELETE FROM expenses.claims WHERE id = $1`, insufficientPrivilege, ""},
		{"rewrite event payload", `UPDATE expenses.outbox SET payload = '{}' WHERE aggregate_id = $1`, insufficientPrivilege, ""},
		{"delete idempotency keys", `DELETE FROM expenses.idempotency_keys WHERE claim_id = $1`, insufficientPrivilege, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := pool.Exec(ctx, tc.sql, c.ID)
			assertPgError(t, err, tc.wantCode, tc.constraint)
		})
	}
	t.Run("too large amount", func(t *testing.T) {
		_, err := pool.Exec(ctx, `INSERT INTO expenses.claims (id, employee_id, amount_cents, description) VALUES ($1, 'e', 10000001, 'x')`, uuid.New())
		assertPgError(t, err, checkViolation, "claims_amount_max")
	})
}

// TestRuntimeRoleIsolation checks the database role boundary between services.
func TestRuntimeRoleIsolation(t *testing.T) {
	pool := appPool(t)
	ctx := testCtx(t)
	for name, sql := range map[string]string{
		"read audit history":      `SELECT count(*) FROM audit.decisions`,
		"write audit history":     `INSERT INTO audit.decisions (event_id) VALUES (gen_random_uuid())`,
		"create table (DDL)":      `CREATE TABLE expenses.evil (id int)`,
		"create table in public":  `CREATE TABLE public.evil (id int)`,
		"drop table":              `DROP TABLE expenses.claims`,
		"alter migration history": `DELETE FROM expenses.goose_db_version`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := pool.Exec(ctx, sql)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != insufficientPrivilege {
				t.Fatalf("err = %v, want permission denied", err)
			}
		})
	}
}

func assertPgError(t *testing.T, err error, code, constraint string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("err = %v, want PostgreSQL error %s", err, code)
	}
	if pgErr.Code != code {
		t.Fatalf("SQLSTATE = %s (%s), want %s", pgErr.Code, pgErr.Message, code)
	}
	if constraint != "" && pgErr.ConstraintName != constraint {
		t.Fatalf("constraint = %q, want %q", pgErr.ConstraintName, constraint)
	}
}
