// Package auth implements admin login, server-side sessions, and the
// middleware that guards the gateway's API routes.
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"strings"
)

// mac returns the base64url HMAC-SHA256 of id under secret.
func mac(secret []byte, id string) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(id))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// signValue binds an opaque session id to the server secret so a forged or
// tampered cookie is rejected before any database lookup.
func signValue(secret []byte, id string) string {
	return id + "." + mac(secret, id)
}

// parseValue verifies the signature and returns the session id.
func parseValue(secret []byte, value string) (string, bool) {
	id, sig, found := strings.Cut(value, ".")
	if !found || id == "" || sig == "" {
		return "", false
	}
	if subtle.ConstantTimeCompare([]byte(sig), []byte(mac(secret, id))) != 1 {
		return "", false
	}
	return id, true
}
