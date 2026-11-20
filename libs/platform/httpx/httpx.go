// Package httpx holds the HTTP plumbing shared by the services: request IDs,
// access logs, metrics, strict JSON decoding and RFC 9457 problem responses.
package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"regexp"
	"runtime/debug"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
)

// RequestIDHeader carries the correlation ID between clients and services.
const RequestIDHeader = "X-Request-ID"

var validRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

type requestIDKey struct{}

// RequestID returns the request ID stored in the context, or "".
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// WithRequestID stores a request ID in the context.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// Problem is an RFC 9457 problem details body.
type Problem struct {
	Type      string `json:"type"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Code      string `json:"code"`
	Detail    string `json:"detail,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

// WriteProblem writes an error response. Details must never contain internal
// error text: callers pass a fixed, client-safe message.
func WriteProblem(w http.ResponseWriter, r *http.Request, status int, code, detail string) {
	p := Problem{
		Type:      "https://github.com/npavlov/expense-platform/blob/main/contracts/http/errors.md#" + code,
		Title:     http.StatusText(status),
		Status:    status,
		Code:      code,
		Detail:    detail,
		RequestID: RequestID(r.Context()),
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(p)
}

// WriteJSON writes a JSON response.
func WriteJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// Decode errors, mapped by handlers to 400/413/415 responses.
var (
	ErrUnsupportedMediaType = errors.New("content type must be application/json")
	ErrBodyTooLarge         = errors.New("request body too large")
	ErrMalformedBody        = errors.New("malformed JSON body")
)

// DecodeJSON reads exactly one JSON object of at most maxBytes into dst.
// Unknown fields are rejected, which blocks mass assignment of fields such as
// "status" or "employee_id" that the client must not set.
func DecodeJSON(w http.ResponseWriter, r *http.Request, maxBytes int64, dst any) error {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "application/json" {
		return ErrUnsupportedMediaType
	}
	body := http.MaxBytesReader(w, r.Body, maxBytes)
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return ErrBodyTooLarge
		}
		return fmt.Errorf("%w: %v", ErrMalformedBody, err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing data after JSON object", ErrMalformedBody)
	}
	return nil
}

// WriteDecodeError maps a DecodeJSON error to a response.
func WriteDecodeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrUnsupportedMediaType):
		WriteProblem(w, r, http.StatusUnsupportedMediaType, "unsupported_media_type", "Send the body as application/json.")
	case errors.Is(err, ErrBodyTooLarge):
		WriteProblem(w, r, http.StatusRequestEntityTooLarge, "body_too_large", "The request body is too large.")
	default:
		WriteProblem(w, r, http.StatusBadRequest, "malformed_body", "The body must be one JSON object with only the documented fields.")
	}
}

// Metrics are the RED metrics of one HTTP server. Labels are bounded:
// route is the registered pattern (never the raw path with IDs).
type Metrics struct {
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

// NewMetrics registers HTTP metrics for a server ("public", "internal").
func NewMetrics(reg prometheus.Registerer, server string) *Metrics {
	m := &Metrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name:        "http_requests_total",
			Help:        "HTTP requests by route, method and status code.",
			ConstLabels: prometheus.Labels{"server": server},
		}, []string{"route", "method", "code"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:        "http_request_duration_seconds",
			Help:        "HTTP request latency by route and method.",
			ConstLabels: prometheus.Labels{"server": server},
			Buckets:     []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5},
		}, []string{"route", "method"}),
	}
	reg.MustRegister(m.requests, m.duration)
	return m
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

// Wrap adds, outermost first: request ID, panic recovery, and access log plus
// metrics. mux must be the *http.ServeMux so the matched route pattern is
// visible after it runs. Logged fields are an allowlist: no headers, bodies,
// query strings or tokens.
func Wrap(mux *http.ServeMux, logger *slog.Logger, m *Metrics) http.Handler {
	observed := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		defer func() {
			if v := recover(); v != nil {
				logger.ErrorContext(r.Context(), "panic", "request_id", RequestID(r.Context()), "panic", fmt.Sprint(v), "stack", string(debug.Stack()))
				if rec.status == 0 {
					WriteProblem(rec, r, http.StatusInternalServerError, "internal", "Internal error.")
				}
			}
			route := r.Pattern
			if route == "" {
				route = "unmatched"
			}
			elapsed := time.Since(start)
			if m != nil {
				m.requests.WithLabelValues(route, r.Method, strconv.Itoa(rec.status)).Inc()
				m.duration.WithLabelValues(route, r.Method).Observe(elapsed.Seconds())
			}
			logger.InfoContext(r.Context(), "http request",
				"request_id", RequestID(r.Context()),
				"method", r.Method,
				"route", route,
				"status", rec.status,
				"bytes", rec.bytes,
				"duration_ms", elapsed.Milliseconds(),
			)
		}()
		mux.ServeHTTP(rec, r)
	})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(RequestIDHeader)
		if !validRequestID.MatchString(id) {
			id = uuid.NewString()
		}
		w.Header().Set(RequestIDHeader, id)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		observed.ServeHTTP(w, r.WithContext(WithRequestID(r.Context(), id)))
	})
}
