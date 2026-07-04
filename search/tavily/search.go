// Package tavily implements gage.SearchProvider using the Tavily API, a search
// service optimized for LLMs. It requires an API key.
package tavily

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/deepteams/gage"
)

// Endpoint is the Tavily search endpoint.
const Endpoint = "https://api.tavily.com/search"

// Provider is a Tavily search provider.
type Provider struct {
	APIKey   string
	Endpoint string
	HTTP     *http.Client
}

// New returns a Tavily provider with the given API key.
func New(apiKey string) *Provider { return &Provider{APIKey: apiKey} }

func (p *Provider) endpoint() string {
	if p.Endpoint != "" {
		return p.Endpoint
	}
	return Endpoint
}

func (p *Provider) http() *http.Client {
	if p.HTTP != nil {
		return p.HTTP
	}
	return &http.Client{Timeout: 20 * time.Second}
}

// Search implements gage.SearchProvider.
func (p *Provider) Search(ctx context.Context, query string, limit int) ([]gage.SearchResult, error) {
	if limit <= 0 {
		limit = 5
	}
	reqBody, _ := json.Marshal(map[string]any{
		"query":       query,
		"max_results": limit,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint(), bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.APIKey)

	resp, err := p.http().Do(req)
	if err != nil {
		return nil, fmt.Errorf("tavily: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode/100 != 2 {
		return nil, &gage.APIError{Provider: "tavily", Status: resp.StatusCode, Body: string(body)}
	}
	var out struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("tavily: parse: %w", err)
	}
	results := make([]gage.SearchResult, 0, len(out.Results))
	for _, r := range out.Results {
		results = append(results, gage.SearchResult{Title: r.Title, URL: r.URL, Snippet: r.Content})
	}
	return results, nil
}
