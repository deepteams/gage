// Package gemini implements a native Google Gemini provider over the
// generativelanguage.googleapis.com REST API. It streams generations via the
// SSE form of streamGenerateContent, authenticates with a plain API key
// (x-goog-api-key), and additionally implements the optional gage.TokenCounter
// (countTokens) and gage.ModelLister (models list) capabilities.
package gemini

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

// DefaultBaseURL is the public Gemini API root (v1beta carries the current
// feature surface: thinking, JSON schema output, function calling config).
const DefaultBaseURL = "https://generativelanguage.googleapis.com/v1beta"

var defaultHTTP = shared.NewClient("gage/gemini")

// Config configures the Gemini provider built by New.
type Config struct {
	// APIKey is sent as x-goog-api-key. Required for any request.
	APIKey string
	// Model is used when Request.Model is empty. Optional; if both are empty
	// Stream fails.
	Model string
	// BaseURL overrides DefaultBaseURL (testing, proxies).
	BaseURL string
	// HTTPClient is the shared retrying client; nil uses a package default.
	// Overridable for tests.
	HTTPClient *shared.Client
	// ProviderName is reported by Name() and used in errors. Defaults to
	// "gemini".
	ProviderName string
}

// Client implements gage.Provider (plus gage.TokenCounter and
// gage.ModelLister) against the Gemini API.
type Client struct {
	// ProviderName is reported by Name() and used in errors.
	ProviderName string
	// BaseURL is the API root, without a trailing slash.
	BaseURL string
	// APIKey is sent as x-goog-api-key.
	APIKey string
	// DefaultModel is used when Request.Model is empty.
	DefaultModel string
	// HTTP is the shared retrying client. If nil, a package default is used.
	HTTP *shared.Client
}

var (
	_ gage.Provider     = (*Client)(nil)
	_ gage.TokenCounter = (*Client)(nil)
	_ gage.ModelLister  = (*Client)(nil)
)

// New builds a Gemini provider from cfg, applying defaults.
func New(cfg Config) *Client {
	base := cfg.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	name := cfg.ProviderName
	if name == "" {
		name = "gemini"
	}
	return &Client{
		ProviderName: name,
		BaseURL:      strings.TrimRight(base, "/"),
		APIKey:       cfg.APIKey,
		DefaultModel: cfg.Model,
		HTTP:         cfg.HTTPClient,
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

// model resolves the effective model id, stripping an optional "models/"
// prefix so it can be spliced into the URL path.
func (c *Client) model(m string) (string, error) {
	if m == "" {
		m = c.DefaultModel
	}
	if m == "" {
		return "", fmt.Errorf("%s: no model specified", c.ProviderName)
	}
	return strings.TrimPrefix(m, "models/"), nil
}

// authorize sets the API-key header, failing with ErrAuth when the key is
// missing.
func (c *Client) authorize(req *http.Request) error {
	if c.APIKey == "" {
		return fmt.Errorf("%s: %w: missing API key", c.ProviderName, gage.ErrAuth)
	}
	req.Header.Set("x-goog-api-key", c.APIKey)
	return nil
}

// Stream implements gage.Provider.
func (c *Client) Stream(ctx context.Context, req gage.Request) (<-chan gage.Event, error) {
	model, err := c.model(req.Model)
	if err != nil {
		return nil, err
	}
	body, err := c.buildBody(req)
	if err != nil {
		return nil, err
	}
	url := c.BaseURL + "/models/" + model + ":streamGenerateContent?alt=sse"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if err := c.authorize(httpReq); err != nil {
		return nil, err
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
