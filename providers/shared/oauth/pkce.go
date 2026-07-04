// Package oauth provides the PKCE flow helpers, an in-memory/file TokenStore,
// and a refreshing token source shared by the Codex and Claude Code providers.
package oauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

// PKCE holds a generated code verifier/challenge pair plus a state value.
type PKCE struct {
	Verifier  string
	Challenge string
	State     string
}

// GeneratePKCE creates a fresh PKCE pair using the S256 method.
func GeneratePKCE() (PKCE, error) {
	verifier, err := randomURLSafe(32)
	if err != nil {
		return PKCE{}, err
	}
	state, err := randomURLSafe(32)
	if err != nil {
		return PKCE{}, err
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	return PKCE{Verifier: verifier, Challenge: challenge, State: state}, nil
}

func randomURLSafe(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
