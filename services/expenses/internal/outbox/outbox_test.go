package outbox

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/npavlov/expense-platform/libs/platform/sigauth"
)

func TestBackoffIsBoundedAndJittered(t *testing.T) {
	base, maxDelay := time.Second, 30*time.Second
	for attempt := 1; attempt <= 100; attempt++ {
		ideal := min(base<<min(attempt-1, 30), maxDelay)
		for range 20 {
			d := Backoff(attempt, base, maxDelay)
			if d < ideal/2 || d > ideal {
				t.Fatalf("attempt %d: %v outside [%v, %v]", attempt, d, ideal/2, ideal)
			}
		}
	}
}

// fakeStore records what the worker decided for each event.
type fakeStore struct {
	mu        sync.Mutex
	due       []Event
	delivered []uuid.UUID
	retried   []uuid.UUID
	parked    map[uuid.UUID]string
}

func (f *fakeStore) Lease(context.Context, int, time.Duration) ([]Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.due
	f.due = nil
	return out, nil
}
func (f *fakeStore) MarkDelivered(_ context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.delivered = append(f.delivered, id)
	return nil
}
func (f *fakeStore) Retry(_ context.Context, id uuid.UUID, _ time.Time, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.retried = append(f.retried, id)
	return nil
}
func (f *fakeStore) Park(_ context.Context, id uuid.UUID, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.parked[id] = reason
	return nil
}
func (f *fakeStore) Stats(context.Context) (Stats, error)              { return Stats{}, nil }
func (f *fakeStore) List(context.Context, bool, int) ([]Status, error) { return nil, nil }
func (f *fakeStore) Requeue(context.Context, uuid.UUID) error          { return nil }

type deliverFunc func(context.Context, Event) error

func (d deliverFunc) Deliver(ctx context.Context, e Event) error { return d(ctx, e) }

func TestWorkerOutcomes(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxAttempts = 3
	cases := []struct {
		name     string
		attempts int
		err      error
		want     string // delivered, retried, parked
	}{
		{"success", 1, nil, "delivered"},
		{"transient", 1, errors.New("timeout"), "retried"},
		{"transient at budget", 3, errors.New("timeout"), "parked"},
		{"permanent", 1, &PermanentError{Reason: "HTTP 422"}, "parked"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStore{parked: map[uuid.UUID]string{}}
			e := Event{ID: uuid.New(), Attempts: tc.attempts, CreatedAt: time.Now()}
			store.due = []Event{e}
			w := NewWorker(store, deliverFunc(func(context.Context, Event) error { return tc.err }), cfg,
				slog.New(slog.NewTextHandler(io.Discard, nil)), NewMetrics(prometheus.NewRegistry()))
			if _, err := w.RunOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			got := map[string]int{"delivered": len(store.delivered), "retried": len(store.retried), "parked": len(store.parked)}
			for k, n := range got {
				want := 0
				if k == tc.want {
					want = 1
				}
				if n != want {
					t.Fatalf("%s = %d, want %d (%+v)", k, n, want, got)
				}
			}
		})
	}
}

func TestHTTPDelivererClassifiesResponses(t *testing.T) {
	key := []byte(strings.Repeat("k", 32))
	cases := []struct {
		status    int
		permanent bool
		ok        bool
	}{
		{201, false, true}, {200, false, true},
		{400, true, false}, {401, true, false}, {409, true, false}, {422, true, false},
		{408, false, false}, {429, false, false}, {500, false, false}, {503, false, false},
		{302, false, false},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if tc.status == 302 {
				http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
				return
			}
			w.WriteHeader(tc.status)
		}))
		d := NewHTTPDeliverer(srv.URL, sigauth.Signer{KeyID: "k1", Key: key})
		err := d.Deliver(context.Background(), Event{ID: uuid.New(), Payload: []byte(`{}`)})
		srv.Close()
		var perm *PermanentError
		if (err == nil) != tc.ok || errors.As(err, &perm) != tc.permanent {
			t.Errorf("HTTP %d: err = %v, want ok=%v permanent=%v", tc.status, err, tc.ok, tc.permanent)
		}
	}
}

func TestHTTPDelivererTimesOut(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-block }))
	defer srv.Close()
	defer close(block)
	d := NewHTTPDeliverer(srv.URL, sigauth.Signer{KeyID: "k1", Key: []byte(strings.Repeat("k", 32))})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := d.Deliver(ctx, Event{Payload: []byte(`{}`)})
	var perm *PermanentError
	if err == nil || errors.As(err, &perm) {
		t.Fatalf("err = %v, want transient timeout", err)
	}
}
