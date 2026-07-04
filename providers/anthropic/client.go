// Package anthropic implements the Anthropic Messages API wire format: the
// request encoder, the SSE stream pump, and a gage.Provider that authenticates
// with a plain API key. The claudecode provider reuses the same Client with an
// OAuth Authorize hook and its system-prompt spoof.
package anthropic

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/deepteams/gage"
	"github.com/deepteams/gage/providers/shared"
)

const (
	// DefaultBaseURL is the public Anthropic API root.
	DefaultBaseURL = "https://api.anthropic.com"
	// MessagesPath is the Messages endpoint path appended to the base URL.
	MessagesPath = "/v1/messages"
	// Version is the required anthropic-version header value.
	Version = "2023-06-01"
	// DefaultMaxTokens is applied when the request does not set MaxTokens
	// (the Messages API requires max_tokens).
	DefaultMaxTokens = 8192
	// BetaStructuredOutputs is the anthropic-beta flag enabling the
	// output_format parameter. Beta header values are subject to change while
	// the feature is in beta; keep it here so it is easy to bump.
	BetaStructuredOutputs = "structured-outputs-2025-11-13"
)

var defaultHTTP = shared.NewClient("gage/anthropic")

// Client implements gage.Provider against the Anthropic Messages API. It is
// the reusable wire layer: the API-key provider built by New and the
// claudecode OAuth provider both configure one.
type Client struct {
	// ProviderName is reported by Name() and used in errors.
	ProviderName string
	// URL is the full messages endpoint.
	URL string
	// DefaultModel is used when Request.Model is empty. If both are empty,
	// Stream fails.
	DefaultModel string
	// MaxTokens is the default max_tokens when the request sets none. Zero
	// means DefaultMaxTokens.
	MaxTokens int
	// Authorize sets auth headers per attempt (x-api-key or an OAuth bearer).
	Authorize func(ctx context.Context, req *http.Request) error
	// Headers are static extra headers added to every request.
	Headers map[string]string
	// SystemPrefix, when set, is inserted as the first system block ahead of
	// the request's system prompt (the claudecode spoof).
	SystemPrefix string
	// DisableResponseFormat makes Stream fail with gage.ErrUnsupported when a
	// structured ResponseFormat is requested (claudecode's spoofed backend is
	// not poked with beta parameters).
	DisableResponseFormat bool
	// HTTP is the shared retrying client. If nil, a package default is used.
	HTTP *shared.Client
}

// Config configures the API-key Anthropic provider built by New.
type Config struct {
	// APIKey is sent as x-api-key. Required.
	APIKey string
	// BaseURL overrides DefaultBaseURL (testing, proxies). MessagesPath is
	// appended.
	BaseURL string
	// Model is used when Request.Model is empty. Optional; if both are empty
	// Stream fails.
	Model string
	// MaxTokens is the default max_tokens; zero means DefaultMaxTokens.
	MaxTokens int
	// HTTP is the shared retrying client; nil uses a package default.
	HTTP *shared.Client
}

// New builds an Anthropic provider that authenticates with cfg.APIKey.
func New(cfg Config) gage.Provider {
	base := cfg.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	return &Client{
		ProviderName: "anthropic",
		URL:          strings.TrimRight(base, "/") + MessagesPath,
		DefaultModel: cfg.Model,
		MaxTokens:    cfg.MaxTokens,
		HTTP:         cfg.HTTP,
		Authorize: func(_ context.Context, req *http.Request) error {
			if cfg.APIKey == "" {
				return fmt.Errorf("anthropic: %w: missing API key", gage.ErrAuth)
			}
			req.Header.Set("x-api-key", cfg.APIKey)
			return nil
		},
	}
}

// Name implements gage.Provider.
func (c *Client) Name() string { return c.ProviderName }

func (c *Client) http() *shared.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return defaultHTTP
}

// Stream implements gage.Provider.
func (c *Client) Stream(ctx context.Context, req gage.Request) (<-chan gage.Event, error) {
	body, structured, err := c.buildBody(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("anthropic-version", Version)
	for k, v := range c.Headers {
		httpReq.Header.Set(k, v)
	}
	if c.Authorize != nil {
		if err := c.Authorize(ctx, httpReq); err != nil {
			return nil, err
		}
	}
	if structured {
		appendBeta(httpReq.Header, BetaStructuredOutputs)
	}

	resp, err := c.http().Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		return nil, &gage.APIError{Provider: c.ProviderName, Status: resp.StatusCode, Body: string(b)}
	}

	out := make(chan gage.Event)
	go c.pump(ctx, resp, out)
	return out, nil
}

// appendBeta merges a beta flag into an existing anthropic-beta header.
func appendBeta(h http.Header, beta string) {
	if cur := h.Get("anthropic-beta"); cur != "" {
		h.Set("anthropic-beta", cur+","+beta)
		return
	}
	h.Set("anthropic-beta", beta)
}
