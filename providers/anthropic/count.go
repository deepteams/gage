package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/deepteams/gage"
)

// CountTokensPath is the suffix appended to the messages endpoint URL to reach
// the count_tokens endpoint ({base}/v1/messages/count_tokens).
const CountTokensPath = "/count_tokens"

var _ gage.TokenCounter = (*Client)(nil)

// CountTokens implements gage.TokenCounter via POST
// {base}/v1/messages/count_tokens. The request body is the same encoding
// Stream sends, minus the stream and max_tokens fields, with the same
// auth/version/beta headers.
func (c *Client) CountTokens(ctx context.Context, req gage.Request) (int, error) {
	b, structured, err := c.buildBodyMap(req)
	if err != nil {
		return 0, err
	}
	delete(b, "stream")
	delete(b, "max_tokens")
	body, err := json.Marshal(b)
	if err != nil {
		return 0, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+CountTokensPath, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("anthropic-version", Version)
	for k, v := range c.Headers {
		httpReq.Header.Set(k, v)
	}
	if c.Authorize != nil {
		if err := c.Authorize(ctx, httpReq); err != nil {
			return 0, err
		}
	}
	if structured {
		appendBeta(httpReq.Header, BetaStructuredOutputs)
	}

	resp, err := c.http().Do(httpReq)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		return 0, &gage.APIError{Provider: c.ProviderName, Status: resp.StatusCode, Body: string(respBody)}
	}

	var out struct {
		InputTokens int `json:"input_tokens"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, fmt.Errorf("%s: count_tokens: %w", c.ProviderName, err)
	}
	return out.InputTokens, nil
}
