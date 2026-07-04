package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/deepteams/gage"
	"github.com/deepteams/gage/internal/jsonschema"
)

// WebConfig configures the web tools.
type WebConfig struct {
	// Search backs the web_search tool. If nil, web_search is not returned by
	// NewWebTools.
	Search gage.SearchProvider
	// HTTP is the client used by web_fetch (default: 30s timeout client).
	HTTP *http.Client
	// MaxFetchBytes caps web_fetch output (default 512 KiB).
	MaxFetchBytes int64
	// UserAgent for web_fetch requests.
	UserAgent string
	// AllowPrivateHosts permits localhost/private/link-local targets. Leave
	// false when web_fetch is exposed to untrusted model input.
	AllowPrivateHosts bool
}

// NewWebTools returns web_fetch and (if a SearchProvider is set) web_search.
func NewWebTools(cfg WebConfig) []gage.Tool {
	tools := []gage.Tool{&webFetchTool{cfg}}
	if cfg.Search != nil {
		tools = append(tools, &webSearchTool{cfg.Search})
	}
	return tools
}

func (c WebConfig) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (c WebConfig) maxFetch() int64 {
	if c.MaxFetchBytes > 0 {
		return c.MaxFetchBytes
	}
	return 512 << 10
}

// ---- web_fetch ----

type webFetchTool struct{ cfg WebConfig }

func (t *webFetchTool) Name() string { return "web_fetch" }
func (t *webFetchTool) Description() string {
	return "Fetch a URL over HTTP(S) and return its textual content (HTML tags stripped)."
}
func (t *webFetchTool) Schema() gage.JSONSchema {
	return jsonschema.Object(map[string]jsonschema.Property{
		"url": jsonschema.Str("The absolute http(s) URL to fetch."),
	}, "url")
}
func (t *webFetchTool) Execute(ctx context.Context, input json.RawMessage) (gage.ToolResult, error) {
	var args struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return gage.ToolResult{}, err
	}
	u, err := validateFetchURL(ctx, args.URL, t.cfg.AllowPrivateHosts)
	if err != nil {
		return errResult(err), nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return errResult(err), nil
	}
	if t.cfg.UserAgent != "" {
		req.Header.Set("User-Agent", t.cfg.UserAgent)
	}
	client := t.cfg.httpClient()
	resp, err := client.Do(req)
	if err != nil {
		return errResult(err), nil
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, t.cfg.maxFetch()))
	text := body
	if strings.Contains(resp.Header.Get("Content-Type"), "html") {
		text = []byte(stripHTML(string(body)))
	}
	if resp.StatusCode/100 != 2 {
		return gage.ErrorResult("", fmt.Sprintf("HTTP %d\n%s", resp.StatusCode, text)), nil
	}
	return gage.TextResult("", string(text)), nil
}

func (c WebConfig) httpClient() *http.Client {
	base := c.http()
	client := *base
	originalRedirect := client.CheckRedirect
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if _, err := validateFetchURL(req.Context(), req.URL.String(), c.AllowPrivateHosts); err != nil {
			return err
		}
		if originalRedirect != nil {
			return originalRedirect(req, via)
		}
		if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 redirects")
		}
		return nil
	}
	return &client
}

func validateFetchURL(ctx context.Context, raw string, allowPrivate bool) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("url must be http(s)")
	}
	if u.Hostname() == "" {
		return nil, fmt.Errorf("url host is required")
	}
	if !allowPrivate {
		if err := rejectPrivateHost(ctx, u.Hostname()); err != nil {
			return nil, err
		}
	}
	return u, nil
}

func rejectPrivateHost(ctx context.Context, host string) error {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return fmt.Errorf("url host %q is private", host)
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		if blockedAddr(addr) {
			return fmt.Errorf("url host %q is private", host)
		}
		return nil
	}
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("resolve %q: %w", host, err)
	}
	for _, ip := range addrs {
		addr, ok := netip.AddrFromSlice(ip.IP)
		if ok && blockedAddr(addr) {
			return fmt.Errorf("url host %q resolves to private address", host)
		}
	}
	return nil
}

func blockedAddr(addr netip.Addr) bool {
	addr = addr.Unmap()
	return addr.IsLoopback() ||
		addr.IsPrivate() ||
		addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() ||
		addr.IsMulticast() ||
		addr.IsUnspecified()
}

var (
	scriptStyleRe = regexp.MustCompile(`(?is)<(script|style)[^>]*>.*?</(script|style)>`)
	tagRe         = regexp.MustCompile(`(?s)<[^>]+>`)
	wsRe          = regexp.MustCompile(`[ \t]*\n[ \t\n]*`)
)

// stripHTML produces a rough plain-text rendering of an HTML document.
func stripHTML(html string) string {
	html = scriptStyleRe.ReplaceAllString(html, " ")
	html = tagRe.ReplaceAllString(html, " ")
	html = htmlUnescape(html)
	html = wsRe.ReplaceAllString(html, "\n")
	return strings.TrimSpace(html)
}

func htmlUnescape(s string) string {
	r := strings.NewReplacer(
		"&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'", "&nbsp;", " ",
	)
	return r.Replace(s)
}

// ---- web_search ----

type webSearchTool struct{ search gage.SearchProvider }

func (t *webSearchTool) Name() string { return "web_search" }
func (t *webSearchTool) Description() string {
	return "Search the web and return a list of result titles, URLs and snippets."
}
func (t *webSearchTool) Schema() gage.JSONSchema {
	return jsonschema.Object(map[string]jsonschema.Property{
		"query": jsonschema.Str("The search query."),
		"limit": jsonschema.Int("Maximum number of results (default 5)."),
	}, "query")
}
func (t *webSearchTool) Execute(ctx context.Context, input json.RawMessage) (gage.ToolResult, error) {
	var args struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return gage.ToolResult{}, err
	}
	if args.Limit <= 0 {
		args.Limit = 5
	}
	results, err := t.search.Search(ctx, args.Query, args.Limit)
	if err != nil {
		return errResult(err), nil
	}
	if len(results) == 0 {
		return gage.TextResult("", "no results"), nil
	}
	var b strings.Builder
	for i, r := range results {
		fmt.Fprintf(&b, "%d. %s\n   %s\n   %s\n", i+1, r.Title, r.URL, r.Snippet)
	}
	return gage.TextResult("", strings.TrimRight(b.String(), "\n")), nil
}
