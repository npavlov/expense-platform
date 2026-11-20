// Package httpapi exposes the expenses service over HTTP.
package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/npavlov/expense-platform/libs/platform/authn"
	"github.com/npavlov/expense-platform/libs/platform/httpx"
	"github.com/npavlov/expense-platform/services/expenses/internal/claims"
)

// Groups from the identity provider (Cognito groups, or the local dev issuer).
const (
	GroupEmployees = "employees"
	GroupReviewers = "reviewers"
)

// maxBodyBytes bounds every request body; a claim is far smaller.
const maxBodyBytes = 4 << 10

// Deps are the collaborators of the router.
type Deps struct {
	Claims   *claims.Service
	Verifier authn.TokenVerifier
	// Ready reports whether dependencies (the database) can serve traffic.
	Ready func(context.Context) error
	Log   *slog.Logger
}

// NewRouter returns the mux with all routes of the expenses service.
// Routes not listed here do not exist (404); every /v1 route requires a
// verified token and an explicit group.
func NewRouter(d Deps) *http.ServeMux {
	if d.Log == nil {
		d.Log = slog.Default()
	}
	a := &api{Deps: d}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", handleHealth)
	mux.HandleFunc("GET /ready", a.handleReady)

	employees := []string{GroupEmployees}
	reviewers := []string{GroupReviewers}
	anyone := []string{GroupEmployees, GroupReviewers}
	mux.Handle("POST /v1/claims", authn.Require(d.Verifier, employees, http.HandlerFunc(a.createClaim)))
	mux.Handle("GET /v1/claims", authn.Require(d.Verifier, employees, http.HandlerFunc(a.listOwnClaims)))
	mux.Handle("GET /v1/claims/{id}", authn.Require(d.Verifier, anyone, http.HandlerFunc(a.getClaim)))
	mux.Handle("GET /v1/review/claims", authn.Require(d.Verifier, reviewers, http.HandlerFunc(a.listForReview)))
	mux.Handle("POST /v1/claims/{id}/decision", authn.Require(d.Verifier, reviewers, http.HandlerFunc(a.decide)))
	return mux
}

// handleHealth reports that the process is up and able to serve requests.
// It does not check dependencies: /ready does, so Kubernetes can tell
// "alive" (restart if not) from "ready" (route traffic if so).
func handleHealth(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type api struct{ Deps }

func (a *api) handleReady(w http.ResponseWriter, r *http.Request) {
	if a.Ready != nil {
		if err := a.Ready(r.Context()); err != nil {
			a.Log.Warn("not ready", "err", err)
			httpx.WriteProblem(w, r, http.StatusServiceUnavailable, "not_ready", "Dependencies are not available.")
			return
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// claimResponse is the public representation of a claim.
type claimResponse struct {
	ID          uuid.UUID  `json:"id"`
	EmployeeID  string     `json:"employee_id"`
	AmountCents int64      `json:"amount_cents"`
	Currency    string     `json:"currency"`
	Description string     `json:"description"`
	Status      string     `json:"status"`
	CreatedAt   time.Time  `json:"created_at"`
	DecidedBy   *string    `json:"decided_by"`
	DecidedAt   *time.Time `json:"decided_at"`
}

func toResponse(c claims.Claim) claimResponse {
	return claimResponse{
		ID: c.ID, EmployeeID: c.EmployeeID, AmountCents: c.AmountCents, Currency: claims.Currency,
		Description: c.Description, Status: string(c.Status), CreatedAt: c.CreatedAt.UTC(),
		DecidedBy: c.DecidedBy, DecidedAt: utcPtr(c.DecidedAt),
	}
}

func toList(cs []claims.Claim) map[string][]claimResponse {
	items := make([]claimResponse, 0, len(cs))
	for _, c := range cs {
		items = append(items, toResponse(c))
	}
	return map[string][]claimResponse{"items": items}
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func viewer(r *http.Request) claims.Viewer {
	p, _ := authn.FromContext(r.Context())
	return claims.Viewer{Subject: p.Subject, IsReviewer: p.InGroup(GroupReviewers)}
}

type createClaimRequest struct {
	AmountCents *int64  `json:"amount_cents"`
	Description *string `json:"description"`
}

func (a *api) createClaim(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		httpx.WriteProblem(w, r, http.StatusBadRequest, "idempotency_key_required", "Send an Idempotency-Key header, unique per claim you submit.")
		return
	}
	if !claims.ValidIdempotencyKey(key) {
		httpx.WriteProblem(w, r, http.StatusBadRequest, "idempotency_key_invalid", "Idempotency-Key must be 8-128 characters of A-Z a-z 0-9 . _ : -")
		return
	}
	var req createClaimRequest
	if err := httpx.DecodeJSON(w, r, maxBodyBytes, &req); err != nil {
		httpx.WriteDecodeError(w, r, err)
		return
	}
	if req.AmountCents == nil || req.Description == nil {
		httpx.WriteProblem(w, r, http.StatusUnprocessableEntity, "validation_failed", "amount_cents and description are required.")
		return
	}

	v := viewer(r)
	c, replayed, err := a.Claims.Submit(r.Context(), v.Subject, key, claims.NewClaim{AmountCents: *req.AmountCents, Description: *req.Description})
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	} else {
		a.Log.InfoContext(r.Context(), "claim submitted", "request_id", httpx.RequestID(r.Context()), "claim_id", c.ID, "subject", v.Subject)
	}
	w.Header().Set("Location", "/v1/claims/"+c.ID.String())
	httpx.WriteJSON(w, http.StatusCreated, toResponse(c))
}

func (a *api) listOwnClaims(w http.ResponseWriter, r *http.Request) {
	limit, ok := parseLimit(w, r)
	if !ok {
		return
	}
	cs, err := a.Claims.ListOwn(r.Context(), viewer(r), limit)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toList(cs))
}

func (a *api) listForReview(w http.ResponseWriter, r *http.Request) {
	limit, ok := parseLimit(w, r)
	if !ok {
		return
	}
	cs, err := a.Claims.ListForReview(r.Context(), viewer(r), limit)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toList(cs))
}

func (a *api) getClaim(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		a.writeError(w, r, claims.ErrNotFound)
		return
	}
	c, err := a.Claims.Get(r.Context(), viewer(r), id)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toResponse(c))
}

type decisionRequest struct {
	Decision string `json:"decision"`
}

func (a *api) decide(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		a.writeError(w, r, claims.ErrNotFound)
		return
	}
	var req decisionRequest
	if err := httpx.DecodeJSON(w, r, maxBodyBytes, &req); err != nil {
		httpx.WriteDecodeError(w, r, err)
		return
	}
	v := viewer(r)
	rid := httpx.RequestID(r.Context())
	c, replayed, err := a.Claims.Decide(r.Context(), v, id, claims.Decision(req.Decision), rid)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	} else {
		a.Log.InfoContext(r.Context(), "claim decided", "request_id", rid, "claim_id", c.ID, "decision", c.Status, "subject", v.Subject)
	}
	httpx.WriteJSON(w, http.StatusOK, toResponse(c))
}

func parseLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	v := r.URL.Query().Get("limit")
	if v == "" {
		return claims.DefaultListLimit, true
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > claims.MaxListLimit {
		httpx.WriteProblem(w, r, http.StatusBadRequest, "invalid_parameter", "limit must be an integer from 1 to 100.")
		return 0, false
	}
	return n, true
}

func (a *api) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var ve *claims.ValidationError
	switch {
	case errors.As(err, &ve):
		httpx.WriteProblem(w, r, http.StatusUnprocessableEntity, "validation_failed", ve.Error())
	case errors.Is(err, claims.ErrNotFound):
		httpx.WriteProblem(w, r, http.StatusNotFound, "not_found", "Claim not found.")
	case errors.Is(err, claims.ErrSelfDecision):
		a.Log.WarnContext(r.Context(), "self decision rejected", "request_id", httpx.RequestID(r.Context()), "subject", viewer(r).Subject)
		httpx.WriteProblem(w, r, http.StatusForbidden, "self_decision_forbidden", "You cannot decide your own claim.")
	case errors.Is(err, claims.ErrAlreadyDecided):
		httpx.WriteProblem(w, r, http.StatusConflict, "already_decided", "The claim has already been decided.")
	case errors.Is(err, claims.ErrIdempotencyKeyReused):
		httpx.WriteProblem(w, r, http.StatusUnprocessableEntity, "idempotency_key_reused", "This Idempotency-Key was used for a different request.")
	case errors.Is(err, context.Canceled):
		// The client went away; nothing useful to send.
	default:
		a.Log.ErrorContext(r.Context(), "request failed", "request_id", httpx.RequestID(r.Context()), "err", err)
		httpx.WriteProblem(w, r, http.StatusInternalServerError, "internal", "Internal error.")
	}
}
