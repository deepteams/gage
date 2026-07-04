package codex

import (
	"context"
	"errors"

	"github.com/deepteams/gage"
	"github.com/deepteams/gage/providers/openai"
	"github.com/deepteams/gage/providers/shared"
	"github.com/deepteams/gage/providers/shared/oauth"
)

// DefaultModel is used when Request.Model is empty.
const DefaultModel = "gpt-5-codex"

type provider struct {
	client *openai.ResponsesClient
	ts     *oauth.TokenSource
}

// Option configures the Codex provider.
type Option func(*provider)

// WithDefaultModel overrides the default model.
func WithDefaultModel(m string) Option {
	return func(p *provider) { p.client.DefaultModel = m }
}

// WithHTTPClient sets a shared HTTP client.
func WithHTTPClient(h *shared.Client) Option {
	return func(p *provider) { p.client.HTTP = h }
}

// WithResponsesURL overrides the backend endpoint (testing).
func WithResponsesURL(u string) Option {
	return func(p *provider) { p.client.URL = u }
}

// New builds a Codex provider backed by the given TokenStore. The store must
// already hold valid credentials (obtained via Login); the provider refreshes
// them transparently.
func New(store gage.TokenStore, opts ...Option) gage.Provider {
	ts := &oauth.TokenSource{
		Config:      OAuthConfig(),
		Store:       store,
		Postprocess: decorateAccountID,
	}
	p := &provider{
		client: &openai.ResponsesClient{
			ProviderName: "codex",
			URL:          ResponsesURL,
			DefaultModel: DefaultModel,
			Store:        false,
			Authorize:    authorizer(ts),
		},
		ts: ts,
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

func (p *provider) Name() string { return "codex" }

// Stream implements gage.Provider, retrying once after a forced token refresh
// when the backend rejects the token.
func (p *provider) Stream(ctx context.Context, req gage.Request) (<-chan gage.Event, error) {
	ch, err := p.client.Stream(ctx, req)
	if err != nil && isAuthError(err) {
		if _, rerr := p.ts.ForceRefresh(ctx); rerr == nil {
			return p.client.Stream(ctx, req)
		}
	}
	return ch, err
}

func isAuthError(err error) bool {
	if apiErr, ok := errors.AsType[*gage.APIError](err); ok {
		return apiErr.Status == 401
	}
	return false
}
