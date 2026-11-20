package claims

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNewClaimValidate(t *testing.T) {
	cases := []struct {
		name  string
		in    NewClaim
		field string // "" means valid
	}{
		{"taxi", NewClaim{1200, "Taxi"}, ""},
		{"max amount", NewClaim{MaxAmountCents, "Laptop"}, ""},
		{"unicode", NewClaim{500, "Такси до аэропорта"}, ""},
		{"zero", NewClaim{0, "Taxi"}, "amount_cents"},
		{"negative", NewClaim{-1, "Taxi"}, "amount_cents"},
		{"over max", NewClaim{MaxAmountCents + 1, "Taxi"}, "amount_cents"},
		{"empty description", NewClaim{1200, ""}, "description"},
		{"blank description", NewClaim{1200, " \t "}, "description"},
		{"newline", NewClaim{1200, "Taxi\nreceipt"}, "description"},
		{"nul byte", NewClaim{1200, "Taxi\x00"}, "description"},
		{"invalid utf8", NewClaim{1200, "\xff"}, "description"},
		{"max length", NewClaim{1200, strings.Repeat("я", MaxDescriptionRunes)}, ""},
		{"too long", NewClaim{1200, strings.Repeat("a", MaxDescriptionRunes+1)}, "description"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.in.Validate()
			if tc.field == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			var ve *ValidationError
			if !errors.As(err, &ve) || ve.Field != tc.field {
				t.Fatalf("err = %v, want validation error on %s", err, tc.field)
			}
		})
	}
}

func TestHashDependsOnContent(t *testing.T) {
	a := NewClaim{1200, "Taxi"}.Hash()
	if a != (NewClaim{1200, "Taxi"}).Hash() {
		t.Fatal("same request, different hash")
	}
	if a == (NewClaim{1201, "Taxi"}).Hash() || a == (NewClaim{1200, "Taxi "}).Hash() {
		t.Fatal("different request, same hash")
	}
}

func TestValidIdempotencyKey(t *testing.T) {
	for _, k := range []string{uuid.NewString(), "order-2026-11-20:1", strings.Repeat("k", 128)} {
		if !ValidIdempotencyKey(k) {
			t.Errorf("%q rejected", k)
		}
	}
	for _, k := range []string{"", "short", "has space here", "semi;colon-key", strings.Repeat("k", 129)} {
		if ValidIdempotencyKey(k) {
			t.Errorf("%q accepted", k)
		}
	}
}

func TestCanView(t *testing.T) {
	c := Claim{EmployeeID: "alice"}
	if !CanView(Viewer{Subject: "alice"}, c) {
		t.Error("owner cannot view own claim")
	}
	if CanView(Viewer{Subject: "bob"}, c) {
		t.Error("another employee can view the claim")
	}
	if !CanView(Viewer{Subject: "rita", IsReviewer: true}, c) {
		t.Error("reviewer cannot view the claim")
	}
}

func TestCheckDecision(t *testing.T) {
	rita := "rita"
	now := time.Now()
	pending := Claim{EmployeeID: "alice", Status: StatusPending}
	approvedByRita := Claim{EmployeeID: "alice", Status: StatusApproved, DecidedBy: &rita, DecidedAt: &now}

	cases := []struct {
		name       string
		c          Claim
		reviewer   string
		d          Decision
		wantReplay bool
		wantErr    error
	}{
		{"first decision", pending, "rita", DecisionApproved, false, nil},
		{"self approval", Claim{EmployeeID: "rita", Status: StatusPending}, "rita", DecisionApproved, false, ErrSelfDecision},
		{"self rejection", Claim{EmployeeID: "rita", Status: StatusPending}, "rita", DecisionRejected, false, ErrSelfDecision},
		{"same reviewer retries", approvedByRita, "rita", DecisionApproved, true, nil},
		{"same reviewer flips", approvedByRita, "rita", DecisionRejected, false, ErrAlreadyDecided},
		{"second reviewer agrees", approvedByRita, "victor", DecisionApproved, false, ErrAlreadyDecided},
		{"second reviewer disagrees", approvedByRita, "victor", DecisionRejected, false, ErrAlreadyDecided},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			replay, err := CheckDecision(tc.c, tc.reviewer, tc.d)
			if !errors.Is(err, tc.wantErr) || replay != tc.wantReplay {
				t.Fatalf("got (%v, %v), want (%v, %v)", replay, err, tc.wantReplay, tc.wantErr)
			}
		})
	}
}

func TestNewExpenseDecided(t *testing.T) {
	rita := "rita"
	at := time.Date(2026, 11, 20, 10, 0, 0, 0, time.FixedZone("CET", 3600))
	c := Claim{ID: uuid.New(), EmployeeID: "alice", AmountCents: 1200, Status: StatusApproved, DecidedBy: &rita, DecidedAt: &at}

	e, err := NewExpenseDecided(c, "req-1")
	if err != nil {
		t.Fatal(err)
	}
	if e.EventType != EventTypeExpenseDecided || e.SchemaVersion != 1 || e.EventID.Version() != 7 {
		t.Fatalf("bad envelope %+v", e)
	}
	if e.Data.AmountCents != 1200 || e.Data.Decision != DecisionApproved || e.Data.DecidedBy != "rita" || e.Data.Currency != "EUR" {
		t.Fatalf("bad data %+v", e.Data)
	}
	if e.OccurredAt.Location() != time.UTC {
		t.Error("times must be UTC")
	}

	if _, err := NewExpenseDecided(Claim{Status: StatusPending}, ""); err == nil {
		t.Error("event built for a pending claim")
	}
}
