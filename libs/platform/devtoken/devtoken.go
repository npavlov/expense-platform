// Package devtoken issues Cognito-shaped RS256 access tokens for local
// development and tests. It is used by tools/devidp and by test suites,
// never by the services themselves.
package devtoken

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// Issuer signs tokens with an in-memory RSA key.
type Issuer struct {
	URL   string
	key   *rsa.PrivateKey
	keyID string
}

// NewIssuer generates a fresh signing key.
func NewIssuer(url string) (*Issuer, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	kid := make([]byte, 8)
	if _, err := rand.Read(kid); err != nil {
		return nil, err
	}
	return &Issuer{URL: url, key: key, keyID: hex.EncodeToString(kid)}, nil
}

// Claims describes one access token.
type Claims struct {
	Subject  string
	Username string
	Groups   []string
	ClientID string
	// TokenUse defaults to "access"; tests set "id" to check rejection.
	TokenUse string
	TTL      time.Duration
	// Issuer overrides the issuer URL; tests use it to check rejection.
	Issuer string
}

// Mint returns a signed compact JWT.
func (i *Issuer) Mint(c Claims) (string, error) {
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: i.key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", i.keyID),
	)
	if err != nil {
		return "", err
	}
	now := time.Now()
	ttl := c.TTL
	if ttl == 0 {
		ttl = 15 * time.Minute
	}
	tokenUse := c.TokenUse
	if tokenUse == "" {
		tokenUse = "access"
	}
	iss := c.Issuer
	if iss == "" {
		iss = i.URL
	}
	jti := make([]byte, 16)
	_, _ = rand.Read(jti)
	return jwt.Signed(signer).
		Claims(jwt.Claims{
			Issuer:   iss,
			Subject:  c.Subject,
			IssuedAt: jwt.NewNumericDate(now),
			Expiry:   jwt.NewNumericDate(now.Add(ttl)),
			ID:       hex.EncodeToString(jti),
		}).
		Claims(map[string]any{
			"token_use":      tokenUse,
			"client_id":      c.ClientID,
			"username":       c.Username,
			"cognito:groups": c.Groups,
			"scope":          "openid",
			"auth_time":      now.Unix(),
		}).
		Serialize()
}

// JWKS returns the public key set.
func (i *Issuer) JWKS() jose.JSONWebKeySet {
	return jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
		Key:       &i.key.PublicKey,
		KeyID:     i.keyID,
		Algorithm: string(jose.RS256),
		Use:       "sig",
	}}}
}

// JWKSHandler serves the public key set at /.well-known/jwks.json.
func (i *Issuer) JWKSHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(i.JWKS())
	})
}
