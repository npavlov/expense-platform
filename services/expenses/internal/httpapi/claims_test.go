package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/npavlov/expense-platform/libs/platform/authn"
	"github.com/npavlov/expense-platform/libs/platform/httpx"
	"github.com/npavlov/expense-platform/services/expenses/internal/claims"
)

// fakeVerifier maps opaque test tokens to principals.
type fakeVerifier map[string]authn.Principal

func (f fakeVerifier) Verify(_ context.Context, raw string) (authn.Principal, error) {
	p, ok := f[raw]
	if !ok {
		return authn.Principal{}, authn.ErrInvalidToken
	}
	return p, nil
}

var users = fakeVerifier{
	"alice":  {Subject: "sub-alice", Groups: []string{GroupEmployees}},
	"bob":    {Subject: "sub-bob", Groups: []string{GroupEmployees}},
	"rita":   {Subject: "sub-rita", Groups: []string{GroupEmployees, GroupReviewers}},
	"victor": {Subject: "sub-victor", Groups: []string{GroupReviewers}},
	"nobody": {Subject: "sub-nobody", Groups: nil},
}

// memStore is an in-memory claims.Store with the same rules as PostgreSQL;
// the SQL implementation is covered by integration tests.
type memStore struct {
	mu     sync.Mutex
	claims map[uuid.UUID]claims.Claim
	keys   map[string]struct {
		hash [32]byte
		id   uuid.UUID
	}
	events int
}

func newMemStore() *memStore {
	return &memStore{claims: map[uuid.UUID]claims.Claim{}, keys: map[string]struct {
		hash [32]byte
		id   uuid.UUID
	}{}}
}

func (m *memStore) Create(_ context.Context, id uuid.UUID, emp, key string, hash [32]byte, n claims.NewClaim) (claims.Claim, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := emp + "\x00" + key
	if prev, ok := m.keys[k]; ok {
		if prev.hash != hash {
			return claims.Claim{}, false, claims.ErrIdempotencyKeyReused
		}
		return m.claims[prev.id], true, nil
	}
	c := claims.Claim{ID: id, EmployeeID: emp, AmountCents: n.AmountCents, Description: n.Description, Status: claims.StatusPending, CreatedAt: time.Now()}
	m.claims[id] = c
	m.keys[k] = struct {
		hash [32]byte
		id   uuid.UUID
	}{hash, id}
	return c, false, nil
}

func (m *memStore) Get(_ context.Context, id uuid.UUID) (claims.Claim, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.claims[id]
	if !ok {
		return claims.Claim{}, claims.ErrNotFound
	}
	return c, nil
}

func (m *memStore) ListByEmployee(_ context.Context, emp string, limit int) ([]claims.Claim, error) {
	return m.filter(func(c claims.Claim) bool { return c.EmployeeID == emp }, limit), nil
}

func (m *memStore) ListPending(_ context.Context, exclude string, limit int) ([]claims.Claim, error) {
	return m.filter(func(c claims.Claim) bool { return c.Status == claims.StatusPending && c.EmployeeID != exclude }, limit), nil
}

func (m *memStore) filter(keep func(claims.Claim) bool, limit int) []claims.Claim {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []claims.Claim{}
	for _, c := range m.claims {
		if keep(c) && len(out) < limit {
			out = append(out, c)
		}
	}
	return out
}

func (m *memStore) Decide(_ context.Context, id uuid.UUID, reviewer string, d claims.Decision, _ string) (claims.Claim, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.claims[id]
	if !ok {
		return claims.Claim{}, false, claims.ErrNotFound
	}
	replay, err := claims.CheckDecision(c, reviewer, d)
	if err != nil || replay {
		return c, replay, err
	}
	now := time.Now()
	c.Status, c.DecidedBy, c.DecidedAt = claims.Status(d), &reviewer, &now
	m.claims[id] = c
	m.events++
	return c, false, nil
}

type testAPI struct {
	t     *testing.T
	h     http.Handler
	store *memStore
}

func newTestAPI(t *testing.T) *testAPI {
	store := newMemStore()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mux := NewRouter(Deps{Claims: claims.NewService(store), Verifier: users, Log: log})
	return &testAPI{t: t, h: httpx.Wrap(mux, log, nil), store: store}
}

type response struct {
	code   int
	header http.Header
	body   map[string]any
}

func (a *testAPI) do(method, path, user string, body string, headers ...string) response {
	a.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if user != "" {
		req.Header.Set("Authorization", "Bearer "+user)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, req)
	out := response{code: rec.Code, header: rec.Header()}
	if b := rec.Body.Bytes(); len(bytes.TrimSpace(b)) > 0 && strings.Contains(rec.Header().Get("Content-Type"), "json") {
		if err := json.Unmarshal(b, &out.body); err != nil {
			a.t.Fatalf("decode %s %s: %v (%s)", method, path, err, b)
		}
	}
	return out
}

func (a *testAPI) submit(user, key string, amount int, desc string) response {
	a.t.Helper()
	b, _ := json.Marshal(map[string]any{"amount_cents": amount, "description": desc})
	return a.do(http.MethodPost, "/v1/claims", user, string(b), "Idempotency-Key", key)
}

func (a *testAPI) mustSubmit(user string) string {
	a.t.Helper()
	r := a.submit(user, uuid.NewString(), 1200, "Taxi")
	if r.code != http.StatusCreated {
		a.t.Fatalf("submit as %s: %d %v", user, r.code, r.body)
	}
	return r.body["id"].(string)
}

func TestSubmitAndRead(t *testing.T) {
	a := newTestAPI(t)
	r := a.submit("alice", "key-00000001", 1200, "Taxi")
	if r.code != http.StatusCreated {
		t.Fatalf("status = %d %v", r.code, r.body)
	}
	id := r.body["id"].(string)
	if r.body["status"] != "pending" || r.body["amount_cents"] != 1200.0 || r.body["currency"] != "EUR" || r.body["employee_id"] != "sub-alice" {
		t.Fatalf("unexpected body %v", r.body)
	}
	if r.header.Get("Location") != "/v1/claims/"+id {
		t.Errorf("Location = %q", r.header.Get("Location"))
	}
	// The response exposes exactly the documented fields.
	want := []string{"amount_cents", "created_at", "currency", "decided_at", "decided_by", "description", "employee_id", "id", "status"}
	if got := slices.Sorted(maps.Keys(r.body)); !slices.Equal(got, want) {
		t.Errorf("fields = %v, want %v", got, want)
	}

	got := a.do(http.MethodGet, "/v1/claims/"+id, "alice", "")
	if got.code != http.StatusOK || got.body["id"] != id {
		t.Fatalf("owner read: %d %v", got.code, got.body)
	}
	list := a.do(http.MethodGet, "/v1/claims", "alice", "")
	if items := list.body["items"].([]any); len(items) != 1 {
		t.Fatalf("own list has %d items", len(items))
	}
}

func TestIdempotentSubmit(t *testing.T) {
	a := newTestAPI(t)
	first := a.submit("alice", "retry-key-1", 1200, "Taxi")
	again := a.submit("alice", "retry-key-1", 1200, "Taxi")
	if again.code != http.StatusCreated || again.body["id"] != first.body["id"] || again.header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay: %d %v %v", again.code, again.body, again.header)
	}
	changed := a.submit("alice", "retry-key-1", 9900, "Taxi")
	if changed.code != http.StatusUnprocessableEntity || changed.body["code"] != "idempotency_key_reused" {
		t.Fatalf("reused key with different body: %d %v", changed.code, changed.body)
	}
	// The key is scoped to the employee: Bob's identical key is a new claim.
	bob := a.submit("bob", "retry-key-1", 1200, "Taxi")
	if bob.code != http.StatusCreated || bob.body["id"] == first.body["id"] {
		t.Fatalf("key leaked across employees: %d %v", bob.code, bob.body)
	}
	if n := len(a.store.claims); n != 2 {
		t.Fatalf("stored %d claims, want 2", n)
	}
}

func TestSubmitInputBoundaries(t *testing.T) {
	a := newTestAPI(t)
	key := func() []string { return []string{"Idempotency-Key", uuid.NewString()} }
	cases := []struct {
		name    string
		body    string
		headers []string
		want    int
		code    string
	}{
		{"zero amount", `{"amount_cents":0,"description":"Taxi"}`, key(), 422, "validation_failed"},
		{"negative amount", `{"amount_cents":-5,"description":"Taxi"}`, key(), 422, "validation_failed"},
		{"too large amount", `{"amount_cents":10000001,"description":"Taxi"}`, key(), 422, "validation_failed"},
		{"fractional amount", `{"amount_cents":12.5,"description":"Taxi"}`, key(), 400, "malformed_body"},
		{"amount as string", `{"amount_cents":"1200","description":"Taxi"}`, key(), 400, "malformed_body"},
		{"int64 overflow", `{"amount_cents":99999999999999999999,"description":"Taxi"}`, key(), 400, "malformed_body"},
		{"missing description", `{"amount_cents":1200}`, key(), 422, "validation_failed"},
		{"blank description", `{"amount_cents":1200,"description":"   "}`, key(), 422, "validation_failed"},
		{"long description", `{"amount_cents":1200,"description":"` + strings.Repeat("x", 501) + `"}`, key(), 422, "validation_failed"},
		{"mass assignment: status", `{"amount_cents":1200,"description":"Taxi","status":"approved"}`, key(), 400, "malformed_body"},
		{"mass assignment: employee", `{"amount_cents":1200,"description":"Taxi","employee_id":"sub-bob"}`, key(), 400, "malformed_body"},
		{"array body", `[{"amount_cents":1200,"description":"Taxi"}]`, key(), 400, "malformed_body"},
		{"body too large", `{"amount_cents":1200,"description":"` + strings.Repeat("x", 5000) + `"}`, key(), 413, "body_too_large"},
		{"missing key", `{"amount_cents":1200,"description":"Taxi"}`, nil, 400, "idempotency_key_required"},
		{"short key", `{"amount_cents":1200,"description":"Taxi"}`, []string{"Idempotency-Key", "abc"}, 400, "idempotency_key_invalid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := a.do(http.MethodPost, "/v1/claims", "alice", tc.body, tc.headers...)
			if r.code != tc.want || r.body["code"] != tc.code {
				t.Fatalf("got %d %v, want %d %s", r.code, r.body["code"], tc.want, tc.code)
			}
		})
	}
	// Wrong content type.
	req := httptest.NewRequest(http.MethodPost, "/v1/claims", strings.NewReader("amount_cents=1"))
	req.Header.Set("Authorization", "Bearer alice")
	req.Header.Set("Idempotency-Key", uuid.NewString())
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("form body: %d", rec.Code)
	}
	if len(a.store.claims) != 0 {
		t.Fatalf("invalid requests stored %d claims", len(a.store.claims))
	}
}

// TestObjectLevelAccess is the tenant-isolation test: Bob holds a valid token
// and knows Alice's claim ID, and still cannot see or decide it.
func TestObjectLevelAccess(t *testing.T) {
	a := newTestAPI(t)
	id := a.mustSubmit("alice")

	if r := a.do(http.MethodGet, "/v1/claims/"+id, "bob", ""); r.code != http.StatusNotFound {
		t.Errorf("bob reads alice's claim: %d", r.code)
	}
	list := a.do(http.MethodGet, "/v1/claims", "bob", "")
	if items := list.body["items"].([]any); len(items) != 0 {
		t.Errorf("bob's list contains %d foreign claims", len(items))
	}
	if r := a.do(http.MethodPost, "/v1/claims/"+id+"/decision", "bob", `{"decision":"approved"}`); r.code != http.StatusForbidden {
		t.Errorf("bob decides alice's claim: %d", r.code)
	}
	if r := a.do(http.MethodGet, "/v1/claims/"+id, "rita", ""); r.code != http.StatusOK {
		t.Errorf("reviewer cannot read the claim: %d", r.code)
	}
	if r := a.do(http.MethodGet, "/v1/claims/not-a-uuid", "alice", ""); r.code != http.StatusNotFound {
		t.Errorf("malformed id: %d", r.code)
	}
}

func TestDecisionRules(t *testing.T) {
	a := newTestAPI(t)
	id := a.mustSubmit("alice")
	ritasOwn := a.mustSubmit("rita")
	decide := func(user, claim, decision string) response {
		return a.do(http.MethodPost, "/v1/claims/"+claim+"/decision", user, `{"decision":"`+decision+`"}`)
	}

	if r := decide("rita", ritasOwn, "approved"); r.code != http.StatusForbidden || r.body["code"] != "self_decision_forbidden" {
		t.Fatalf("self approval: %d %v", r.code, r.body)
	}
	if r := decide("rita", id, "maybe"); r.code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid decision: %d", r.code)
	}
	r := decide("rita", id, "approved")
	if r.code != http.StatusOK || r.body["status"] != "approved" || r.body["decided_by"] != "sub-rita" {
		t.Fatalf("approve: %d %v", r.code, r.body)
	}
	if r := decide("rita", id, "approved"); r.code != http.StatusOK || r.header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("retry of same decision: %d %v", r.code, r.header)
	}
	if r := decide("victor", id, "rejected"); r.code != http.StatusConflict || r.body["code"] != "already_decided" {
		t.Fatalf("second decision: %d %v", r.code, r.body)
	}
	if r := decide("rita", uuid.NewString(), "approved"); r.code != http.StatusNotFound {
		t.Fatalf("unknown claim: %d", r.code)
	}
	if a.store.events != 1 {
		t.Fatalf("decision events = %d, want 1", a.store.events)
	}
	if got := a.do(http.MethodGet, "/v1/claims/"+id, "alice", ""); got.body["status"] != "approved" {
		t.Fatalf("employee sees status %v", got.body["status"])
	}
}

func TestReviewQueue(t *testing.T) {
	a := newTestAPI(t)
	a.mustSubmit("alice")
	a.mustSubmit("rita")
	r := a.do(http.MethodGet, "/v1/review/claims", "rita", "")
	items := r.body["items"].([]any)
	if r.code != http.StatusOK || len(items) != 1 || items[0].(map[string]any)["employee_id"] != "sub-alice" {
		t.Fatalf("review queue: %d %v", r.code, r.body)
	}
	if r := a.do(http.MethodGet, "/v1/review/claims?limit=101", "rita", ""); r.code != http.StatusBadRequest {
		t.Errorf("oversized page: %d", r.code)
	}
	if r := a.do(http.MethodGet, "/v1/review/claims?limit=0", "rita", ""); r.code != http.StatusBadRequest {
		t.Errorf("zero page: %d", r.code)
	}
}

// TestAuthorizationMatrix checks every route against every kind of caller.
func TestAuthorizationMatrix(t *testing.T) {
	a := newTestAPI(t)
	id := a.mustSubmit("alice")
	submit := `{"amount_cents":100,"description":"x"}`
	type route struct{ method, path, body string }
	routes := map[string]route{
		"submit":  {http.MethodPost, "/v1/claims", submit},
		"list":    {http.MethodGet, "/v1/claims", ""},
		"get":     {http.MethodGet, "/v1/claims/" + id, ""},
		"queue":   {http.MethodGet, "/v1/review/claims", ""},
		"decide":  {http.MethodPost, "/v1/claims/" + id + "/decision", `{"decision":"rejected"}`},
		"unknown": {http.MethodGet, "/v1/admin", ""},
	}
	// Expected status per caller; "invalid" is a token that fails verification.
	expect := map[string]map[string]int{
		"":        {"submit": 401, "list": 401, "get": 401, "queue": 401, "decide": 401, "unknown": 404},
		"invalid": {"submit": 401, "list": 401, "get": 401, "queue": 401, "decide": 401, "unknown": 404},
		"nobody":  {"submit": 403, "list": 403, "get": 403, "queue": 403, "decide": 403, "unknown": 404},
		"bob":     {"submit": 201, "list": 200, "get": 404, "queue": 403, "decide": 403, "unknown": 404},
		"alice":   {"submit": 201, "list": 200, "get": 200, "queue": 403, "decide": 403, "unknown": 404},
		"victor":  {"submit": 403, "list": 403, "get": 200, "queue": 200, "decide": 200, "unknown": 404},
	}
	for caller, byRoute := range expect {
		for name, want := range byRoute {
			rt := routes[name]
			t.Run(caller+"/"+name, func(t *testing.T) {
				r := a.do(rt.method, rt.path, caller, rt.body, "Idempotency-Key", uuid.NewString())
				if r.code != want {
					t.Fatalf("%s %s as %q: %d, want %d", rt.method, rt.path, caller, r.code, want)
				}
			})
		}
	}
}

func TestReady(t *testing.T) {
	down := NewRouter(Deps{Ready: func(context.Context) error { return errors.New("db down") }, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	rec := httptest.NewRecorder()
	down.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("ready with db down: %d", rec.Code)
	}
}
