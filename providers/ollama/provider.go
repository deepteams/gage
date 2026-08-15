// Package ollama implements a gage.Provider backed by a local Ollama server.
// By default it uses Ollama's native /api/chat NDJSON protocol; WithOpenAICompat
// switches to the OpenAI-compatible /v1/chat/completions endpoint.
package ollama

import (
	"strings"

	"github.com/deepteams/gage"
	"github.com/deepteams/gage/providers/openai"
	"github.com/deepteams/gage/providers/shared"
)

// DefaultBaseURL is the standard local Ollama address.
const DefaultBaseURL = "http://localhost:11434"

type config struct {
	baseURL      string
	defaultModel string
	http         *shared.Client
	openaiCompat bool
}

// Option configures the provider.
type Option func(*config)

// WithDefaultModel sets the model used when Request.Model is empty.
func WithDefaultModel(m string) Option {
	return func(c *config) { c.defaultModel = m }
}

// WithHTTPClient sets a shared HTTP client.
func WithHTTPClient(h *shared.Client) Option {
	return func(c *config) { c.http = h }
}

// WithOpenAICompat routes requests through Ollama's OpenAI-compatible endpoint
// instead of the native /api/chat protocol.
func WithOpenAICompat() Option {
	return func(c *config) { c.openaiCompat = true }
}

// New builds an Ollama provider. baseURL may be empty to use DefaultBaseURL.
// The returned provider also implements gage.ModelLister (native: /api/tags,
// OpenAI-compat: /v1/models).
func New(baseURL string, opts ...Option) gage.Provider {
	cfg := config{baseURL: DefaultBaseURL}
	if baseURL != "" {
		cfg.baseURL = strings.TrimRight(baseURL, "/")
	}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.openaiCompat {
		return &openai.ChatClient{
			ProviderName: "ollama",
			BaseURL:      cfg.baseURL + "/v1",
			DefaultModel: cfg.defaultModel,
			HTTP:         cfg.http,
		}
	}
	return &nativeProvider{cfg: cfg}
}
