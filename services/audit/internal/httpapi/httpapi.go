// Package httpapi exposes the audit service.
//
// Two listeners with different trust:
//   - public  (reachable through the ingress): decision history, for users in
//     the reviewers group, authenticated with the same OIDC verifier as expenses.
//   - internal (cluster network only, NetworkPolicy allows only expenses pods):
//     event ingestion, authenticated with an HMAC signature. A valid user token
//     is not accepted here, and a signature is not accepted on the public side.
package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/npavlov/expense-platform/libs/platform/authn"
	"github.com/npavlov/expense-platform/libs/platform/httpx"
	"github.com/npavlov/expense-platform/libs/platform/sigauth"
	"github.com/npavlov/expense-platform/services/audit/internal/events"
	"github.com/npavlov/expense-platform/services/audit/internal/storage"
)

// GroupReviewers may read decision history.
const GroupReviewers = "reviewers"

const maxEventBytes = 64 << 10

// Store is the decision history.
type Store interface {
	Record(ctx context.Context, d events.Decision) (bool, error)
	ByClaim(ctx context.Context, claimID uuid.UUID) ([]storage.Entry, error)
}

// Deps are the collaborators of both routers.
type Deps struct {
	Store     Store
	Verifier  authn.TokenVerifier
	Signature sigauth.Verifier
	Ready     func(context.Context) error
	Log       *slog.Logger
	Metrics   *Metrics
}

// Metrics counts ingested events by result.
type Metrics struct {
	received *prometheus.CounterVec
}

// NewMetrics registers ingestion metrics.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{received: prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "audit_events_received_total",
		Help: "Events received on the internal endpoint by result.",
	}, []string{"result"})}
	for _, r := range []string{"recorded", "duplicate", "unauthenticated", "invalid", "conflict", "error"} {
		m.received.WithLabelValues(r)
	}
	reg.MustRegister(m.received)
	return m
}

func (m *Metrics) inc(result string) {
	if m != nil {
		m.received.WithLabelValues(result).Inc()
	}
}

// NewPublicRouter serves health, readiness and decision history.
func NewPublicRouter(d Deps) *http.ServeMux {
	mux := http.NewServeMux()
	health(mux, d)
	mux.Handle("GET /v1/audit/claims/{id}/decisions", authn.Require(d.Verifier, []string{GroupReviewers},
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, err := uuid.Parse(r.PathValue("id"))
			if err != nil {
				httpx.WriteProblem(w, r, http.StatusNotFound, "not_found", "Claim not found.")
				return
			}
			entries, err := d.Store.ByClaim(r.Context(), id)
			if err != nil {
				d.Log.ErrorContext(r.Context(), "read history", "request_id", httpx.RequestID(r.Context()), "err", err)
				httpx.WriteProblem(w, r, http.StatusInternalServerError, "internal", "Internal error.")
				return
			}
			p, _ := authn.FromContext(r.Context())
			// Reading the history is itself an access to financial data; record who looked.
			d.Log.InfoContext(r.Context(), "history read", "request_id", httpx.RequestID(r.Context()), "claim_id", id, "subject", p.Subject, "entries", len(entries))
			httpx.WriteJSON(w, http.StatusOK, map[string]any{"claim_id": id, "decisions": entries})
		})))
	return mux
}

// NewInternalRouter serves event ingestion.
func NewInternalRouter(d Deps) *http.ServeMux {
	mux := http.NewServeMux()
	health(mux, d)
	mux.HandleFunc("POST /internal/v1/events", func(w http.ResponseWriter, r *http.Request) {
		log := d.Log.With("request_id", httpx.RequestID(r.Context()))
		// The signature covers the raw bytes, so read them before parsing.
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxEventBytes))
		if err != nil {
			d.Metrics.inc("invalid")
			httpx.WriteProblem(w, r, http.StatusRequestEntityTooLarge, "body_too_large", "Event too large.")
			return
		}
		if err := d.Signature.Verify(r.Header.Get(sigauth.Header), body); err != nil {
			d.Metrics.inc("unauthenticated")
			log.WarnContext(r.Context(), "event rejected: bad signature", "reason", err.Error())
			httpx.WriteProblem(w, r, http.StatusUnauthorized, "invalid_signature", "Missing or invalid signature.")
			return
		}
		ev, err := events.Parse(body)
		if err != nil {
			d.Metrics.inc("invalid")
			code := "invalid_event"
			if errors.Is(err, events.ErrUnsupportedVersion) {
				code = "unsupported_schema_version"
			}
			log.WarnContext(r.Context(), "event rejected", "reason", err.Error())
			httpx.WriteProblem(w, r, http.StatusUnprocessableEntity, code, err.Error())
			return
		}
		log = log.With("event_id", ev.EventID, "claim_id", ev.ClaimID, "correlation_id", ev.CorrelationID)

		inserted, err := d.Store.Record(r.Context(), ev)
		switch {
		case errors.Is(err, storage.ErrConflict):
			d.Metrics.inc("conflict")
			log.ErrorContext(r.Context(), "event ID reused with different content")
			httpx.WriteProblem(w, r, http.StatusConflict, "event_conflict", "This event ID was recorded with different content.")
		case err != nil:
			d.Metrics.inc("error")
			log.ErrorContext(r.Context(), "record event", "err", err)
			httpx.WriteProblem(w, r, http.StatusServiceUnavailable, "unavailable", "Try again later.")
		case inserted:
			d.Metrics.inc("recorded")
			log.InfoContext(r.Context(), "decision recorded", "decision", ev.Decision)
			httpx.WriteJSON(w, http.StatusCreated, map[string]string{"event_id": ev.EventID.String(), "status": "recorded"})
		default:
			d.Metrics.inc("duplicate")
			log.InfoContext(r.Context(), "duplicate event acknowledged")
			httpx.WriteJSON(w, http.StatusOK, map[string]string{"event_id": ev.EventID.String(), "status": "duplicate"})
		}
	})
	return mux
}

func health(mux *http.ServeMux, d Deps) {
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, r *http.Request) {
		if d.Ready != nil {
			if err := d.Ready(r.Context()); err != nil {
				d.Log.Warn("not ready", "err", err)
				httpx.WriteProblem(w, r, http.StatusServiceUnavailable, "not_ready", "Dependencies are not available.")
				return
			}
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})
}
