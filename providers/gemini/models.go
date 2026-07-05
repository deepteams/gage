package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/deepteams/gage"
)

// CountTokens implements gage.TokenCounter via the countTokens endpoint. The
// count covers the encoded contents plus systemInstruction and tools.
func (c *Client) CountTokens(ctx context.Context, req gage.Request) (int, error) {
	model, err := c.model(req.Model)
	if err != nil {
		return 0, err
	}
	body, err := c.countBody(req)
	if err != nil {
		return 0, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+"/models/"+model+":countTokens", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if err := c.authorize(httpReq); err != nil {
		return 0, err
	}
	var out struct {
		TotalTokens int `json:"totalTokens"`
	}
	if err := c.doJSON(httpReq, &out); err != nil {
		return 0, err
	}
	return out.TotalTokens, nil
}

// Models implements gage.ModelLister via the models list endpoint, following
// pagination until exhausted.
func (c *Client) Models(ctx context.Context) ([]gage.ModelInfo, error) {
	var infos []gage.ModelInfo
	pageToken := ""
	for {
		u := c.BaseURL + "/models"
		if pageToken != "" {
			u += "?pageToken=" + url.QueryEscape(pageToken)
		}
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		if err := c.authorize(httpReq); err != nil {
			return nil, err
		}
		var out struct {
			Models []struct {
				Name             string `json:"name"`
				DisplayName      string `json:"displayName"`
				InputTokenLimit  int    `json:"inputTokenLimit"`
				OutputTokenLimit int    `json:"outputTokenLimit"`
			} `json:"models"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := c.doJSON(httpReq, &out); err != nil {
			return nil, err
		}
		for _, m := range out.Models {
			infos = append(infos, gage.ModelInfo{
				ID:              strings.TrimPrefix(m.Name, "models/"),
				Name:            m.DisplayName,
				ContextWindow:   m.InputTokenLimit,
				MaxOutputTokens: m.OutputTokenLimit,
			})
		}
		if out.NextPageToken == "" {
			return infos, nil
		}
		pageToken = out.NextPageToken
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
	return json.NewDecoder(resp.Body).Decode(v)
}
