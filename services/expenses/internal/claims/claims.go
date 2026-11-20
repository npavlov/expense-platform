// Package claims holds the rules of expense claims: what a valid claim is,
// who may see it, and how a reviewer decides it. Storage and HTTP live
// elsewhere; this package has no I/O so every rule is unit-testable.
package claims

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Currency is fixed for now; it is still stored in events and responses so
// adding currencies later does not change the contract shape.
const Currency = "EUR"

// Limits shared by the API and the database constraints (migration 002).
const (
	MaxAmountCents      = 10_000_000 // 100,000.00 EUR
	MaxDescriptionRunes = 500
	MaxListLimit        = 100
	DefaultListLimit    = 50
)

// Status of a claim. Allowed transitions: pending → approved, pending → rejected.
type Status string

const (
	StatusPending  Status = "pending"
	StatusApproved Status = "approved"
	StatusRejected Status = "rejected"
)

// Decision a reviewer can take.
type Decision string

const (
	DecisionApproved Decision = "approved"
	DecisionRejected Decision = "rejected"
)

// Valid reports whether d is a known decision.
func (d Decision) Valid() bool { return d == DecisionApproved || d == DecisionRejected }

// Claim is a stored expense claim.
type Claim struct {
	ID          uuid.UUID
	EmployeeID  string
	AmountCents int64
	Description string
	Status      Status
	CreatedAt   time.Time
	DecidedBy   *string
	DecidedAt   *time.Time
}

// Errors returned by the rules; the HTTP layer maps them to status codes.
var (
	ErrNotFound             = errors.New("claim not found")
	ErrSelfDecision         = errors.New("reviewers cannot decide their own claims")
	ErrAlreadyDecided       = errors.New("claim already decided")
	ErrIdempotencyKeyReused = errors.New("idempotency key reused with a different request")
)

// ValidationError describes one invalid input field.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Message }

// NewClaim is a submission request.
type NewClaim struct {
	AmountCents int64
	Description string
}

// Validate checks the submission. Descriptions are user text that is later
// shown to reviewers, so control characters are rejected outright.
func (n NewClaim) Validate() error {
	if n.AmountCents <= 0 {
		return &ValidationError{"amount_cents", "must be a positive number of euro cents"}
	}
	if n.AmountCents > MaxAmountCents {
		return &ValidationError{"amount_cents", fmt.Sprintf("must not exceed %d", MaxAmountCents)}
	}
	if !utf8.ValidString(n.Description) {
		return &ValidationError{"description", "must be valid UTF-8"}
	}
	if strings.TrimSpace(n.Description) == "" {
		return &ValidationError{"description", "must not be blank"}
	}
	if utf8.RuneCountInString(n.Description) > MaxDescriptionRunes {
		return &ValidationError{"description", fmt.Sprintf("must be at most %d characters", MaxDescriptionRunes)}
	}
	for _, r := range n.Description {
		if unicode.IsControl(r) {
			return &ValidationError{"description", "must not contain control characters"}
		}
	}
	return nil
}

// Hash identifies the request for idempotency. It is computed over the parsed
// values, so formatting differences (key order, whitespace) do not matter.
func (n NewClaim) Hash() [32]byte {
	h := sha256.New()
	// Decimal amount, then NUL (never part of a valid description), then text.
	h.Write(strconv.AppendInt(nil, n.AmountCents, 10))
	h.Write([]byte{0})
	h.Write([]byte(n.Description))
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

var idempotencyKeyFormat = regexp.MustCompile(`^[A-Za-z0-9._:-]{8,128}$`)

// ValidIdempotencyKey reports whether k is acceptable as an Idempotency-Key.
func ValidIdempotencyKey(k string) bool { return idempotencyKeyFormat.MatchString(k) }

// Viewer is the authenticated caller as the rules see it.
type Viewer struct {
	Subject    string
	IsReviewer bool
}

// CanView reports whether v may read c. Employees see only their own claims;
// reviewers see all claims. Callers answer "not found" otherwise, so the
// existence of someone else's claim is not revealed.
func CanView(v Viewer, c Claim) bool {
	return c.EmployeeID == v.Subject || v.IsReviewer
}

// CheckDecision applies the decision rules to the current state of a claim,
// read under a row lock. It returns replay=true when the same reviewer repeats
// the same decision (a retried request), which succeeds without a new event.
func CheckDecision(c Claim, reviewer string, d Decision) (replay bool, err error) {
	if c.EmployeeID == reviewer {
		return false, ErrSelfDecision
	}
	if c.Status == StatusPending {
		return false, nil
	}
	if c.DecidedBy != nil && *c.DecidedBy == reviewer && string(c.Status) == string(d) {
		return true, nil
	}
	return false, ErrAlreadyDecided
}

// EventTypeExpenseDecided is the outbox event type for decisions.
const EventTypeExpenseDecided = "ExpenseDecided"

// ExpenseDecidedV1 is the event contract (contracts/events/expense-decided.v1.schema.json).
type ExpenseDecidedV1 struct {
	EventID       uuid.UUID          `json:"event_id"`
	EventType     string             `json:"event_type"`
	SchemaVersion int                `json:"schema_version"`
	OccurredAt    time.Time          `json:"occurred_at"`
	CorrelationID string             `json:"correlation_id,omitempty"`
	Data          ExpenseDecidedData `json:"data"`
}

// ExpenseDecidedData is the business content of the event. It carries the
// exact amount that was approved, so the audit trail is bound to the content.
type ExpenseDecidedData struct {
	ClaimID     uuid.UUID `json:"claim_id"`
	EmployeeID  string    `json:"employee_id"`
	AmountCents int64     `json:"amount_cents"`
	Currency    string    `json:"currency"`
	Decision    Decision  `json:"decision"`
	DecidedBy   string    `json:"decided_by"`
	DecidedAt   time.Time `json:"decided_at"`
}

// NewExpenseDecided builds the event for a freshly decided claim.
func NewExpenseDecided(c Claim, correlationID string) (ExpenseDecidedV1, error) {
	if c.DecidedBy == nil || c.DecidedAt == nil || c.Status == StatusPending {
		return ExpenseDecidedV1{}, errors.New("claims: claim is not decided")
	}
	id, err := uuid.NewV7()
	if err != nil {
		return ExpenseDecidedV1{}, err
	}
	return ExpenseDecidedV1{
		EventID:       id,
		EventType:     EventTypeExpenseDecided,
		SchemaVersion: 1,
		OccurredAt:    c.DecidedAt.UTC(),
		CorrelationID: correlationID,
		Data: ExpenseDecidedData{
			ClaimID:     c.ID,
			EmployeeID:  c.EmployeeID,
			AmountCents: c.AmountCents,
			Currency:    Currency,
			Decision:    Decision(c.Status),
			DecidedBy:   *c.DecidedBy,
			DecidedAt:   c.DecidedAt.UTC(),
		},
	}, nil
}
