//go:build integration

package storage

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/npavlov/expense-platform/libs/platform/sigauth"
	"github.com/npavlov/expense-platform/services/expenses/internal/claims"
	"github.com/npavlov/expense-platform/services/expenses/internal/outbox"
)

// drainOutbox marks every pending event delivered so a test starts with an
// empty queue. It uses the superuser because the app role cannot do bulk edits
// outside its own lease protocol.
func drainOutbox(t *testing.T) {
	t.Helper()
	if _, err := superPool(t).Exec(testCtx(t), `UPDATE expenses.outbox SET delivered_at = now() WHERE delivered_at IS NULL`); err != nil {
		t.Fatal(err)
	}
}

func decided(t *testing.T, s *Claims) claims.Claim {
	t.Helper()
	c := submit(t, s, "emp-"+uuid.NewString())
	d, _, err := s.Decide(testCtx(t), c.ID, "rev-"+uuid.NewString(), claims.DecisionApproved, "req-"+uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestOutboxLeaseLifecycle(t *testing.T) {
	drainOutbox(t)
	pool := appPool(t)
	ctx := testCtx(t)
	s, o := NewClaims(pool), NewOutbox(pool)
	c := decided(t, s)

	leased, err := o.Lease(ctx, 10, 2*time.Second)
	if err != nil || len(leased) != 1 || leased[0].AggregateID != c.ID || leased[0].Attempts != 1 {
		t.Fatalf("lease = %+v, %v", leased, err)
	}
	// While leased, nobody else gets it.
	if again, _ := o.Lease(ctx, 10, time.Second); len(again) != 0 {
		t.Fatalf("leased event handed out twice: %+v", again)
	}
	// A crashed worker never reports back; after the lease expires the event is due again.
	time.Sleep(2100 * time.Millisecond)
	again, err := o.Lease(ctx, 10, time.Minute)
	if err != nil || len(again) != 1 || again[0].ID != leased[0].ID || again[0].Attempts != 2 {
		t.Fatalf("after lease expiry = %+v, %v", again, err)
	}

	// A retry scheduled in the future is not due.
	if err := o.Retry(ctx, again[0].ID, time.Now().Add(time.Hour), "receiver error: HTTP 503"); err != nil {
		t.Fatal(err)
	}
	if due, _ := o.Lease(ctx, 10, time.Second); len(due) != 0 {
		t.Fatalf("event leased before next_attempt_at")
	}
	st, err := o.Stats(ctx)
	if err != nil || st.Pending != 1 || st.Parked != 0 || st.OldestAge <= 0 {
		t.Fatalf("stats = %+v, %v", st, err)
	}

	// Parked events wait for an operator, then requeue makes them due.
	if err := o.Park(ctx, again[0].ID, "permanent: HTTP 422"); err != nil {
		t.Fatal(err)
	}
	parked, err := o.List(ctx, true, 10)
	if err != nil || len(parked) != 1 || parked[0].LastError != "permanent: HTTP 422" || parked[0].ParkedAt == nil {
		t.Fatalf("parked list = %+v, %v", parked, err)
	}
	if err := o.Requeue(ctx, again[0].ID); err != nil {
		t.Fatal(err)
	}
	due, err := o.Lease(ctx, 10, time.Minute)
	if err != nil || len(due) != 1 || due[0].Attempts != 1 {
		t.Fatalf("after requeue = %+v, %v", due, err)
	}
	if err := o.MarkDelivered(ctx, due[0].ID); err != nil {
		t.Fatal(err)
	}
	if st, _ := o.Stats(ctx); st.Pending != 0 || st.Parked != 0 || st.OldestAge != 0 {
		t.Fatalf("stats after delivery = %+v", st)
	}
	if err := o.Requeue(ctx, due[0].ID); err != outbox.ErrNotFound {
		t.Fatalf("requeue of delivered event: %v", err)
	}
}

// lossyAudit records every delivery and, for the first one, drops the
// connection after reading the request: the event reached the receiver but
// the sender never learns it.
type lossyAudit struct {
	mu       sync.Mutex
	bodies   [][]byte
	verifier sigauth.Verifier
	badSigs  int
}

func (a *lossyAudit) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	a.mu.Lock()
	if a.verifier.Verify(r.Header.Get(sigauth.Header), body) != nil {
		a.badSigs++
	}
	a.bodies = append(a.bodies, body)
	first := len(a.bodies) == 1
	a.mu.Unlock()
	if first {
		conn, _, _ := w.(http.Hijacker).Hijack()
		_ = conn.Close()
		return
	}
	w.WriteHeader(http.StatusOK) // duplicate acknowledged by event ID
}

func TestWorkerRedeliversAfterLostResponse(t *testing.T) {
	drainOutbox(t)
	pool := appPool(t)
	s, o := NewClaims(pool), NewOutbox(pool)
	c := decided(t, s)

	key := []byte(strings.Repeat("k", 32))
	audit := &lossyAudit{verifier: sigauth.Verifier{Keys: map[string][]byte{"k1": key}, MaxSkew: time.Minute}}
	srv := httptest.NewServer(audit)
	t.Cleanup(srv.Close)

	cfg := outbox.DefaultConfig()
	cfg.BackoffBase, cfg.BackoffMax, cfg.PollInterval = 10*time.Millisecond, 50*time.Millisecond, 20*time.Millisecond
	w := outbox.NewWorker(o, outbox.NewHTTPDeliverer(srv.URL, sigauth.Signer{KeyID: "k1", Key: key}), cfg,
		slog.New(slog.NewTextHandler(io.Discard, nil)), newTestMetrics())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	for {
		st, err := o.Stats(ctx)
		if err == nil && st.Pending == 0 {
			break
		}
		if ctx.Err() != nil {
			t.Fatalf("event not delivered: %+v", st)
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()

	audit.mu.Lock()
	defer audit.mu.Unlock()
	if len(audit.bodies) != 2 {
		t.Fatalf("receiver saw %d deliveries, want 2 (lost response, then redelivery)", len(audit.bodies))
	}
	if !slices.Equal(audit.bodies[0], audit.bodies[1]) {
		t.Fatal("redelivery changed the payload; the receiver could not deduplicate it")
	}
	if !strings.Contains(string(audit.bodies[0]), c.ID.String()) {
		t.Fatal("payload does not reference the claim")
	}
	if audit.badSigs != 0 {
		t.Fatalf("%d deliveries had invalid signatures", audit.badSigs)
	}
}
