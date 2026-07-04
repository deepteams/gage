package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/deepteams/gage"
)

// Config describes an OAuth 2.0 + PKCE authorization-code client.
type Config struct {
	ClientID     string
	AuthorizeURL string
	TokenURL     string
	RedirectURI  string
	Scopes       []string
	// UserAgent is sent on token requests; some endpoints (Anthropic behind
	// Cloudflare) reject non-browser-looking clients.
	UserAgent string
	// ExtraAuthParams are appended to the authorization URL (e.g. Codex's
	// codex_cli_simplified_flow=true).
	ExtraAuthParams map[string]string
	// HTTP is the client used for token requests. If nil, http.DefaultClient.
	HTTP *http.Client
	// Now is injectable for tests; defaults to time.Now.
	Now func() time.Time
}

func (c *Config) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Config) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// AuthCodeURL builds the authorization URL the user must visit.
func (c *Config) AuthCodeURL(p PKCE) string {
	v := url.Values{}
	v.Set("response_type", "code")
	v.Set("client_id", c.ClientID)
	if c.RedirectURI != "" {
		v.Set("redirect_uri", c.RedirectURI)
	}
	if len(c.Scopes) > 0 {
		v.Set("scope", strings.Join(c.Scopes, " "))
	}
	v.Set("code_challenge", p.Challenge)
	v.Set("code_challenge_method", "S256")
	v.Set("state", p.State)
	for k, val := range c.ExtraAuthParams {
		v.Set(k, val)
	}
	return c.AuthorizeURL + "?" + v.Encode()
}

// tokenResponse is the standard OAuth token endpoint payload.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
	IDToken      string `json:"id_token"`
}

func (c *Config) toCredentials(tr tokenResponse) gage.Credentials {
	cr := gage.Credentials{
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		Extra:        map[string]string{},
	}
	if tr.ExpiresIn > 0 {
		cr.ExpiresAt = c.now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	}
	if tr.IDToken != "" {
		cr.Extra["id_token"] = tr.IDToken
	}
	if tr.Scope != "" {
		cr.Extra["scope"] = tr.Scope
	}
	if tr.TokenType != "" {
		cr.Extra["token_type"] = tr.TokenType
	}
	return cr
}

// Exchange swaps an authorization code for tokens (login flow).
func (c *Config) Exchange(ctx context.Context, code, verifier string) (gage.Credentials, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("client_id", c.ClientID)
	form.Set("code_verifier", verifier)
	if c.RedirectURI != "" {
		form.Set("redirect_uri", c.RedirectURI)
	}
	return c.postToken(ctx, form)
}

// Refresh exchanges a refresh token for a new access token.
func (c *Config) Refresh(ctx context.Context, refreshToken string) (gage.Credentials, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", c.ClientID)
	if len(c.Scopes) > 0 {
		form.Set("scope", strings.Join(c.Scopes, " "))
	}
	cr, err := c.postToken(ctx, form)
	if err != nil {
		return cr, err
	}
	// Endpoints often omit the refresh token on refresh; keep the old one.
	if cr.RefreshToken == "" {
		cr.RefreshToken = refreshToken
	}
	return cr, nil
}

func (c *Config) postToken(ctx context.Context, form url.Values) (gage.Credentials, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return gage.Credentials{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return gage.Credentials{}, fmt.Errorf("oauth: token request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return gage.Credentials{}, &gage.APIError{Provider: "oauth", Status: resp.StatusCode, Body: string(body)}
	}
	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return gage.Credentials{}, fmt.Errorf("oauth: parse token response: %w", err)
	}
	return c.toCredentials(tr), nil
}

// RefreshSkew is how long before expiry a token is proactively refreshed.
const RefreshSkew = 60 * time.Second

// TokenSource returns a valid access token, refreshing and persisting as
// needed. It is safe for concurrent use. Postprocess, if set, runs on freshly
// obtained credentials (e.g. to decode an account id) before they are saved.
type TokenSource struct {
	Config      *Config
	Store       gage.TokenStore
	Postprocess func(*gage.Credentials) error

	mu sync.Mutex
}

// Token returns current credentials, refreshing if expired within RefreshSkew.
func (s *TokenSource) Token(ctx context.Context) (gage.Credentials, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cr, err := s.Store.Load(ctx)
	if err != nil {
		return gage.Credentials{}, err
	}
	if !cr.Expired(RefreshSkew, s.Config.now()) && cr.AccessToken != "" {
		return cr, nil
	}
	if cr.RefreshToken == "" {
		return gage.Credentials{}, fmt.Errorf("oauth: token expired and no refresh token: %w", gage.ErrAuth)
	}
	return s.refreshLocked(ctx, cr)
}

// ForceRefresh refreshes unconditionally (used after a 401 from the API).
func (s *TokenSource) ForceRefresh(ctx context.Context) (gage.Credentials, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cr, err := s.Store.Load(ctx)
	if err != nil {
		return gage.Credentials{}, err
	}
	if cr.RefreshToken == "" {
		return gage.Credentials{}, fmt.Errorf("oauth: no refresh token: %w", gage.ErrAuth)
	}
	return s.refreshLocked(ctx, cr)
}

func (s *TokenSource) refreshLocked(ctx context.Context, old gage.Credentials) (gage.Credentials, error) {
	fresh, err := s.Config.Refresh(ctx, old.RefreshToken)
	if err != nil {
		return gage.Credentials{}, err
	}
	// Preserve account id and any extras not returned by the refresh.
	if fresh.AccountID == "" {
		fresh.AccountID = old.AccountID
	}
	for k, v := range old.Extra {
		if _, ok := fresh.Extra[k]; !ok {
			if fresh.Extra == nil {
				fresh.Extra = map[string]string{}
			}
			fresh.Extra[k] = v
		}
	}
	if s.Postprocess != nil {
		if err := s.Postprocess(&fresh); err != nil {
			return gage.Credentials{}, err
		}
	}
	if err := s.Store.Save(ctx, fresh); err != nil {
		return gage.Credentials{}, err
	}
	return fresh, nil
}
