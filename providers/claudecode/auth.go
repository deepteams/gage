// Package claudecode implements a gage.Provider that uses a Claude subscription
// via Anthropic's OAuth (PKCE) flow against the Messages API, presenting itself
// as the Claude Code CLI. The client id and endpoints are the public ones used
// by Claude Code; they are undocumented and their use is subject to Anthropic's
// terms.
package claudecode

import (
	"context"
	"fmt"
	"net/http"

	"github.com/deepteams/gage"
	"github.com/deepteams/gage/providers/shared/oauth"
)

// Public Claude Code OAuth parameters.
const (
	ClientID     = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	AuthorizeURL = "https://claude.ai/oauth/authorize"
	// AuthorizeURLConsole is the alternate authorize host for console accounts.
	AuthorizeURLConsole = "https://console.anthropic.com/oauth/authorize"
	TokenURL            = "https://console.anthropic.com/v1/oauth/token"
	RedirectURI         = "https://console.anthropic.com/oauth/code/callback"
	// MessagesURL is the Anthropic Messages endpoint.
	MessagesURL = "https://api.anthropic.com/v1/messages"
	// AnthropicVersion is the required API version header.
	AnthropicVersion = "2023-06-01"
	// AnthropicBeta enables OAuth and the Claude Code surface.
	AnthropicBeta = "oauth-2025-04-20,claude-code-20250219"
	// SystemSpoof is the required first system block.
	SystemSpoof = "You are Claude Code, Anthropic's official CLI for Claude."
	// DefaultUserAgent avoids Cloudflare challenges on the token endpoint.
	DefaultUserAgent = "claude-cli/1.0 (external, cli)"
)

// Scopes requested during login.
var Scopes = []string{"org:create_api_key", "user:profile", "user:inference"}

// OAuthConfig returns the oauth.Config for the Claude Code flow. If console is
// true, the console authorize host is used.
func OAuthConfig(console bool) *oauth.Config {
	authURL := AuthorizeURL
	if console {
		authURL = AuthorizeURLConsole
	}
	return &oauth.Config{
		ClientID:     ClientID,
		AuthorizeURL: authURL,
		TokenURL:     TokenURL,
		RedirectURI:  RedirectURI,
		Scopes:       Scopes,
		UserAgent:    DefaultUserAgent,
	}
}

// Login returns the authorization URL and a completion function using the
// copy-paste redirect flow. The user opens authURL, authorizes, then passes the
// returned "code#state" value to complete.
func Login(console bool) (authURL string, complete func(ctx context.Context, store gage.TokenStore, pasted string) (gage.Credentials, error), err error) {
	return oauth.ManualLogin(OAuthConfig(console))
}

// authorizer builds the per-request auth decorator for the Messages client.
func authorizer(ts *oauth.TokenSource) func(ctx context.Context, req *http.Request) error {
	return func(ctx context.Context, req *http.Request) error {
		cr, err := ts.Token(ctx)
		if err != nil {
			return fmt.Errorf("claudecode: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+cr.AccessToken)
		req.Header.Set("anthropic-version", AnthropicVersion)
		req.Header.Set("anthropic-beta", AnthropicBeta)
		req.Header.Set("User-Agent", DefaultUserAgent)
		return nil
	}
}
