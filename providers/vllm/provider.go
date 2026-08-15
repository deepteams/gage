// Package vllm implements a gage.Provider backed by a vLLM server, which
// exposes the OpenAI Chat Completions protocol at <baseURL>/v1.
package vllm

import (
	"strings"

	"github.com/deepteams/gage"
	"github.com/deepteams/gage/providers/openai"
	"github.com/deepteams/gage/providers/shared"
)

// Option configures the provider.
type Option func(*openai.ChatClient)

// WithAPIKey sets a bearer token (vLLM may be deployed with --api-key).
func WithAPIKey(k string) Option {
	return func(c *openai.ChatClient) { c.APIKey = k }
}

// WithDefaultModel sets the model used when Request.Model is empty.
func WithDefaultModel(m string) Option {
	return func(c *openai.ChatClient) { c.DefaultModel = m }
}

// WithHTTPClient sets a shared HTTP client.
func WithHTTPClient(h *shared.Client) Option {
	return func(c *openai.ChatClient) { c.HTTP = h }
}

// New builds a vLLM provider. baseURL is the server root, e.g.
// "http://localhost:8000" (the "/v1" suffix is added automatically if absent).
// The returned provider also implements gage.ModelLister (GET /v1/models).
func New(baseURL string, opts ...Option) gage.Provider {
	c := &openai.ChatClient{
		ProviderName: "vllm",
		BaseURL:      normalizeBaseURL(baseURL),
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

func normalizeBaseURL(u string) string {
	u = strings.TrimRight(u, "/")
	if !strings.HasSuffix(u, "/v1") {
		u += "/v1"
	}
	return u
}
