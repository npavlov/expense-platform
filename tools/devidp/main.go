// Command devidp is a local stand-in for Amazon Cognito. It issues
// Cognito-shaped RS256 access tokens for four fictional users, so the services
// run the same verification code locally as in AWS. It is deployed only by the
// local Kubernetes overlay and has no passwords: it is a development tool,
// never a production identity provider.
//
//	POST /token  (form: username=alice)  -> {"access_token": "...", ...}
//	GET  /.well-known/jwks.json
//	GET  /.well-known/openid-configuration
package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/npavlov/expense-platform/libs/platform/devtoken"
	"github.com/npavlov/expense-platform/libs/platform/httpx"
	"github.com/npavlov/expense-platform/libs/platform/logging"
	"github.com/npavlov/expense-platform/libs/platform/server"
)

type user struct {
	Subject string
	Groups  []string
}

// Fictional users with stable subjects, so data created in one session is
// still owned by the same user after a restart.
var users = map[string]user{
	"alice":  {"a11ce000-0000-4000-8000-000000000001", []string{"employees"}},
	"bob":    {"b0b00000-0000-4000-8000-000000000002", []string{"employees"}},
	"rita":   {"4174a000-0000-4000-8000-000000000003", []string{"employees", "reviewers"}},
	"victor": {"71c70000-0000-4000-8000-000000000004", []string{"reviewers"}},
}

const tokenTTL = 15 * time.Minute

func main() {
	logger := logging.New("devidp")
	issuerURL := os.Getenv("ISSUER")
	clientID := os.Getenv("CLIENT_ID")
	if issuerURL == "" || clientID == "" {
		logger.Error("ISSUER and CLIENT_ID must be set")
		os.Exit(1)
	}
	iss, err := devtoken.NewIssuer(issuerURL)
	if err != nil {
		logger.Error("generate key", "err", err)
		os.Exit(1)
	}
	logger.Warn("development identity provider: issues tokens without passwords; never deploy outside a local cluster", "issuer", issuerURL)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.Handle("GET /.well-known/jwks.json", iss.JWKSHandler())
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"issuer":                                issuerURL,
			"jwks_uri":                              strings.TrimSuffix(issuerURL, "/") + "/.well-known/jwks.json",
			"token_endpoint":                        strings.TrimSuffix(issuerURL, "/") + "/token",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<10)
		if err := r.ParseForm(); err != nil {
			httpx.WriteProblem(w, r, http.StatusBadRequest, "invalid_request", "Send username as a form field.")
			return
		}
		name := r.PostForm.Get("username")
		u, ok := users[name]
		if !ok {
			names := make([]string, 0, len(users))
			for n := range users {
				names = append(names, n)
			}
			slices.Sort(names)
			httpx.WriteProblem(w, r, http.StatusBadRequest, "unknown_user", "Known users: "+strings.Join(names, ", "))
			return
		}
		tok, err := iss.Mint(devtoken.Claims{Subject: u.Subject, Username: name, Groups: u.Groups, ClientID: clientID, TTL: tokenTTL})
		if err != nil {
			logger.Error("mint token", "err", err)
			httpx.WriteProblem(w, r, http.StatusInternalServerError, "internal", "Internal error.")
			return
		}
		logger.Info("token issued", "username", name, "request_id", httpx.RequestID(r.Context()))
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": tok,
			"token_type":   "Bearer",
			"expires_in":   int(tokenTTL.Seconds()),
		})
	})

	srv := server.New(server.Env("HTTP_ADDR", ":8080"), httpx.Wrap(mux, logger, nil))
	if err := server.Run(logger, 5*time.Second, []*http.Server{srv}); err != nil {
		slog.Error("stopped", "err", err)
		os.Exit(1)
	}
}
