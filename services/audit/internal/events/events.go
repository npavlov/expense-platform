// Package events parses and validates events received by the audit service.
//
// Compatibility rules for ExpenseDecided (contracts/events/README.md):
//   - schema_version is the major version; only 1 is understood here.
//     Any other value is rejected permanently, so the producer parks the event
//     instead of retrying it forever.
//   - Unknown fields are ignored, so the producer can add optional fields
//     without a coordinated release.
//   - Removing or retyping a field requires schema_version 2.
package events

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"
)

// TypeExpenseDecided is the only event type the audit service accepts.
const TypeExpenseDecided = "ExpenseDecided"

// SupportedSchemaVersion is the major version this build understands.
const SupportedSchemaVersion = 1

// ErrUnsupportedVersion is returned for an unknown event type or major version.
var ErrUnsupportedVersion = errors.New("unsupported event type or schema version")

// InvalidError describes a malformed event.
type InvalidError struct{ Reason string }

func (e *InvalidError) Error() string { return "invalid event: " + e.Reason }

// Decision is a validated ExpenseDecided event.
type Decision struct {
	EventID       uuid.UUID
	SchemaVersion int
	OccurredAt    time.Time
	CorrelationID string
	ClaimID       uuid.UUID
	EmployeeID    string
	AmountCents   int64
	Currency      string
	Decision      string
	DecidedBy     string
	DecidedAt     time.Time
}

type envelope struct {
	EventID       *uuid.UUID      `json:"event_id"`
	EventType     string          `json:"event_type"`
	SchemaVersion *int            `json:"schema_version"`
	OccurredAt    *time.Time      `json:"occurred_at"`
	CorrelationID string          `json:"correlation_id"`
	Data          json.RawMessage `json:"data"`
}

type decidedData struct {
	ClaimID     *uuid.UUID `json:"claim_id"`
	EmployeeID  string     `json:"employee_id"`
	AmountCents *int64     `json:"amount_cents"`
	Currency    string     `json:"currency"`
	Decision    string     `json:"decision"`
	DecidedBy   string     `json:"decided_by"`
	DecidedAt   *time.Time `json:"decided_at"`
}

var currencyFormat = regexp.MustCompile(`^[A-Z]{3}$`)

// Parse decodes and validates an ExpenseDecided event.
func Parse(body []byte) (Decision, error) {
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return Decision{}, &InvalidError{"not a JSON object"}
	}
	if env.EventType != TypeExpenseDecided || env.SchemaVersion == nil || *env.SchemaVersion != SupportedSchemaVersion {
		return Decision{}, ErrUnsupportedVersion
	}
	if env.EventID == nil || *env.EventID == uuid.Nil {
		return Decision{}, &InvalidError{"event_id is required"}
	}
	if env.OccurredAt == nil {
		return Decision{}, &InvalidError{"occurred_at is required"}
	}
	if len(env.CorrelationID) > 64 {
		return Decision{}, &InvalidError{"correlation_id is too long"}
	}
	var d decidedData
	if err := json.Unmarshal(env.Data, &d); err != nil {
		return Decision{}, &InvalidError{"data must be an object"}
	}
	switch {
	case d.ClaimID == nil || *d.ClaimID == uuid.Nil:
		return Decision{}, &InvalidError{"data.claim_id is required"}
	case d.EmployeeID == "" || len(d.EmployeeID) > 128:
		return Decision{}, &InvalidError{"data.employee_id is invalid"}
	case d.DecidedBy == "" || len(d.DecidedBy) > 128:
		return Decision{}, &InvalidError{"data.decided_by is invalid"}
	case d.DecidedBy == d.EmployeeID:
		return Decision{}, &InvalidError{"data.decided_by equals data.employee_id"}
	case d.AmountCents == nil || *d.AmountCents <= 0:
		return Decision{}, &InvalidError{"data.amount_cents must be positive"}
	case !currencyFormat.MatchString(d.Currency):
		return Decision{}, &InvalidError{"data.currency must be an ISO 4217 code"}
	case d.Decision != "approved" && d.Decision != "rejected":
		return Decision{}, &InvalidError{"data.decision must be approved or rejected"}
	case d.DecidedAt == nil:
		return Decision{}, &InvalidError{"data.decided_at is required"}
	}
	return Decision{
		EventID:       *env.EventID,
		SchemaVersion: *env.SchemaVersion,
		OccurredAt:    env.OccurredAt.UTC(),
		CorrelationID: env.CorrelationID,
		ClaimID:       *d.ClaimID,
		EmployeeID:    d.EmployeeID,
		AmountCents:   *d.AmountCents,
		Currency:      d.Currency,
		Decision:      d.Decision,
		DecidedBy:     d.DecidedBy,
		DecidedAt:     d.DecidedAt.UTC(),
	}, nil
}

// ContentHash identifies the business content of the event, independent of
// JSON formatting and of fields this version does not know.
func (d Decision) ContentHash() [32]byte {
	s := fmt.Sprintf("%s|%d|%s|%s|%d|%s|%s|%s|%s",
		d.EventID, d.SchemaVersion, d.ClaimID, d.EmployeeID, d.AmountCents, d.Currency,
		d.Decision, d.DecidedBy, d.DecidedAt.UTC().Format(time.RFC3339Nano))
	return sha256.Sum256([]byte(s))
}
