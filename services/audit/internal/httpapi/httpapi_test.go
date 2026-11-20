package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/npavlov/expense-platform/libs/platform/authn"
	"github.com/npavlov/expense-platform/libs/platform/sigauth"
	"github.com/npavlov/expense-platform/services/audit/internal/events"
	"github.com/npavlov/expense-platform/services/audit/internal/storage"
)

type fakeVerifier map[string]authn.Principal

func (f fakeVerifier) Verify(_ context.Context, raw string) (authn.Principal, error) {
	if p, ok := f[raw]; ok {
		return p, nil
	}
	return authn.Principal{}, authn.ErrInvalidToken
}

type memStore struct {
	mu   sync.Mutex
	rows map[uuid.UUID]events.Decision
}

func (m *memStore) Record(_ context.Context, d events.Decision) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if prev, ok := m.rows[d.EventID]; ok {
		if prev.ContentHash() != d.ContentHash() {
			return false, storage.ErrConflict
		}
		return false, nil
	}
	m.rows[d.EventID] = d
	return true, nil
}

func (m *memStore) ByClaim(_ context.Context, id uuid.UUID) ([]storage.Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []storage.Entry{}
	for _, d := range m.rows {
		if d.ClaimID == id {
			out = append(out, storage.Entry{EventID: d.EventID, ClaimID: d.ClaimID, Decision: d.Decision, DecidedBy: d.DecidedBy})
		}
	}
	return out, nil
}

var key = []byte(strings.Repeat("s", 32))

func setup(t *testing.T) (public, internal http.Handler, store *memStore) {
	t.Helper()
	store = &memStore{rows: map[uuid.UUID]events.Decision{}}
	d := Deps{
		Store: store,
		Verifier: fakeVerifier{
			"rita":  {Subject: "sub-rita", Groups: []string{"employees", "reviewers"}},
			"alice": {Subject: "sub-alice", Groups: []string{"employees"}},
		},
		Signature: sigauth.Verifier{Keys: map[string][]byte{"k1": key}, MaxSkew: time.Minute},
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		Metrics:   NewMetrics(prometheus.NewRegistry()),
	}
	return NewPublicRouter(d), NewInternalRouter(d), store
}

func example(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../../../../contracts/events/examples/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func post(h http.Handler, body []byte, header map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/events", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func signed(body []byte) map[string]string {
	return map[string]string{sigauth.Header: sigauth.Signer{KeyID: "k1", Key: key}.Sign(body)}
}

func TestIngest(t *testing.T) {
	_, internal, store := setup(t)
	body := example(t, "expense-decided.v1.json")

	if rec := post(internal, body, signed(body)); rec.Code != http.StatusCreated {
		t.Fatalf("first delivery: %d %s", rec.Code, rec.Body)
	}
	// Lost acknowledgement: the producer sends the same event again.
	if rec := post(internal, body, signed(body)); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "duplicate") {
		t.Fatalf("redelivery: %d %s", rec.Code, rec.Body)
	}
	if len(store.rows) != 1 {
		t.Fatalf("stored %d rows, want 1", len(store.rows))
	}

	// Same event ID, different content: refused.
	var m map[string]any
	_ = json.Unmarshal(body, &m)
	m["data"].(map[string]any)["amount_cents"] = 999999
	tampered, _ := json.Marshal(m)
	if rec := post(internal, tampered, signed(tampered)); rec.Code != http.StatusConflict {
		t.Fatalf("conflicting redelivery: %d %s", rec.Code, rec.Body)
	}

	v2 := example(t, "expense-decided.v2.json")
	rec := post(internal, v2, signed(v2))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "unsupported_schema_version") {
		t.Fatalf("v2 event: %d %s", rec.Code, rec.Body)
	}
}

func TestIngestRequiresValidSignature(t *testing.T) {
	_, internal, store := setup(t)
	body := example(t, "expense-decided.v1.json")
	other := []byte(strings.Repeat("o", 32))
	cases := map[string]map[string]string{
		"no signature":            nil,
		"wrong key":               {sigauth.Header: sigauth.Signer{KeyID: "k1", Key: other}.Sign(body)},
		"signature of other body": {sigauth.Header: sigauth.Signer{KeyID: "k1", Key: key}.Sign([]byte("{}"))},
		"user token instead":      {"Authorization": "Bearer rita"},
		"stale timestamp":         {sigauth.Header: sigauth.Signer{KeyID: "k1", Key: key, Now: func() time.Time { return time.Now().Add(-time.Hour) }}.Sign(body)},
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			if rec := post(internal, body, h); rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
		})
	}
	if len(store.rows) != 0 {
		t.Fatal("unauthenticated event was stored")
	}
}

func TestHistoryAccess(t *testing.T) {
	public, internal, _ := setup(t)
	body := example(t, "expense-decided.v1.json")
	post(internal, body, signed(body))
	path := "/v1/audit/claims/019a9f3a-1111-7aaa-8bbb-123456789abc/decisions"

	get := func(h http.Handler, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := get(public, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous: %d", rec.Code)
	}
	if rec := get(public, "alice"); rec.Code != http.StatusForbidden {
		t.Errorf("employee: %d", rec.Code)
	}
	rec := get(public, "rita")
	if rec.Code != http.StatusOK {
		t.Fatalf("reviewer: %d", rec.Code)
	}
	var resp struct {
		Decisions []storage.Entry `json:"decisions"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Decisions) != 1 || resp.Decisions[0].Decision != "approved" {
		t.Fatalf("history = %+v", resp)
	}

	// Each listener only serves its own routes.
	if rec := get(internal, "rita"); rec.Code != http.StatusNotFound {
		t.Errorf("history on internal listener: %d", rec.Code)
	}
	if rec := post(public, body, signed(body)); rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("ingest on public listener: %d", rec.Code)
	}
}
