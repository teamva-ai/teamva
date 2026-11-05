package store

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

const csrfDomain = "teamva:csrf:v1:"

func newCSRFKey(securityPepper string) ([]byte, error) {
	if securityPepper != "" {
		digest := sha256.Sum256([]byte("teamva:csrf-key:v1:" + securityPepper))
		return digest[:], nil
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return key, nil
}

func csrfTokenForSession(key []byte, sessionID string) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(csrfDomain))
	_, _ = mac.Write([]byte(sessionID))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
