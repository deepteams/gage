// Package codex implements a gage.Provider that uses a ChatGPT/Codex plan via
// OpenAI's OAuth (PKCE) flow and the Responses API backend. The endpoints and
// client id are the public ones used by the Codex CLI; they are undocumented
// and may change, and their use is subject to OpenAI's terms.
package codex

import (
	"context"
	"fmt"
	"net/http"

	"github.com/deepteams/gage"
	"github.com/deepteams/gage/providers/shared/oauth"
)

// Public Codex CLI OAuth parameters.
const (
	ClientID     = "app_EMoamEEZ73f0CkXaXp7hrann"
	AuthorizeURL = "https://auth.openai.com/oauth/authorize"
	TokenURL     = "https://auth.openai.com/oauth/token"
	RedirectURI  = "http://localhost:1455/auth/callback"
	// ResponsesURL is the Codex backend Responses endpoint.
	ResponsesURL = "https://chatgpt.com/backend-api/codex/responses"
)

// Scopes requested during login.
var Scopes = []string{"openid", "profile", "email", "offline_access"}

// OAuthConfig returns the oauth.Config for the Codex flow.
func OAuthConfig() *oauth.Config {
	return &oauth.Config{
		ClientID:     ClientID,
		AuthorizeURL: AuthorizeURL,
		TokenURL:     TokenURL,
		RedirectURI:  RedirectURI,
		Scopes:       Scopes,
		ExtraAuthParams: map[string]string{
			"id_token_add_organizations": "true",
			"codex_cli_simplified_flow":  "true",
		},
	}
}

// Login runs the local-server login flow and stores credentials (with the
// account id decoded) in store. open is called with the authorization URL.
func Login(ctx context.Context, store gage.TokenStore, open func(url string)) (gage.Credentials, error) {
	cfg := OAuthConfig()
	cr, err := oauth.LocalLogin(ctx, cfg, store, "localhost:1455", open)
	if err != nil {
		return gage.Credentials{}, err
	}
	if err := decorateAccountID(&cr); err != nil {
		return cr, err
	}
	return cr, store.Save(ctx, cr)
}

// decorateAccountID fills Credentials.AccountID from the access/id token claims.
func decorateAccountID(cr *gage.Credentials) error {
	if cr.AccountID != "" {
		return nil
	}
	for _, tok := range []string{cr.AccessToken, cr.Extra["id_token"]} {
		if tok == "" {
			continue
		}
		claims, err := oauth.DecodeJWTClaims(tok)
		if err != nil {
			continue
		}
		if id := accountIDFromClaims(claims); id != "" {
			cr.AccountID = id
			return nil
		}
	}
	return nil
}

// accountIDFromClaims looks for the chatgpt_account_id in the known locations.
func accountIDFromClaims(claims map[string]any) string {
	if id, ok := claims["chatgpt_account_id"].(string); ok && id != "" {
		return id
	}
	// Nested under the auth namespace: {"https://api.openai.com/auth": {...}}.
	if auth, ok := claims["https://api.openai.com/auth"].(map[string]any); ok {
		if id, ok := auth["chatgpt_account_id"].(string); ok && id != "" {
			return id
		}
		if orgs, ok := auth["organizations"].([]any); ok && len(orgs) > 0 {
			if org, ok := orgs[0].(map[string]any); ok {
				if id, ok := org["id"].(string); ok {
					return id
				}
			}
		}
	}
	return ""
}

// authorizer builds the per-request auth decorator for the Responses client.
func authorizer(ts *oauth.TokenSource) func(ctx context.Context, req *http.Request) error {
	return func(ctx context.Context, req *http.Request) error {
		cr, err := ts.Token(ctx)
		if err != nil {
			return fmt.Errorf("codex: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+cr.AccessToken)
		if cr.AccountID != "" {
			req.Header.Set("ChatGPT-Account-Id", cr.AccountID)
		}
		req.Header.Set("OpenAI-Beta", "responses=experimental")
		req.Header.Set("originator", "codex_cli_go")
		return nil
	}
}
