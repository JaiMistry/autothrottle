package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
)

// CSRF derives a per-session token via HMAC rather than storing one —
// no extra session-keyed map to keep in sync, just a synchronizer token
// tied to the session token that's already there.
type CSRF struct {
	key []byte
}

func NewCSRF() (*CSRF, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return &CSRF{key: key}, nil
}

// TokenFor returns the CSRF token for a given session token, to embed
// in forms as a hidden field.
func (c *CSRF) TokenFor(sessionToken string) string {
	mac := hmac.New(sha256.New, c.key)
	mac.Write([]byte(sessionToken))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// Valid checks a submitted CSRF token against the expected one for
// this session, in constant time.
func (c *CSRF) Valid(sessionToken, submitted string) bool {
	if submitted == "" {
		return false
	}
	expected := c.TokenFor(sessionToken)
	return subtle.ConstantTimeCompare([]byte(expected), []byte(submitted)) == 1
}
