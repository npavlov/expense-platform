//go:build e2e

// Package e2e drives the deployed system through its public APIs only.
//
//	E2E_EXPENSES_URL  default http://127.0.0.1:30080
//	E2E_AUDIT_URL     default http://127.0.0.1:30081
//	E2E_TOKEN_URL     default http://127.0.0.1:30082/token (local devidp)
//	E2E_TOKEN_<USER>  optional pre-issued token per user (e.g. from Cognito),
//	                  overrides E2E_TOKEN_URL for that user
//	E2E_DISRUPTIVE=1  also run tests that stop and start the audit deployment
//	                  (needs kubectl; E2E_KUBE_CONTEXT, E2E_NAMESPACE)
package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

var (
	expensesURL = env("E2E_EXPENSES_URL", "http://127.0.0.1:30080")
	auditURL    = env("E2E_AUDIT_URL", "http://127.0.0.1:30081")
	tokenURL    = env("E2E_TOKEN_URL", "http://127.0.0.1:30082/token")
	httpClient  = &http.Client{Timeout: 10 * time.Second}
)

var (
	tokenMu sync.Mutex
	tokens  = map[string]string{}
)

// token returns an access token for a fictional user.
func token(t *testing.T, user string) string {
	t.Helper()
	if v := os.Getenv("E2E_TOKEN_" + strings.ToUpper(user)); v != "" {
		return v
	}
	tokenMu.Lock()
	defer tokenMu.Unlock()
	if tok, ok := tokens[user]; ok {
		return tok
	}
	resp, err := httpClient.PostForm(tokenURL, url.Values{"username": {user}})
	if err != nil {
		t.Fatalf("get token for %s: %v", user, err)
	}
	defer resp.Body.Close()
	var body struct {
		AccessToken string `json:"access_token"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&body) != nil || body.AccessToken == "" {
		t.Fatalf("get token for %s: HTTP %d", user, resp.StatusCode)
	}
	tokens[user] = body.AccessToken
	return body.AccessToken
}

type result struct {
	Status int
	Header http.Header
	Body   map[string]any
}

func (r result) str(key string) string {
	s, _ := r.Body[key].(string)
	return s
}

func call(t *testing.T, method, base, path, bearer string, body any, headers ...string) result {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, base+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	out := result{Status: resp.StatusCode, Header: resp.Header}
	raw, _ := io.ReadAll(resp.Body)
	if len(bytes.TrimSpace(raw)) > 0 {
		_ = json.Unmarshal(raw, &out.Body)
	}
	return out
}

func submit(t *testing.T, user string, amount int, desc string) result {
	t.Helper()
	return call(t, http.MethodPost, expensesURL, "/v1/claims", token(t, user),
		map[string]any{"amount_cents": amount, "description": desc}, "Idempotency-Key", uuid.NewString())
}

func mustSubmit(t *testing.T, user string, amount int, desc string) string {
	t.Helper()
	r := submit(t, user, amount, desc)
	if r.Status != http.StatusCreated {
		t.Fatalf("submit as %s: %d %v", user, r.Status, r.Body)
	}
	return r.str("id")
}

func decide(t *testing.T, user, id, decision string, headers ...string) result {
	t.Helper()
	return call(t, http.MethodPost, expensesURL, "/v1/claims/"+id+"/decision", token(t, user),
		map[string]string{"decision": decision}, headers...)
}

func history(t *testing.T, id string) (int, []map[string]any) {
	t.Helper()
	r := call(t, http.MethodGet, auditURL, "/v1/audit/claims/"+id+"/decisions", token(t, "rita"), nil)
	var out []map[string]any
	if list, ok := r.Body["decisions"].([]any); ok {
		for _, d := range list {
			out = append(out, d.(map[string]any))
		}
	}
	return r.Status, out
}

// waitForHistory polls until the claim has n history entries or the timeout passes.
func waitForHistory(t *testing.T, id string, n int, timeout time.Duration) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	reviewer := token(t, "rita")
	for {
		status, entries := tryHistory(reviewer, id)
		if status == http.StatusOK && len(entries) >= n {
			return entries
		}
		if time.Now().After(deadline) {
			t.Fatalf("history of %s: want %d entries within %v, have %d (last status %d)", id, n, timeout, len(entries), status)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// tryHistory is history without failing the test on transport errors (audit may be down).
func tryHistory(bearer, id string) (int, []map[string]any) {
	req, _ := http.NewRequest(http.MethodGet, auditURL+"/v1/audit/claims/"+id+"/decisions", nil)
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, nil
	}
	defer resp.Body.Close()
	var body struct {
		Decisions []map[string]any `json:"decisions"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body.Decisions
}

func must(t *testing.T, r result, want int, what string) {
	t.Helper()
	if r.Status != want {
		t.Fatalf("%s: HTTP %d, want %d: %v", what, r.Status, want, r.Body)
	}
}

func describe(id string) string { return fmt.Sprintf("claim %s", id) }
