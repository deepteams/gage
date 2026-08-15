// Package openrouter implements a gage.Provider backed by the OpenRouter API
// (https://openrouter.ai), which speaks the OpenAI Chat Completions protocol.
package openrouter

import (
	"github.com/deepteams/gage"
	"github.com/deepteams/gage/providers/openai"
	"github.com/deepteams/gage/providers/shared"
)

// DefaultBaseURL is the OpenRouter API root.
const DefaultBaseURL = "https://openrouter.ai/api/v1"

// Option configures the provider.
type Option func(*openai.ChatClient)

// WithBaseURL overrides the API root (rarely needed).
func WithBaseURL(u string) Option {
	return func(c *openai.ChatClient) { c.BaseURL = u }
}

// WithDefaultModel sets the model used when Request.Model is empty.
func WithDefaultModel(m string) Option {
	return func(c *openai.ChatClient) { c.DefaultModel = m }
}

// WithHTTPClient sets a shared HTTP client (for custom timeouts/retries).
func WithHTTPClient(h *shared.Client) Option {
	return func(c *openai.ChatClient) { c.HTTP = h }
}

// WithReferer sets the optional HTTP-Referer / X-Title attribution headers that
// OpenRouter uses for app ranking.
func WithReferer(referer, title string) Option {
	return func(c *openai.ChatClient) {
		if c.Headers == nil {
			c.Headers = map[string]string{}
		}
		if referer != "" {
			c.Headers["HTTP-Referer"] = referer
		}
		if title != "" {
			c.Headers["X-Title"] = title
		}
	}
}

// New builds an OpenRouter provider. apiKey is required. The returned provider
// also implements gage.ModelLister (GET /models, including context sizes).
func New(apiKey string, opts ...Option) gage.Provider {
	c := &openai.ChatClient{
		ProviderName: "openrouter",
		BaseURL:      DefaultBaseURL,
		APIKey:       apiKey,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}
