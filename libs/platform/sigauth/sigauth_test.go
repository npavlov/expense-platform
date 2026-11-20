package sigauth

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var (
	keyA = []byte(strings.Repeat("a", 32))
	keyB = []byte(strings.Repeat("b", 32))
	now  = time.Unix(1_800_000_000, 0)
)

func fixed(t time.Time) func() time.Time { return func() time.Time { return t } }

func TestSignVerify(t *testing.T) {
	body := []byte(`{"event_id":"1"}`)
	s := Signer{KeyID: "k1", Key: keyA, Now: fixed(now)}
	v := Verifier{Keys: map[string][]byte{"k1": keyA}, MaxSkew: 5 * time.Minute, Now: fixed(now.Add(time.Minute))}

	if err := v.Verify(s.Sign(body), body); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}

	cases := map[string]struct {
		header string
		body   []byte
		v      Verifier
	}{
		"tampered body":   {s.Sign(body), []byte(`{"event_id":"2"}`), v},
		"unknown key id":  {Signer{KeyID: "k9", Key: keyA, Now: fixed(now)}.Sign(body), body, v},
		"wrong key":       {Signer{KeyID: "k1", Key: keyB, Now: fixed(now)}.Sign(body), body, v},
		"too old":         {Signer{KeyID: "k1", Key: keyA, Now: fixed(now.Add(-10 * time.Minute))}.Sign(body), body, v},
		"from the future": {Signer{KeyID: "k1", Key: keyA, Now: fixed(now.Add(10 * time.Minute))}.Sign(body), body, v},
		"empty":           {"", body, v},
		"garbage":         {"t=abc,kid=k1,v1=zz", body, v},
		"missing mac":     {"t=1800000000,kid=k1", body, v},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if err := tc.v.Verify(tc.header, tc.body); !errors.Is(err, ErrInvalidSignature) {
				t.Fatalf("err = %v, want ErrInvalidSignature", err)
			}
		})
	}
}

func TestKeyRotation(t *testing.T) {
	body := []byte(`{}`)
	// During rotation the receiver trusts both keys.
	v := Verifier{Keys: map[string][]byte{"old": keyA, "new": keyB}, MaxSkew: time.Minute, Now: fixed(now)}
	for _, s := range []Signer{{KeyID: "old", Key: keyA, Now: fixed(now)}, {KeyID: "new", Key: keyB, Now: fixed(now)}} {
		if err := v.Verify(s.Sign(body), body); err != nil {
			t.Errorf("key %s rejected during rotation: %v", s.KeyID, err)
		}
	}
	// After the old key is removed, its signatures stop working.
	v.Keys = map[string][]byte{"new": keyB}
	if err := v.Verify(Signer{KeyID: "old", Key: keyA, Now: fixed(now)}.Sign(body), body); err == nil {
		t.Error("retired key still accepted")
	}
}

func TestParseKeys(t *testing.T) {
	keys, err := ParseKeys("k1:" + string(keyA) + ", k2:" + string(keyB))
	if err != nil || len(keys) != 2 {
		t.Fatalf("ParseKeys = %v, %v", keys, err)
	}
	for _, bad := range []string{"", "k1", "k1:short", ":" + string(keyA)} {
		if _, err := ParseKeys(bad); err == nil {
			t.Errorf("ParseKeys(%q) accepted", bad)
		}
	}
}

func FuzzVerify(f *testing.F) {
	f.Add("t=1800000000,kid=k1,v1=00", []byte("{}"))
	f.Add("", []byte(nil))
	v := Verifier{Keys: map[string][]byte{"k1": keyA}, MaxSkew: time.Minute, Now: fixed(now)}
	f.Fuzz(func(t *testing.T, header string, body []byte) {
		// Must never panic, and must never accept a header it did not sign.
		if err := v.Verify(header, body); err == nil {
			if header != (Signer{KeyID: "k1", Key: keyA, Now: fixed(now)}).Sign(body) {
				t.Fatalf("accepted forged header %q", header)
			}
		}
	})
}
