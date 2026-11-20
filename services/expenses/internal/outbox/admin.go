package outbox

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"github.com/npavlov/expense-platform/libs/platform/httpx"
)

// AdminRoutes registers the operator endpoints. They are served on a separate
// listener bound to 127.0.0.1 inside the pod, reachable only through
// "kubectl port-forward", which already requires cluster RBAC.
//
//	GET  /admin/outbox?state=parked|undelivered&limit=N
//	POST /admin/outbox/{event_id}/requeue
func AdminRoutes(mux *http.ServeMux, store Store, log *slog.Logger) {
	mux.HandleFunc("GET /admin/outbox", func(w http.ResponseWriter, r *http.Request) {
		state := r.URL.Query().Get("state")
		if state == "" {
			state = "undelivered"
		}
		if state != "parked" && state != "undelivered" {
			httpx.WriteProblem(w, r, http.StatusBadRequest, "invalid_parameter", `state must be "parked" or "undelivered"`)
			return
		}
		limit := 50
		if v := r.URL.Query().Get("limit"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > 500 {
				httpx.WriteProblem(w, r, http.StatusBadRequest, "invalid_parameter", "limit must be 1..500")
				return
			}
			limit = n
		}
		items, err := store.List(r.Context(), state == "parked", limit)
		if err != nil {
			log.Error("list outbox", "err", err)
			httpx.WriteProblem(w, r, http.StatusInternalServerError, "internal", "Internal error.")
			return
		}
		stats, err := store.Stats(r.Context())
		if err != nil {
			log.Error("outbox stats", "err", err)
			httpx.WriteProblem(w, r, http.StatusInternalServerError, "internal", "Internal error.")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"pending":            stats.Pending,
			"parked":             stats.Parked,
			"oldest_age_seconds": int64(stats.OldestAge.Seconds()),
			"items":              items,
		})
	})

	mux.HandleFunc("POST /admin/outbox/{id}/requeue", func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			httpx.WriteProblem(w, r, http.StatusNotFound, "not_found", "No undelivered event with this ID.")
			return
		}
		if err := store.Requeue(r.Context(), id); err != nil {
			if errors.Is(err, ErrNotFound) {
				httpx.WriteProblem(w, r, http.StatusNotFound, "not_found", "No undelivered event with this ID.")
				return
			}
			log.Error("requeue outbox event", "err", err)
			httpx.WriteProblem(w, r, http.StatusInternalServerError, "internal", "Internal error.")
			return
		}
		log.Warn("outbox event requeued by operator", "event_id", id)
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"event_id": id.String(), "status": "requeued"})
	})
}
