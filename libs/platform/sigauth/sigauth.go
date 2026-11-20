// Package sigauth authenticates service-to-service requests with HMAC-SHA256.
//
// The sender signs "<unix timestamp>.<raw body>" and sends
//
//	X-Expense-Signature: t=<unix>,kid=<key id>,v1=<hex mac>
//
// The receiver recomputes the MAC over the raw bytes it received (never over
// re-serialized JSON), compares in constant time and rejects timestamps outside
// the allowed skew. Replays inside the window are absorbed by event-ID
// deduplication in the receiver. Several keys can be active at once, so a key
// is rotated by adding the new one, switching the sender, then removing the old.
package sigauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Header is the request header that carries the signature.
const Header = "X-Expense-Signature"

// ErrInvalidSignature is returned for any signature that does not verify.
var ErrInvalidSignature = errors.New("invalid signature")

// Signer signs outgoing requests with one key.
type Signer struct {
	KeyID string
	Key   []byte
	Now   func() time.Time
}

// Sign returns the header value for body.
func (s Signer) Sign(body []byte) string {
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	ts := strconv.FormatInt(now().Unix(), 10)
	return fmt.Sprintf("t=%s,kid=%s,v1=%s", ts, s.KeyID, hex.EncodeToString(mac(s.Key, ts, body)))
}

// Verifier checks incoming signatures against a set of active keys.
type Verifier struct {
	Keys    map[string][]byte
	MaxSkew time.Duration
	Now     func() time.Time
}

// Verify checks header against body.
func (v Verifier) Verify(header string, body []byte) error {
	var ts, kid, sig string
	for part := range strings.SplitSeq(header, ",") {
		k, val, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			return ErrInvalidSignature
		}
		switch k {
		case "t":
			ts = val
		case "kid":
			kid = val
		case "v1":
			sig = val
		}
	}
	key, ok := v.Keys[kid]
	if !ok || ts == "" || sig == "" {
		return ErrInvalidSignature
	}
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return ErrInvalidSignature
	}
	now := time.Now
	if v.Now != nil {
		now = v.Now
	}
	skew := now().Sub(time.Unix(sec, 0))
	if skew < 0 {
		skew = -skew
	}
	if skew > v.MaxSkew {
		return fmt.Errorf("%w: timestamp outside allowed window", ErrInvalidSignature)
	}
	got, err := hex.DecodeString(sig)
	if err != nil {
		return ErrInvalidSignature
	}
	if !hmac.Equal(got, mac(key, ts, body)) {
		return ErrInvalidSignature
	}
	return nil
}

// ParseKeys parses "kid1:secret1,kid2:secret2". Secrets must be at least 32 bytes.
func ParseKeys(s string) (map[string][]byte, error) {
	keys := map[string][]byte{}
	for entry := range strings.SplitSeq(s, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		kid, secret, ok := strings.Cut(entry, ":")
		if !ok || kid == "" || strings.ContainsAny(kid, ",= ") {
			return nil, errors.New("sigauth: keys must look like kid:secret")
		}
		if len(secret) < 32 {
			return nil, fmt.Errorf("sigauth: key %q is shorter than 32 bytes", kid)
		}
		keys[kid] = []byte(secret)
	}
	if len(keys) == 0 {
		return nil, errors.New("sigauth: no keys configured")
	}
	return keys, nil
}

func mac(key []byte, ts string, body []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(ts))
	h.Write([]byte("."))
	h.Write(body)
	return h.Sum(nil)
}
