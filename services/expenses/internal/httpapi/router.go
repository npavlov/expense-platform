// Package httpapi exposes the expenses service over HTTP.
package httpapi

import (
	"encoding/json"
	"net/http"
)

// NewRouter returns the HTTP handler with all routes of the expenses service.
func NewRouter() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", handleHealth)
	return mux
}

// handleHealth reports that the process is up and able to serve requests.
// It does not check dependencies: once PostgreSQL is added, readiness
// will get its own endpoint so Kubernetes can tell "alive" from "ready".
func handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
