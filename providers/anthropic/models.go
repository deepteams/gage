package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/deepteams/gage"
)

// ModelsPath is the models list endpoint path appended to the base URL.
const ModelsPath = "/v1/models"

var _ gage.ModelLister = (*Client)(nil)

// Models implements gage.ModelLister via GET {base}/v1/models, following
// pagination until exhausted. The base URL is derived from the messages
// endpoint URL, so a Client pointed at a non-standard endpoint (one that does
// not end in MessagesPath) fails rather than guessing.
func (c *Client) Models(ctx context.Context) ([]gage.ModelInfo, error) {
	base, ok := strings.CutSuffix(c.URL, MessagesPath)
	if !ok {
		return nil, fmt.Errorf("%s: models: cannot derive models endpoint from %q", c.ProviderName, c.URL)
	}

	var infos []gage.ModelInfo
	afterID := ""
	for {
		u := base + ModelsPath + "?limit=1000"
		if afterID != "" {
			u += "&after_id=" + url.QueryEscape(afterID)
		}
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Accept", "application/json")
		httpReq.Header.Set("anthropic-version", Version)
		for k, v := range c.Headers {
			httpReq.Header.Set(k, v)
		}
		if c.Authorize != nil {
			if err := c.Authorize(ctx, httpReq); err != nil {
				return nil, err
			}
		}

		var out struct {
			Data []struct {
				ID          string `json:"id"`
				DisplayName string `json:"display_name"`
			} `json:"data"`
			HasMore bool   `json:"has_more"`
			LastID  string `json:"last_id"`
		}
		if err := c.doJSON(httpReq, &out); err != nil {
			return nil, err
		}
		for _, m := range out.Data {
			infos = append(infos, gage.ModelInfo{ID: m.ID, Name: m.DisplayName})
		}
		if !out.HasMore || out.LastID == "" {
			return infos, nil
		}
		afterID = out.LastID
	}
}

// doJSON runs a non-streaming request through the shared retrying client,
// turning non-2xx responses into *gage.APIError and decoding the JSON body
// into v.
func (c *Client) doJSON(req *http.Request, v any) error {
	resp, err := c.http().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		return &gage.APIError{Provider: c.ProviderName, Status: resp.StatusCode, Body: string(b)}
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		return fmt.Errorf("%s: %w", c.ProviderName, err)
	}
	return nil
}
