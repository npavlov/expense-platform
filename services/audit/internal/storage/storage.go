// Package storage persists the decision history in PostgreSQL.
package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/npavlov/expense-platform/services/audit/internal/events"
)

// ErrConflict means an event ID was seen before with different content.
var ErrConflict = errors.New("event ID already recorded with different content")

// RequiredSchemaVersion is the newest migration this build depends on.
const RequiredSchemaVersion = 2

// Open creates a lazily-connecting pool.
func Open(ctx context.Context, dsn, appName string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse database URL: %w", err)
	}
	cfg.MaxConns = 10
	cfg.ConnConfig.ConnectTimeout = 5 * time.Second
	cfg.ConnConfig.RuntimeParams["application_name"] = appName
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "5000"
	return pgxpool.NewWithConfig(ctx, cfg)
}

// Ready checks that the database answers and the schema is migrated.
func Ready(ctx context.Context, pool *pgxpool.Pool) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var applied bool
	err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM audit.goose_db_version WHERE version_id >= $1 AND is_applied)`,
		RequiredSchemaVersion).Scan(&applied)
	if err != nil {
		return err
	}
	if !applied {
		return fmt.Errorf("schema version %d not applied", RequiredSchemaVersion)
	}
	return nil
}

// Decisions is the decision history.
type Decisions struct {
	pool *pgxpool.Pool
}

// NewDecisions returns the store.
func NewDecisions(pool *pgxpool.Pool) *Decisions { return &Decisions{pool: pool} }

// Record stores the decision once. It returns inserted=false for a duplicate
// delivery of the same event, and ErrConflict if the same event ID arrives
// with different content.
func (s *Decisions) Record(ctx context.Context, d events.Decision) (inserted bool, err error) {
	hash := d.ContentHash()
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO audit.decisions (event_id, schema_version, claim_id, employee_id, amount_cents, currency,
		                             decision, decided_by, decided_at, occurred_at, correlation_id, content_hash)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (event_id) DO NOTHING`,
		d.EventID, d.SchemaVersion, d.ClaimID, d.EmployeeID, d.AmountCents, d.Currency,
		d.Decision, d.DecidedBy, d.DecidedAt, d.OccurredAt, nullable(d.CorrelationID), hash[:])
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 1 {
		return true, nil
	}
	var stored []byte
	if err := s.pool.QueryRow(ctx, `SELECT content_hash FROM audit.decisions WHERE event_id = $1`, d.EventID).Scan(&stored); err != nil {
		return false, err
	}
	if string(stored) != string(hash[:]) {
		return false, ErrConflict
	}
	return false, nil
}

// Entry is one recorded decision.
type Entry struct {
	EventID       uuid.UUID `json:"event_id"`
	ClaimID       uuid.UUID `json:"claim_id"`
	EmployeeID    string    `json:"employee_id"`
	AmountCents   int64     `json:"amount_cents"`
	Currency      string    `json:"currency"`
	Decision      string    `json:"decision"`
	DecidedBy     string    `json:"decided_by"`
	DecidedAt     time.Time `json:"decided_at"`
	RecordedAt    time.Time `json:"recorded_at"`
	CorrelationID string    `json:"correlation_id,omitempty"`
}

// ByClaim returns the history of one claim in decision order.
func (s *Decisions) ByClaim(ctx context.Context, claimID uuid.UUID) ([]Entry, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT event_id, claim_id, employee_id, amount_cents, currency, decision, decided_by,
		       decided_at, received_at, coalesce(correlation_id, '')
		FROM audit.decisions WHERE claim_id = $1
		ORDER BY decided_at, event_id`, claimID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.EventID, &e.ClaimID, &e.EmployeeID, &e.AmountCents, &e.Currency, &e.Decision,
			&e.DecidedBy, &e.DecidedAt, &e.RecordedAt, &e.CorrelationID); err != nil {
			return nil, err
		}
		e.DecidedAt, e.RecordedAt = e.DecidedAt.UTC(), e.RecordedAt.UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
