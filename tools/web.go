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
	"github.com/deepteams/gage/jsonschema"
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

// httpClient derives the client used by web_fetch from the configured base
// client. It installs a redirect validator (every hop must pass
// validateFetchURL) and, to defeat DNS rebinding (TOCTOU between the
// pre-flight check and the connection), a dialer that resolves the hostname
// once, validates every returned address, and dials one of those exact
// addresses. The URL keeps the original hostname, so the Host header and TLS
// ServerName/SNI are unchanged.
func (c WebConfig) httpClient() *http.Client {
	base := c.http()
	client := *base
	client.Transport = c.pinnedTransport(base.Transport)
	originalRedirect := base.CheckRedirect
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

// pinnedTransport clones the base transport and overrides DialContext so name
// resolution and address validation happen atomically with dialing. When the
// base RoundTripper is not an *http.Transport it cannot be re-dialed safely
// and is returned unchanged; the pre-flight and per-redirect URL validation
// still apply.
func (c WebConfig) pinnedTransport(rt http.RoundTripper) http.RoundTripper {
	var t *http.Transport
	switch base := rt.(type) {
	case nil:
		t = http.DefaultTransport.(*http.Transport).Clone()
	case *http.Transport:
		t = base.Clone()
	default:
		return rt
	}
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	t.DialContext = func(ctx context.Context, network, hostport string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(hostport)
		if err != nil {
			return nil, err
		}
		addrs, err := resolveVetted(ctx, host, c.AllowPrivateHosts)
		if err != nil {
			return nil, err
		}
		var firstErr error
		for _, addr := range addrs {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(addr.String(), port))
			if err == nil {
				return conn, nil
			}
			if firstErr == nil {
				firstErr = err
			}
		}
		return nil, firstErr
	}
	return t
}

// lookupIPAddr resolves a hostname. It is a variable so tests can simulate
// DNS rebinding without the network.
var lookupIPAddr = func(ctx context.Context, host string) ([]net.IPAddr, error) {
	return net.DefaultResolver.LookupIPAddr(ctx, host)
}

// resolveVetted resolves host and, unless allowPrivate, rejects the whole
// lookup if any returned address is private/blocked. The returned addresses
// are the ones that must be dialed: dialing anything else would allow the DNS
// answer to change between validation and connection.
func resolveVetted(ctx context.Context, host string, allowPrivate bool) ([]netip.Addr, error) {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if addr, err := netip.ParseAddr(host); err == nil {
		if !allowPrivate && blockedAddr(addr) {
			return nil, fmt.Errorf("url host %q is private", host)
		}
		return []netip.Addr{addr}, nil
	}
	if !allowPrivate && (host == "localhost" || strings.HasSuffix(host, ".localhost")) {
		return nil, fmt.Errorf("url host %q is private", host)
	}
	ipAddrs, err := lookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolve %q: %w", host, err)
	}
	addrs := make([]netip.Addr, 0, len(ipAddrs))
	for _, ip := range ipAddrs {
		addr, ok := netip.AddrFromSlice(ip.IP)
		if !ok {
			continue
		}
		if !allowPrivate && blockedAddr(addr) {
			return nil, fmt.Errorf("url host %q resolves to private address", host)
		}
		addrs = append(addrs, addr)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("resolve %q: no addresses", host)
	}
	return addrs, nil
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
	if _, err := resolveVetted(ctx, u.Hostname(), allowPrivate); err != nil {
		return nil, err
	}
	return u, nil
}

var (
	cgnatPrefix = netip.MustParsePrefix("100.64.0.0/10") // RFC 6598 carrier-grade NAT
	nat64Prefix = netip.MustParsePrefix("64:ff9b::/96")  // RFC 6052 NAT64 well-known prefix
)

func blockedAddr(addr netip.Addr) bool {
	addr = addr.Unmap()
	return addr.IsLoopback() ||
		addr.IsPrivate() ||
		addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() ||
		addr.IsMulticast() ||
		addr.IsUnspecified() ||
		cgnatPrefix.Contains(addr) ||
		// A NAT64 gateway would translate these to the embedded IPv4 address,
		// bypassing the IPv4 checks; block the whole well-known prefix.
		nat64Prefix.Contains(addr)
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
