package claims

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const schemaPath = "../../../../contracts/events/expense-decided.v1.schema.json"

// TestEventMatchesContract validates what the producer actually emits against
// the published schema, so a change in the Go struct that breaks consumers
// fails here before it reaches the audit service.
func TestEventMatchesContract(t *testing.T) {
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	schema, err := c.Compile(schemaPath)
	if err != nil {
		t.Fatalf("compile schema: %v", err)
	}

	rita := "4174a000-0000-4000-8000-000000000003"
	now := time.Now()
	for _, status := range []Status{StatusApproved, StatusRejected} {
		claim := Claim{ID: uuid.New(), EmployeeID: "a11ce000-0000-4000-8000-000000000001", AmountCents: MaxAmountCents,
			Description: "never part of the event", Status: status, CreatedAt: now, DecidedBy: &rita, DecidedAt: &now}
		ev, err := NewExpenseDecided(claim, "0f6c1d0e-5b7a-4c2e-9a1f-1a2b3c4d5e6f")
		if err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(inst); err != nil {
			t.Fatalf("%s event violates the contract: %v\n%s", status, err, b)
		}
		// Data minimization: the free-text description is not shared.
		if bytes.Contains(b, []byte("never part of the event")) {
			t.Fatal("description leaked into the event")
		}
	}
}
