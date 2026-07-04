// Package duckduckgo implements gage.SearchProvider against DuckDuckGo's HTML
// "lite" endpoint, which requires no API key. It parses the returned HTML; the
// markup is not a stable API and may change.
package duckduckgo

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/deepteams/gage"
)

// Endpoint is the DuckDuckGo HTML endpoint.
const Endpoint = "https://html.duckduckgo.com/html/"

// Provider is a keyless DuckDuckGo search provider.
type Provider struct {
	// Endpoint overrides the default (testing).
	Endpoint string
	// HTTP overrides the default client.
	HTTP *http.Client
	// UserAgent for requests.
	UserAgent string
}

// New returns a DuckDuckGo search provider.
func New() *Provider { return &Provider{} }

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

func (p *Provider) userAgent() string {
	if p.UserAgent != "" {
		return p.UserAgent
	}
	return "Mozilla/5.0 (compatible; gage/1.0)"
}

// Search implements gage.SearchProvider.
func (p *Provider) Search(ctx context.Context, query string, limit int) ([]gage.SearchResult, error) {
	if limit <= 0 {
		limit = 5
	}
	form := url.Values{"q": {query}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint(), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", p.userAgent())

	resp, err := p.http().Do(req)
	if err != nil {
		return nil, fmt.Errorf("duckduckgo: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode/100 != 2 {
		return nil, &gage.APIError{Provider: "duckduckgo", Status: resp.StatusCode, Body: string(body)}
	}
	return parseResults(string(body), limit), nil
}

var resultRe = regexp.MustCompile(`(?s)<a[^>]*class="result__a"[^>]*href="([^"]+)"[^>]*>(.*?)</a>.*?<a[^>]*class="result__snippet"[^>]*>(.*?)</a>`)

func parseResults(html string, limit int) []gage.SearchResult {
	matches := resultRe.FindAllStringSubmatch(html, -1)
	out := make([]gage.SearchResult, 0, len(matches))
	for _, m := range matches {
		if len(out) >= limit {
			break
		}
		out = append(out, gage.SearchResult{
			Title:   cleanText(m[2]),
			URL:     decodeURL(m[1]),
			Snippet: cleanText(m[3]),
		})
	}
	return out
}

var tagRe = regexp.MustCompile(`(?s)<[^>]+>`)

func cleanText(s string) string {
	s = tagRe.ReplaceAllString(s, "")
	r := strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'", "&#x27;", "'", "&nbsp;", " ")
	return strings.TrimSpace(r.Replace(s))
}

// decodeURL resolves DuckDuckGo's redirect wrapper (//duckduckgo.com/l/?uddg=...).
func decodeURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "//") {
		raw = "https:" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if target := u.Query().Get("uddg"); target != "" {
		return target
	}
	return raw
}
