// Package outbox delivers events from the transactional outbox table.
//
// Delivery is at least once: an event is marked delivered only after the
// receiver acknowledged it, so a lost acknowledgement or a crash leads to a
// redelivery of the same event ID, which the receiver deduplicates.
package outbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
)

// Event is a leased outbox row.
type Event struct {
	ID          uuid.UUID
	Type        string
	AggregateID uuid.UUID
	Payload     []byte
	RequestID   string
	Attempts    int
	CreatedAt   time.Time
}

// Stats describes the undelivered backlog.
type Stats struct {
	Pending   int64
	Parked    int64
	OldestAge time.Duration
}

// Status is the operator view of an undelivered event (no payload).
type Status struct {
	EventID       uuid.UUID  `json:"event_id"`
	EventType     string     `json:"event_type"`
	ClaimID       uuid.UUID  `json:"claim_id"`
	RequestID     string     `json:"request_id,omitempty"`
	Attempts      int        `json:"attempts"`
	CreatedAt     time.Time  `json:"created_at"`
	NextAttemptAt time.Time  `json:"next_attempt_at"`
	LastError     string     `json:"last_error,omitempty"`
	ParkedAt      *time.Time `json:"parked_at,omitempty"`
}

// ErrNotFound is returned when an event does not exist or is already delivered.
var ErrNotFound = errors.New("outbox event not found")

// Store is the outbox table.
type Store interface {
	Lease(ctx context.Context, n int, lease time.Duration) ([]Event, error)
	MarkDelivered(ctx context.Context, id uuid.UUID) error
	Retry(ctx context.Context, id uuid.UUID, next time.Time, reason string) error
	Park(ctx context.Context, id uuid.UUID, reason string) error
	Stats(ctx context.Context) (Stats, error)
	List(ctx context.Context, parkedOnly bool, limit int) ([]Status, error)
	Requeue(ctx context.Context, id uuid.UUID) error
}

// Deliverer sends one event to its destination.
type Deliverer interface {
	Deliver(ctx context.Context, e Event) error
}

// PermanentError marks a failure that retrying cannot fix (the receiver
// rejected the event as invalid). Such events are parked immediately.
type PermanentError struct{ Reason string }

func (e *PermanentError) Error() string { return "permanent: " + e.Reason }

// Config tunes the worker.
type Config struct {
	PollInterval    time.Duration // idle wait between empty polls
	BatchSize       int
	Lease           time.Duration // must exceed DeliveryTimeout
	DeliveryTimeout time.Duration
	BackoffBase     time.Duration
	BackoffMax      time.Duration
	// MaxAttempts bounds automatic retries of transient failures; after that
	// the event is parked and the outbox_events_parked alert fires.
	MaxAttempts int
}

// DefaultConfig returns settings for a small deployment.
func DefaultConfig() Config {
	return Config{
		PollInterval:    time.Second,
		BatchSize:       20,
		Lease:           30 * time.Second,
		DeliveryTimeout: 5 * time.Second,
		BackoffBase:     time.Second,
		BackoffMax:      30 * time.Second,
		MaxAttempts:     200, // about 1.5 h of retries at the 30 s cap
	}
}

// Worker delivers due events in a loop.
type Worker struct {
	store     Store
	deliverer Deliverer
	cfg       Config
	log       *slog.Logger
	metrics   *Metrics
}

// NewWorker returns a worker.
func NewWorker(store Store, d Deliverer, cfg Config, log *slog.Logger, m *Metrics) *Worker {
	return &Worker{store: store, deliverer: d, cfg: cfg, log: log, metrics: m}
}

// Run delivers events until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) error {
	w.log.Info("outbox worker started")
	for {
		n, err := w.RunOnce(ctx)
		if err != nil && ctx.Err() == nil {
			w.log.Error("outbox poll failed", "err", err)
		}
		if n == 0 || err != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(w.cfg.PollInterval):
			}
		}
	}
}

// RunOnce leases one batch and attempts each event; it returns how many events it handled.
func (w *Worker) RunOnce(ctx context.Context) (int, error) {
	events, err := w.store.Lease(ctx, w.cfg.BatchSize, w.cfg.Lease)
	if err != nil {
		return 0, fmt.Errorf("lease: %w", err)
	}
	for _, e := range events {
		w.attempt(ctx, e)
	}
	return len(events), nil
}

func (w *Worker) attempt(ctx context.Context, e Event) {
	log := w.log.With("event_id", e.ID, "claim_id", e.AggregateID, "request_id", e.RequestID, "attempt", e.Attempts)
	dctx, cancel := context.WithTimeout(ctx, w.cfg.DeliveryTimeout)
	start := time.Now()
	err := w.deliverer.Deliver(dctx, e)
	cancel()
	w.metrics.duration.Observe(time.Since(start).Seconds())

	// Bookkeeping uses a fresh context so a shutdown mid-delivery still
	// records the outcome; if it is lost, the lease expires and we retry.
	bctx, bcancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer bcancel()

	var perm *PermanentError
	switch {
	case err == nil:
		if err := w.store.MarkDelivered(bctx, e.ID); err != nil {
			log.Error("mark delivered failed; event will be redelivered", "err", err)
			return
		}
		w.metrics.attempts.WithLabelValues("delivered").Inc()
		log.Info("event delivered", "age_ms", time.Since(e.CreatedAt).Milliseconds())
	case errors.As(err, &perm):
		w.park(bctx, log, e, err.Error())
	case w.cfg.MaxAttempts > 0 && e.Attempts >= w.cfg.MaxAttempts:
		w.park(bctx, log, e, "max attempts reached: "+err.Error())
	default:
		next := time.Now().Add(Backoff(e.Attempts, w.cfg.BackoffBase, w.cfg.BackoffMax))
		if err := w.store.Retry(bctx, e.ID, next, err.Error()); err != nil {
			log.Error("schedule retry failed", "err", err)
		}
		w.metrics.attempts.WithLabelValues("retry").Inc()
		log.Warn("event delivery failed; will retry", "err", err, "next_attempt_at", next)
	}
}

func (w *Worker) park(ctx context.Context, log *slog.Logger, e Event, reason string) {
	if err := w.store.Park(ctx, e.ID, reason); err != nil {
		log.Error("park failed", "err", err)
		return
	}
	w.metrics.attempts.WithLabelValues("parked").Inc()
	log.Error("event parked; operator action required", "reason", reason)
}

// Backoff returns the delay before attempt+1: exponential from base, capped at
// max, with jitter in [d/2, d] so retries from many events do not synchronize.
func Backoff(attempt int, base, maxDelay time.Duration) time.Duration {
	d := base
	for i := 1; i < attempt && d < maxDelay; i++ {
		d *= 2
	}
	d = min(d, maxDelay)
	half := d / 2
	return half + rand.N(half+1) // #nosec G404 -- retry jitter, not security-sensitive
}

// Metrics of the outbox. No label carries an event, claim or user ID.
type Metrics struct {
	attempts  *prometheus.CounterVec
	duration  prometheus.Histogram
	pending   prometheus.Gauge
	parked    prometheus.Gauge
	oldestAge prometheus.Gauge
}

// NewMetrics registers outbox metrics.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		attempts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "outbox_delivery_attempts_total",
			Help: "Outbox delivery attempts by result (delivered, retry, parked).",
		}, []string{"result"}),
		duration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "outbox_delivery_duration_seconds",
			Help:    "Time spent in one delivery attempt.",
			Buckets: []float64{.01, .025, .05, .1, .25, .5, 1, 2.5, 5},
		}),
		pending: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "outbox_events_pending",
			Help: "Undelivered events that will be retried automatically.",
		}),
		parked: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "outbox_events_parked",
			Help: "Undelivered events waiting for an operator.",
		}),
		oldestAge: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "outbox_oldest_undelivered_age_seconds",
			Help: "Age of the oldest undelivered event (0 when the outbox is drained).",
		}),
	}
	for _, r := range []string{"delivered", "retry", "parked"} {
		m.attempts.WithLabelValues(r)
	}
	reg.MustRegister(m.attempts, m.duration, m.pending, m.parked, m.oldestAge)
	return m
}

// RunStats refreshes the backlog gauges every interval until ctx is cancelled.
func RunStats(ctx context.Context, store Store, m *Metrics, interval time.Duration, log *slog.Logger) error {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		st, err := store.Stats(ctx)
		if err == nil {
			m.pending.Set(float64(st.Pending))
			m.parked.Set(float64(st.Parked))
			m.oldestAge.Set(st.OldestAge.Seconds())
		} else if ctx.Err() == nil {
			log.Warn("outbox stats failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}
