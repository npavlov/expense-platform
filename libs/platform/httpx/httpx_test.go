package httpx

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

type payload struct {
	Amount int64 `json:"amount"`
}

func decode(body, contentType string) error {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	var p payload
	return DecodeJSON(httptest.NewRecorder(), req, 64, &p)
}

func TestDecodeJSON(t *testing.T) {
	cases := []struct {
		name, body, ct string
		want           error
	}{
		{"valid", `{"amount":1}`, "application/json", nil},
		{"valid with charset", `{"amount":1}`, "application/json; charset=utf-8", nil},
		{"unknown field", `{"amount":1,"status":"approved"}`, "application/json", ErrMalformedBody},
		{"two objects", `{"amount":1}{"amount":2}`, "application/json", ErrMalformedBody},
		{"wrong type", `{"amount":"1"}`, "application/json", ErrMalformedBody},
		{"too large", `{"amount":1,` + strings.Repeat(" ", 100) + `}`, "application/json", ErrBodyTooLarge},
		{"form content type", `amount=1`, "application/x-www-form-urlencoded", ErrUnsupportedMediaType},
		{"no content type", `{"amount":1}`, "", ErrUnsupportedMediaType},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := decode(tc.body, tc.ct)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestWrapRequestIDAndMetrics(t *testing.T) {
	mux := http.NewServeMux()
	var seen string
	mux.HandleFunc("GET /items/{id}", func(w http.ResponseWriter, r *http.Request) {
		seen = RequestID(r.Context())
		WriteJSON(w, http.StatusOK, map[string]string{"ok": "yes"})
	})
	mux.HandleFunc("GET /panic", func(http.ResponseWriter, *http.Request) { panic("boom") })

	var logs bytes.Buffer
	reg := prometheus.NewRegistry()
	h := Wrap(mux, slog.New(slog.NewJSONHandler(&logs, nil)), NewMetrics(reg, "public"))

	// A valid incoming ID is propagated.
	req := httptest.NewRequest(http.MethodGet, "/items/123?secret=x", nil)
	req.Header.Set(RequestIDHeader, "abc-123")
	req.Header.Set("Authorization", "Bearer super-secret-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if seen != "abc-123" || rec.Header().Get(RequestIDHeader) != "abc-123" {
		t.Fatalf("request id not propagated: handler=%q header=%q", seen, rec.Header().Get(RequestIDHeader))
	}

	// An invalid incoming ID is replaced.
	req = httptest.NewRequest(http.MethodGet, "/items/1", nil)
	req.Header.Set(RequestIDHeader, "bad id with spaces\n")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get(RequestIDHeader); got == "" || strings.Contains(got, " ") {
		t.Fatalf("invalid request id kept: %q", got)
	}

	// A panic becomes a 500 problem.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/panic", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("panic status = %d", rec.Code)
	}

	// Metrics use the route pattern, never the raw path or ID.
	if got := testutil.ToFloat64(mustCounter(t, reg, "GET /items/{id}")); got != 2 {
		t.Errorf("route counter = %v, want 2", got)
	}
	// Logs do not contain the token, the query string or the raw ID.
	for _, leak := range []string{"super-secret-token", "secret=x", "/items/123"} {
		if strings.Contains(logs.String(), leak) {
			t.Errorf("log contains %q:\n%s", leak, logs.String())
		}
	}
}

func mustCounter(t *testing.T, reg *prometheus.Registry, route string) prometheus.Collector {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() != "http_requests_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "route" && strings.Contains(l.GetValue(), "123") {
					t.Fatalf("raw ID leaked into metric label: %s", l.GetValue())
				}
			}
		}
	}
	return counterFor{reg: reg, route: route}
}

// counterFor sums http_requests_total for one route across codes.
type counterFor struct {
	reg   *prometheus.Registry
	route string
}

func (c counterFor) Describe(chan<- *prometheus.Desc) {}
func (c counterFor) Collect(ch chan<- prometheus.Metric) {
	families, _ := c.reg.Gather()
	var sum float64
	for _, f := range families {
		if f.GetName() != "http_requests_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "route" && l.GetValue() == c.route {
					sum += m.GetCounter().GetValue()
				}
			}
		}
	}
	ch <- prometheus.MustNewConstMetric(prometheus.NewDesc("sum", "", nil, nil), prometheus.CounterValue, sum)
}

func TestWriteProblem(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(WithRequestID(req.Context(), "rid-1"))
	rec := httptest.NewRecorder()
	WriteProblem(rec, req, http.StatusConflict, "already_decided", "The claim was already decided.")

	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content type = %q", ct)
	}
	var p Problem
	body, _ := io.ReadAll(rec.Body)
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatal(err)
	}
	if p.Status != 409 || p.Code != "already_decided" || p.RequestID != "rid-1" {
		t.Fatalf("problem = %+v", p)
	}
}

func FuzzDecodeJSON(f *testing.F) {
	f.Add(`{"amount":1}`)
	f.Add(`{"amount":1e400}`)
	f.Add(`[`)
	f.Fuzz(func(t *testing.T, body string) {
		_ = decode(body, "application/json") // must not panic
	})
}
