package authn_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/npavlov/expense-platform/libs/platform/authn"
	"github.com/npavlov/expense-platform/libs/platform/devtoken"
)

const clientID = "test-client"

// newIssuer starts a JWKS server and returns an issuer whose URL is that server.
func newIssuer(t *testing.T) (*devtoken.Issuer, *authn.Verifier) {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	iss, err := devtoken.NewIssuer(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	mux.Handle("GET /.well-known/jwks.json", iss.JWKSHandler())

	v, err := authn.NewVerifier(context.Background(), authn.Config{
		Issuer:              srv.URL,
		ClientIDs:           []string{clientID},
		AllowInsecureIssuer: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return iss, v
}

func TestVerifyAcceptsValidAccessToken(t *testing.T) {
	iss, v := newIssuer(t)
	tok, err := iss.Mint(devtoken.Claims{Subject: "u-1", Username: "alice", Groups: []string{"employees"}, ClientID: clientID})
	if err != nil {
		t.Fatal(err)
	}

	p, err := v.Verify(context.Background(), tok)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if p.Subject != "u-1" || p.Username != "alice" || !p.InGroup("employees") || p.InGroup("reviewers") {
		t.Fatalf("unexpected principal %+v", p)
	}
}

func TestVerifyRejects(t *testing.T) {
	iss, v := newIssuer(t)
	other, err := devtoken.NewIssuer(iss.URL) // same URL, different key
	if err != nil {
		t.Fatal(err)
	}
	base := devtoken.Claims{Subject: "u-1", Groups: []string{"employees"}, ClientID: clientID}
	mint := func(i *devtoken.Issuer, mutate func(*devtoken.Claims)) string {
		c := base
		mutate(&c)
		tok, err := i.Mint(c)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	valid := mint(iss, func(*devtoken.Claims) {})

	cases := map[string]string{
		"expired":           mint(iss, func(c *devtoken.Claims) { c.TTL = -time.Minute }),
		"wrong issuer":      mint(iss, func(c *devtoken.Claims) { c.Issuer = "https://evil.example" }),
		"id token":          mint(iss, func(c *devtoken.Claims) { c.TokenUse = "id" }),
		"wrong client":      mint(iss, func(c *devtoken.Claims) { c.ClientID = "other-app" }),
		"empty subject":     mint(iss, func(c *devtoken.Claims) { c.Subject = "" }),
		"unknown key":       mint(other, func(*devtoken.Claims) {}),
		"tampered payload":  tamper(t, valid),
		"alg none":          algNone(t, valid),
		"HS256 confusion":   hs256(t, valid),
		"garbage":           "not-a-jwt",
		"empty":             "",
		"signature removed": strings.Join(strings.Split(valid, ".")[:2], ".") + ".",
	}
	for name, tok := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := v.Verify(context.Background(), tok)
			if !errors.Is(err, authn.ErrInvalidToken) {
				t.Fatalf("err = %v, want ErrInvalidToken", err)
			}
		})
	}
}

func TestNewVerifierRequiresHTTPSIssuer(t *testing.T) {
	_, err := authn.NewVerifier(context.Background(), authn.Config{Issuer: "http://idp.example", ClientIDs: []string{"c"}})
	if err == nil {
		t.Fatal("http issuer accepted without AllowInsecureIssuer")
	}
	_, err = authn.NewVerifier(context.Background(), authn.Config{Issuer: "https://idp.example"})
	if err == nil {
		t.Fatal("verifier without client IDs accepted")
	}
}

func TestRequire(t *testing.T) {
	iss, v := newIssuer(t)
	h := authn.Require(v, []string{"reviewers"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := authn.FromContext(r.Context())
		_, _ = w.Write([]byte(p.Subject))
	}))
	employee, _ := iss.Mint(devtoken.Claims{Subject: "e-1", Groups: []string{"employees"}, ClientID: clientID})
	reviewer, _ := iss.Mint(devtoken.Claims{Subject: "r-1", Groups: []string{"employees", "reviewers"}, ClientID: clientID})

	cases := []struct {
		name   string
		header string
		want   int
	}{
		{"no header", "", http.StatusUnauthorized},
		{"basic auth", "Basic dXNlcjpwYXNz", http.StatusUnauthorized},
		{"wrong group", "Bearer " + employee, http.StatusForbidden},
		{"allowed", "Bearer " + reviewer, http.StatusOK},
		{"case-insensitive scheme", "bearer " + reviewer, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.want, rec.Body)
			}
			if tc.want == http.StatusUnauthorized && rec.Header().Get("WWW-Authenticate") == "" {
				t.Error("401 without WWW-Authenticate")
			}
			if tc.want == http.StatusOK && rec.Body.String() != "r-1" {
				t.Errorf("principal subject = %q, want r-1", rec.Body)
			}
		})
	}
}

func tamper(t *testing.T, tok string) string {
	t.Helper()
	parts := strings.Split(tok, ".")
	payload := decodeSegment(t, parts[1])
	payload["cognito:groups"] = []string{"employees", "reviewers"}
	parts[1] = encodeSegment(t, payload)
	return strings.Join(parts, ".")
}

func algNone(t *testing.T, tok string) string {
	t.Helper()
	parts := strings.Split(tok, ".")
	return encodeSegment(t, map[string]any{"alg": "none", "typ": "JWT"}) + "." + parts[1] + "."
}

// hs256 re-signs the token with HMAC using a public value as the secret, the
// classic algorithm-confusion attack against verifiers that trust the header.
func hs256(t *testing.T, tok string) string {
	t.Helper()
	parts := strings.Split(tok, ".")
	header := decodeSegment(t, parts[0])
	header["alg"] = "HS256"
	signing := encodeSegment(t, header) + "." + parts[1]
	mac := hmac.New(sha256.New, []byte("public-key-material"))
	mac.Write([]byte(signing))
	return signing + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func decodeSegment(t *testing.T, s string) map[string]any {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]any{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func encodeSegment(t *testing.T, m map[string]any) string {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
