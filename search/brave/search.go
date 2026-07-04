// Package brave implements gage.SearchProvider using the Brave Search API,
// which requires an API key (a free tier is available).
package brave

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/deepteams/gage"
)

// Endpoint is the Brave web search endpoint.
const Endpoint = "https://api.search.brave.com/res/v1/web/search"

// Provider is a Brave Search provider.
type Provider struct {
	APIKey   string
	Endpoint string
	HTTP     *http.Client
}

// New returns a Brave provider with the given API key.
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
	q := url.Values{"q": {query}, "count": {strconv.Itoa(limit)}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.endpoint()+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", p.APIKey)

	resp, err := p.http().Do(req)
	if err != nil {
		return nil, fmt.Errorf("brave: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode/100 != 2 {
		return nil, &gage.APIError{Provider: "brave", Status: resp.StatusCode, Body: string(body)}
	}
	var out struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("brave: parse: %w", err)
	}
	results := make([]gage.SearchResult, 0, len(out.Web.Results))
	for _, r := range out.Web.Results {
		if len(results) >= limit {
			break
		}
		results = append(results, gage.SearchResult{Title: r.Title, URL: r.URL, Snippet: r.Description})
	}
	return results, nil
}
