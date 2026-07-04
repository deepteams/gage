package gage

import (
	"context"
	"time"
)

// Credentials holds OAuth tokens (and provider-specific extras) for a premium
// provider such as Codex or Claude Code.
type Credentials struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitzero"`
	// AccountID is a provider-specific account identifier (e.g. Codex's
	// chatgpt_account_id). Empty when not applicable.
	AccountID string `json:"account_id,omitempty"`
	// Extra carries any additional provider fields (scopes, token type, ...).
	Extra map[string]string `json:"extra,omitempty"`
}

// Expired reports whether the access token is expired (or will be within skew).
// A zero ExpiresAt is treated as "never expires".
func (c Credentials) Expired(skew time.Duration, now time.Time) bool {
	if c.ExpiresAt.IsZero() {
		return false
	}
	return !now.Add(skew).Before(c.ExpiresAt)
}

// TokenStore persists and retrieves Credentials. The consumer implements it
// (database, keychain, encrypted file, ...); gage provides an optional file
// store in providers/shared/oauth as a convenience. Implementations must be
// safe for concurrent use.
type TokenStore interface {
	// Load returns the stored credentials. It should return a wrapped ErrAuth
	// when no credentials are available.
	Load(ctx context.Context) (Credentials, error)
	// Save persists refreshed credentials.
	Save(ctx context.Context, c Credentials) error
}
