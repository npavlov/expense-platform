package authn

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/npavlov/expense-platform/libs/platform/httpx"
)

// TokenVerifier is implemented by *Verifier; tests substitute their own.
type TokenVerifier interface {
	Verify(ctx context.Context, raw string) (Principal, error)
}

// Require authenticates the request and checks that the caller is in at least
// one of the groups. It fails closed: a missing, malformed or unverifiable token
// is 401, a valid token without a required group is 403. Every protected route
// is registered through it, so a route without an explicit group list cannot exist.
func Require(v TokenVerifier, groups []string, next http.Handler) http.Handler {
	if len(groups) == 0 {
		panic("authn.Require: at least one group is required")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, ok := bearerToken(r)
		if !ok {
			unauthorized(w, r)
			return
		}
		p, err := v.Verify(r.Context(), raw)
		if err != nil {
			slog.InfoContext(r.Context(), "authentication failed", "request_id", httpx.RequestID(r.Context()), "reason", err.Error())
			unauthorized(w, r)
			return
		}
		allowed := false
		for _, g := range groups {
			if p.InGroup(g) {
				allowed = true
				break
			}
		}
		if !allowed {
			slog.InfoContext(r.Context(), "access denied", "request_id", httpx.RequestID(r.Context()), "subject", p.Subject, "route", r.Pattern)
			httpx.WriteProblem(w, r, http.StatusForbidden, "forbidden", "Your role does not allow this operation.")
			return
		}
		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
	})
}

func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	scheme, token, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" || len(token) > 8192 {
		return "", false
	}
	return token, true
}

func unauthorized(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
	httpx.WriteProblem(w, r, http.StatusUnauthorized, "unauthenticated", "A valid access token is required.")
}
