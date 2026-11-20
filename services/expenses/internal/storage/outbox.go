package storage

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/npavlov/expense-platform/services/expenses/internal/outbox"
)

// Outbox implements outbox.Store.
type Outbox struct {
	pool *pgxpool.Pool
}

// NewOutbox returns an outbox store.
func NewOutbox(pool *pgxpool.Pool) *Outbox { return &Outbox{pool: pool} }

var _ outbox.Store = (*Outbox)(nil)

// Lease claims up to n due events for lease. SKIP LOCKED lets several workers
// (replicas) share the queue; the lease makes an event whose worker crashed
// mid-delivery due again once the lease expires, which is how delivery
// recovers after a restart. attempts counts leases, so a crash still counts.
func (s *Outbox) Lease(ctx context.Context, n int, lease time.Duration) ([]outbox.Event, error) {
	rows, err := s.pool.Query(ctx, `
		UPDATE expenses.outbox
		SET locked_until = now() + make_interval(secs => $2), attempts = attempts + 1
		WHERE event_id IN (
			SELECT event_id FROM expenses.outbox
			WHERE delivered_at IS NULL AND parked_at IS NULL
			  AND next_attempt_at <= now()
			  AND (locked_until IS NULL OR locked_until < now())
			ORDER BY created_at
			LIMIT $1
			FOR UPDATE SKIP LOCKED)
		RETURNING event_id, event_type, aggregate_id, payload, coalesce(request_id, ''), attempts, created_at`,
		n, lease.Seconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []outbox.Event
	for rows.Next() {
		var e outbox.Event
		if err := rows.Scan(&e.ID, &e.Type, &e.AggregateID, &e.Payload, &e.RequestID, &e.Attempts, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// MarkDelivered records a successful delivery.
func (s *Outbox) MarkDelivered(ctx context.Context, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE expenses.outbox SET delivered_at = now(), locked_until = NULL, last_error = NULL
		WHERE event_id = $1`, id)
	return err
}

// Retry schedules the next attempt after a transient failure.
func (s *Outbox) Retry(ctx context.Context, id uuid.UUID, next time.Time, reason string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE expenses.outbox SET next_attempt_at = $2, locked_until = NULL, last_error = left($3, 500)
		WHERE event_id = $1`, id, next, reason)
	return err
}

// Park stops automatic delivery of an event until an operator retries it.
func (s *Outbox) Park(ctx context.Context, id uuid.UUID, reason string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE expenses.outbox SET parked_at = now(), locked_until = NULL, last_error = left($2, 500)
		WHERE event_id = $1`, id, reason)
	return err
}

// Stats summarizes the backlog for metrics.
func (s *Outbox) Stats(ctx context.Context) (outbox.Stats, error) {
	var st outbox.Stats
	var oldest *time.Time
	var now time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE parked_at IS NULL),
			count(*) FILTER (WHERE parked_at IS NOT NULL),
			min(created_at),
			now()
		FROM expenses.outbox WHERE delivered_at IS NULL`).Scan(&st.Pending, &st.Parked, &oldest, &now)
	if err != nil {
		return st, err
	}
	if oldest != nil {
		st.OldestAge = now.Sub(*oldest)
	}
	return st, nil
}

// List returns undelivered events for the operator, oldest first.
func (s *Outbox) List(ctx context.Context, parkedOnly bool, limit int) ([]outbox.Status, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT event_id, event_type, aggregate_id, coalesce(request_id, ''), attempts, created_at,
		       next_attempt_at, coalesce(last_error, ''), parked_at
		FROM expenses.outbox
		WHERE delivered_at IS NULL AND (NOT $1 OR parked_at IS NOT NULL)
		ORDER BY created_at
		LIMIT $2`, parkedOnly, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []outbox.Status{}
	for rows.Next() {
		var st outbox.Status
		if err := rows.Scan(&st.EventID, &st.EventType, &st.ClaimID, &st.RequestID, &st.Attempts, &st.CreatedAt,
			&st.NextAttemptAt, &st.LastError, &st.ParkedAt); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// Requeue makes an undelivered (typically parked) event due now and resets
// its attempt budget. last_error is kept for the record until the next attempt.
func (s *Outbox) Requeue(ctx context.Context, id uuid.UUID) error {
	var got uuid.UUID
	err := s.pool.QueryRow(ctx, `
		UPDATE expenses.outbox
		SET parked_at = NULL, next_attempt_at = now(), locked_until = NULL, attempts = 0
		WHERE event_id = $1 AND delivered_at IS NULL
		RETURNING event_id`, id).Scan(&got)
	if errors.Is(err, pgx.ErrNoRows) {
		return outbox.ErrNotFound
	}
	return err
}
