package claims

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// Store persists claims. The PostgreSQL implementation is in internal/storage.
type Store interface {
	// Create inserts the claim unless (employeeID, key) was used before. On a
	// repeat with the same hash it returns the original claim and replayed=true;
	// with a different hash it returns ErrIdempotencyKeyReused.
	Create(ctx context.Context, id uuid.UUID, employeeID, key string, hash [32]byte, n NewClaim) (c Claim, replayed bool, err error)
	Get(ctx context.Context, id uuid.UUID) (Claim, error)
	ListByEmployee(ctx context.Context, employeeID string, limit int) ([]Claim, error)
	ListPending(ctx context.Context, excludeEmployee string, limit int) ([]Claim, error)
	// Decide locks the claim, applies CheckDecision and, for a new decision,
	// updates the claim and writes the ExpenseDecided event in one transaction.
	Decide(ctx context.Context, id uuid.UUID, reviewer string, d Decision, correlationID string) (c Claim, replayed bool, err error)
}

// Service applies the claim rules on top of a Store.
type Service struct {
	store Store
}

// NewService returns a service backed by store.
func NewService(store Store) *Service { return &Service{store: store} }

// Submit validates and stores a new claim for the employee.
func (s *Service) Submit(ctx context.Context, employeeID, idempotencyKey string, n NewClaim) (Claim, bool, error) {
	if err := n.Validate(); err != nil {
		return Claim{}, false, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Claim{}, false, err
	}
	return s.store.Create(ctx, id, employeeID, idempotencyKey, n.Hash(), n)
}

// Get returns the claim if the viewer may see it, ErrNotFound otherwise.
func (s *Service) Get(ctx context.Context, v Viewer, id uuid.UUID) (Claim, error) {
	c, err := s.store.Get(ctx, id)
	if err != nil {
		return Claim{}, err
	}
	if !CanView(v, c) {
		return Claim{}, ErrNotFound
	}
	return c, nil
}

// ListOwn returns the viewer's own claims, newest first.
func (s *Service) ListOwn(ctx context.Context, v Viewer, limit int) ([]Claim, error) {
	return s.store.ListByEmployee(ctx, v.Subject, limit)
}

// ListForReview returns pending claims the reviewer may decide (not their own).
func (s *Service) ListForReview(ctx context.Context, v Viewer, limit int) ([]Claim, error) {
	if !v.IsReviewer {
		return nil, errors.New("claims: viewer is not a reviewer")
	}
	return s.store.ListPending(ctx, v.Subject, limit)
}

// Decide records the reviewer's decision.
func (s *Service) Decide(ctx context.Context, v Viewer, id uuid.UUID, d Decision, correlationID string) (Claim, bool, error) {
	if !v.IsReviewer {
		return Claim{}, false, errors.New("claims: viewer is not a reviewer")
	}
	if !d.Valid() {
		return Claim{}, false, &ValidationError{"decision", `must be "approved" or "rejected"`}
	}
	return s.store.Decide(ctx, id, v.Subject, d, correlationID)
}
