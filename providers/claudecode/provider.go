package claudecode

import (
	"context"
	"errors"

	"github.com/deepteams/gage"
	"github.com/deepteams/gage/providers/anthropic"
	"github.com/deepteams/gage/providers/shared"
	"github.com/deepteams/gage/providers/shared/oauth"
)

// DefaultModel is used when Request.Model is empty.
const DefaultModel = "claude-sonnet-4-5"

// DefaultMaxTokens is applied when the request does not set MaxTokens (the
// Messages API requires it).
const DefaultMaxTokens = 8192

type provider struct {
	client *anthropic.Client
	ts     *oauth.TokenSource
}

// Option configures the Claude Code provider.
type Option func(*provider)

// WithDefaultModel overrides the default model.
func WithDefaultModel(m string) Option { return func(p *provider) { p.client.DefaultModel = m } }

// WithMaxTokens overrides the default max tokens.
func WithMaxTokens(n int) Option { return func(p *provider) { p.client.MaxTokens = n } }

// WithHTTPClient sets a shared HTTP client.
func WithHTTPClient(h *shared.Client) Option { return func(p *provider) { p.client.HTTP = h } }

// WithMessagesURL overrides the endpoint (testing).
func WithMessagesURL(u string) Option { return func(p *provider) { p.client.URL = u } }

// New builds a Claude Code provider backed by the given TokenStore, which must
// already hold valid credentials (obtained via Login). The provider refreshes
// them transparently. console selects the console OAuth host for refreshes.
//
// The provider is the anthropic Messages wire client configured with the
// Claude Code OAuth authorizer and the mandatory system-prompt spoof.
// Structured output (ResponseFormat) is unsupported: the spoofed backend is
// not poked with beta parameters.
func New(store gage.TokenStore, console bool, opts ...Option) gage.Provider {
	ts := &oauth.TokenSource{Config: OAuthConfig(console), Store: store}
	p := &provider{
		client: &anthropic.Client{
			ProviderName:          "claudecode",
			URL:                   MessagesURL,
			DefaultModel:          DefaultModel,
			MaxTokens:             DefaultMaxTokens,
			Authorize:             authorizer(ts),
			SystemPrefix:          SystemSpoof,
			DisableResponseFormat: true,
		},
		ts: ts,
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

func (p *provider) Name() string { return "claudecode" }

// Stream implements gage.Provider with a single auth-refresh retry on 401.
func (p *provider) Stream(ctx context.Context, req gage.Request) (<-chan gage.Event, error) {
	ch, err := p.client.Stream(ctx, req)
	if err != nil {
		if apiErr, ok := errors.AsType[*gage.APIError](err); ok && apiErr.Status == 401 {
			if _, rerr := p.ts.ForceRefresh(ctx); rerr == nil {
				return p.client.Stream(ctx, req)
			}
		}
	}
	return ch, err
}
