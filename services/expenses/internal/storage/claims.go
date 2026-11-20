package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/npavlov/expense-platform/services/expenses/internal/claims"
)

// Claims implements claims.Store.
type Claims struct {
	pool *pgxpool.Pool
}

// NewClaims returns a claim store.
func NewClaims(pool *pgxpool.Pool) *Claims { return &Claims{pool: pool} }

var _ claims.Store = (*Claims)(nil)

const claimColumns = `id, employee_id, amount_cents, description, status, created_at, decided_by, decided_at`

func scanClaim(row pgx.Row) (claims.Claim, error) {
	var c claims.Claim
	var status string
	err := row.Scan(&c.ID, &c.EmployeeID, &c.AmountCents, &c.Description, &status, &c.CreatedAt, &c.DecidedBy, &c.DecidedAt)
	c.Status = claims.Status(status)
	return c, err
}

// Create inserts the idempotency key first. A concurrent request with the same
// key blocks on the primary key until the first transaction finishes, then
// takes the "already exists" path, so in-flight duplicates also get the
// original claim instead of creating a second one.
func (s *Claims) Create(ctx context.Context, id uuid.UUID, employeeID, key string, hash [32]byte, n claims.NewClaim) (claims.Claim, bool, error) {
	var out claims.Claim
	var replayed bool
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			INSERT INTO expenses.idempotency_keys (employee_id, key, request_hash, claim_id)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (employee_id, key) DO NOTHING`,
			employeeID, key, hash[:], id)
		if err != nil {
			return fmt.Errorf("insert idempotency key: %w", err)
		}

		if tag.RowsAffected() == 0 {
			var storedHash []byte
			var claimID uuid.UUID
			err := tx.QueryRow(ctx, `
				SELECT request_hash, claim_id FROM expenses.idempotency_keys
				WHERE employee_id = $1 AND key = $2`,
				employeeID, key).Scan(&storedHash, &claimID)
			if err != nil {
				return fmt.Errorf("read idempotency key: %w", err)
			}
			if string(storedHash) != string(hash[:]) {
				return claims.ErrIdempotencyKeyReused
			}
			out, err = scanClaim(tx.QueryRow(ctx, `SELECT `+claimColumns+` FROM expenses.claims WHERE id = $1`, claimID))
			replayed = true
			return err
		}

		out, err = scanClaim(tx.QueryRow(ctx, `
			INSERT INTO expenses.claims (id, employee_id, amount_cents, description)
			VALUES ($1, $2, $3, $4)
			RETURNING `+claimColumns,
			id, employeeID, n.AmountCents, n.Description))
		if err != nil {
			return fmt.Errorf("insert claim: %w", err)
		}
		return nil
	})
	return out, replayed, err
}

// Get returns a claim by ID.
func (s *Claims) Get(ctx context.Context, id uuid.UUID) (claims.Claim, error) {
	c, err := scanClaim(s.pool.QueryRow(ctx, `SELECT `+claimColumns+` FROM expenses.claims WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return claims.Claim{}, claims.ErrNotFound
	}
	return c, err
}

// ListByEmployee returns the employee's claims, newest first.
func (s *Claims) ListByEmployee(ctx context.Context, employeeID string, limit int) ([]claims.Claim, error) {
	return s.list(ctx, `
		SELECT `+claimColumns+` FROM expenses.claims
		WHERE employee_id = $1
		ORDER BY created_at DESC, id DESC
		LIMIT $2`, employeeID, limit)
}

// ListPending returns the oldest pending claims, excluding the reviewer's own.
func (s *Claims) ListPending(ctx context.Context, excludeEmployee string, limit int) ([]claims.Claim, error) {
	return s.list(ctx, `
		SELECT `+claimColumns+` FROM expenses.claims
		WHERE status = 'pending' AND employee_id <> $1
		ORDER BY created_at, id
		LIMIT $2`, excludeEmployee, limit)
}

func (s *Claims) list(ctx context.Context, sql string, args ...any) ([]claims.Claim, error) {
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []claims.Claim{}
	for rows.Next() {
		c, err := scanClaim(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Decide serializes decisions on one claim with SELECT ... FOR UPDATE. The
// status change and the ExpenseDecided outbox row commit together or not at
// all, so a decision can never exist without its event, or the reverse.
func (s *Claims) Decide(ctx context.Context, id uuid.UUID, reviewer string, d claims.Decision, correlationID string) (claims.Claim, bool, error) {
	var out claims.Claim
	var replayed bool
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		current, err := scanClaim(tx.QueryRow(ctx, `SELECT `+claimColumns+` FROM expenses.claims WHERE id = $1 FOR UPDATE`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return claims.ErrNotFound
		}
		if err != nil {
			return err
		}
		replayed, err = claims.CheckDecision(current, reviewer, d)
		if err != nil {
			out = current
			return err
		}
		if replayed {
			out = current
			return nil
		}

		out, err = scanClaim(tx.QueryRow(ctx, `
			UPDATE expenses.claims
			SET status = $2, decided_by = $3, decided_at = now()
			WHERE id = $1 AND status = 'pending'
			RETURNING `+claimColumns,
			id, string(d), reviewer))
		if err != nil {
			return fmt.Errorf("update claim: %w", err)
		}

		event, err := claims.NewExpenseDecided(out, correlationID)
		if err != nil {
			return err
		}
		payload, err := json.Marshal(event)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO expenses.outbox (event_id, event_type, aggregate_id, payload, request_id)
			VALUES ($1, $2, $3, $4, $5)`,
			event.EventID, event.EventType, out.ID, payload, nullable(correlationID))
		if err != nil {
			return fmt.Errorf("insert outbox event: %w", err)
		}
		return nil
	})
	return out, replayed, err
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
