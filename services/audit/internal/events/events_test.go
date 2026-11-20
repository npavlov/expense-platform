package events

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

const examples = "../../../../contracts/events/examples"

func readExample(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(examples, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestContractExamples pins the consumer to the published contract.
func TestContractExamples(t *testing.T) {
	d, err := Parse(readExample(t, "expense-decided.v1.json"))
	if err != nil {
		t.Fatalf("v1 example rejected: %v", err)
	}
	if d.AmountCents != 1200 || d.Decision != "approved" || d.Currency != "EUR" || d.CorrelationID == "" {
		t.Fatalf("v1 example misread: %+v", d)
	}

	// Additive change: unknown fields are ignored, the event is accepted.
	if _, err := Parse(readExample(t, "expense-decided.v1-additive.json")); err != nil {
		t.Fatalf("additive v1 example rejected: %v", err)
	}

	// Breaking change: an unknown major version is refused, not misread.
	if _, err := Parse(readExample(t, "expense-decided.v2.json")); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("v2 example: err = %v, want ErrUnsupportedVersion", err)
	}
}

func TestParseRejectsInvalid(t *testing.T) {
	base := map[string]any{}
	if err := json.Unmarshal(readExample(t, "expense-decided.v1.json"), &base); err != nil {
		t.Fatal(err)
	}
	mutate := func(f func(env, data map[string]any)) []byte {
		b, _ := json.Marshal(base)
		var env map[string]any
		_ = json.Unmarshal(b, &env)
		f(env, env["data"].(map[string]any))
		out, _ := json.Marshal(env)
		return out
	}
	cases := map[string][]byte{
		"missing event_id":   mutate(func(e, _ map[string]any) { delete(e, "event_id") }),
		"nil event_id":       mutate(func(e, _ map[string]any) { e["event_id"] = "00000000-0000-0000-0000-000000000000" }),
		"missing occurred":   mutate(func(e, _ map[string]any) { delete(e, "occurred_at") }),
		"zero amount":        mutate(func(_, d map[string]any) { d["amount_cents"] = 0 }),
		"string amount":      mutate(func(_, d map[string]any) { d["amount_cents"] = "1200" }),
		"bad decision":       mutate(func(_, d map[string]any) { d["decision"] = "maybe" }),
		"bad currency":       mutate(func(_, d map[string]any) { d["currency"] = "eur" }),
		"self decision":      mutate(func(_, d map[string]any) { d["decided_by"] = d["employee_id"] }),
		"missing claim":      mutate(func(_, d map[string]any) { delete(d, "claim_id") }),
		"data not an object": mutate(func(e, _ map[string]any) { e["data"] = "x" }),
		"not json":           []byte("{"),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			var inv *InvalidError
			if _, err := Parse(body); !errors.As(err, &inv) {
				t.Fatalf("err = %v, want InvalidError", err)
			}
		})
	}
	wrongType := mutate(func(e, _ map[string]any) { e["event_type"] = "ExpenseCreated" })
	if _, err := Parse(wrongType); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("wrong type: err = %v", err)
	}
}

func TestContentHashIgnoresFormatting(t *testing.T) {
	raw := readExample(t, "expense-decided.v1.json")
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	compact, _ := json.Marshal(m) // different whitespace and key order
	a, err1 := Parse(raw)
	b, err2 := Parse(compact)
	if err1 != nil || err2 != nil || a.ContentHash() != b.ContentHash() {
		t.Fatal("reformatted event has a different content hash")
	}
	b.AmountCents++
	if a.ContentHash() == b.ContentHash() {
		t.Fatal("changed amount, same hash")
	}
}

func FuzzParse(f *testing.F) {
	for _, name := range []string{"expense-decided.v1.json", "expense-decided.v1-additive.json", "expense-decided.v2.json"} {
		b, err := os.ReadFile(filepath.Join(examples, name))
		if err != nil {
			f.Fatal(err)
		}
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		d, err := Parse(body)
		if err != nil {
			return
		}
		// Whatever parses must satisfy the invariants the database relies on.
		if d.AmountCents <= 0 || d.DecidedBy == d.EmployeeID || (d.Decision != "approved" && d.Decision != "rejected") {
			t.Fatalf("invalid event accepted: %+v", d)
		}
	})
}
