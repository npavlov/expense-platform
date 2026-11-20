// Package authn verifies OIDC access tokens and exposes the caller's identity.
//
// The same verifier runs against Amazon Cognito in AWS and against the local
// development issuer (tools/devidp). Both issue Cognito-shaped access tokens:
// signed with RS256, with token_use=access, client_id and cognito:groups claims.
// There is no switch that disables verification; the local setup differs only
// in which issuer and keys are trusted.
package authn

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

// ErrInvalidToken is returned for any token that fails verification.
// Callers must not reveal the underlying reason to clients.
var ErrInvalidToken = errors.New("invalid token")

// Principal is the authenticated caller, taken only from a verified token.
type Principal struct {
	// Subject is the stable user ID (the token "sub" claim).
	Subject  string
	Username string
	Groups   []string
}

// InGroup reports whether the principal belongs to the group.
func (p Principal) InGroup(group string) bool {
	return slices.Contains(p.Groups, group)
}

// Config describes which tokens are trusted.
type Config struct {
	// Issuer must match the token "iss" claim exactly.
	Issuer string
	// JWKSURL defaults to Issuer + "/.well-known/jwks.json" (the Cognito layout).
	JWKSURL string
	// ClientIDs lists accepted "client_id" values (the app clients of the user pool).
	ClientIDs []string
	// AllowInsecureIssuer permits an http:// issuer. Only the local overlay sets it;
	// a manifest test fails if the AWS overlay does.
	AllowInsecureIssuer bool
}

// Verifier checks signature, algorithm, issuer, expiry, token type and client.
type Verifier struct {
	verifier  *oidc.IDTokenVerifier
	clientIDs []string
}

// NewVerifier builds a verifier. Signing keys are fetched lazily and cached;
// an unknown key ID triggers a refetch, so issuer key rotation is picked up.
// ctx must live as long as the verifier.
func NewVerifier(ctx context.Context, cfg Config) (*Verifier, error) {
	if cfg.Issuer == "" {
		return nil, errors.New("authn: issuer is required")
	}
	u, err := url.Parse(cfg.Issuer)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("authn: invalid issuer %q", cfg.Issuer)
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && cfg.AllowInsecureIssuer) {
		return nil, fmt.Errorf("authn: issuer must use https, got %q", cfg.Issuer)
	}
	if len(cfg.ClientIDs) == 0 {
		return nil, errors.New("authn: at least one client ID is required")
	}
	jwks := cfg.JWKSURL
	if jwks == "" {
		jwks = strings.TrimSuffix(cfg.Issuer, "/") + "/.well-known/jwks.json"
	}

	ctx = oidc.ClientContext(ctx, &http.Client{Timeout: 5 * time.Second})
	keys := oidc.NewRemoteKeySet(ctx, jwks)
	v := oidc.NewVerifier(cfg.Issuer, keys, &oidc.Config{
		// Cognito access tokens carry client_id instead of aud; checked below.
		SkipClientIDCheck:    true,
		SupportedSigningAlgs: []string{oidc.RS256},
	})
	return &Verifier{verifier: v, clientIDs: slices.Clone(cfg.ClientIDs)}, nil
}

type accessTokenClaims struct {
	TokenUse string   `json:"token_use"`
	ClientID string   `json:"client_id"`
	Username string   `json:"username"`
	Groups   []string `json:"cognito:groups"`
}

// Verify returns the principal for a valid access token.
func (v *Verifier) Verify(ctx context.Context, raw string) (Principal, error) {
	tok, err := v.verifier.Verify(ctx, raw)
	if err != nil {
		return Principal{}, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}
	var c accessTokenClaims
	if err := tok.Claims(&c); err != nil {
		return Principal{}, fmt.Errorf("%w: claims: %v", ErrInvalidToken, err)
	}
	// ID tokens are signed by the same keys; only access tokens grant API access.
	if c.TokenUse != "access" {
		return Principal{}, fmt.Errorf("%w: token_use %q", ErrInvalidToken, c.TokenUse)
	}
	if !slices.Contains(v.clientIDs, c.ClientID) {
		return Principal{}, fmt.Errorf("%w: unexpected client_id", ErrInvalidToken)
	}
	if tok.Subject == "" {
		return Principal{}, fmt.Errorf("%w: empty subject", ErrInvalidToken)
	}
	return Principal{Subject: tok.Subject, Username: c.Username, Groups: c.Groups}, nil
}

type ctxKey struct{}

// WithPrincipal stores the principal in the context.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// FromContext returns the principal set by the authentication middleware.
func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}
