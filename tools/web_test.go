package tools

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/deepteams/gage"
)

func TestWebFetchStripsHTML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, "<html><head><style>x{}</style></head><body><h1>Hi</h1><p>World &amp; more</p></body></html>")
	}))
	t.Cleanup(srv.Close)

	tools := NewWebTools(WebConfig{AllowPrivateHosts: true})
	fetch := toolByName(tools, "web_fetch")
	res := run(t, fetch, `{"url":"`+srv.URL+`"}`)
	text := res.Text()
	if !strings.Contains(text, "Hi") || !strings.Contains(text, "World & more") {
		t.Fatalf("fetch text = %q", text)
	}
	if strings.Contains(text, "<") || strings.Contains(text, "x{}") {
		t.Fatalf("html not stripped: %q", text)
	}
}

func TestWebFetchBlocksPrivateHostsByDefault(t *testing.T) {
	fetch := toolByName(NewWebTools(WebConfig{}), "web_fetch")
	for _, url := range []string{
		"http://localhost:1234",
		"http://127.0.0.1:1234",
		"http://10.0.0.8",
		"http://169.254.169.254/latest/meta-data/", // cloud metadata
		"http://100.64.0.7",                        // CGNAT
		"http://[64:ff9b::a00:1]/",                 // NAT64-mapped 10.0.0.1
		"http://[::1]:8080",
	} {
		res := run(t, fetch, `{"url":"`+url+`"}`)
		if !res.IsError || !strings.Contains(res.Text(), "private") {
			t.Fatalf("%s: expected private host error, got %q", url, res.Text())
		}
	}
}

func TestBlockedAddr(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1",
		"169.254.169.254", "0.0.0.0", "224.0.0.1",
		"100.64.0.1", "100.127.255.255", // CGNAT range
		"::1", "fe80::1", "fd00::1",
		"64:ff9b::808:808", // NAT64 well-known prefix
		"::ffff:10.0.0.1",  // 4-mapped-6 private
	}
	for _, s := range blocked {
		if !blockedAddr(netip.MustParseAddr(s)) {
			t.Errorf("%s should be blocked", s)
		}
	}
	allowed := []string{"8.8.8.8", "1.1.1.1", "100.63.255.255", "100.128.0.1", "2606:4700::1111"}
	for _, s := range allowed {
		if blockedAddr(netip.MustParseAddr(s)) {
			t.Errorf("%s should not be blocked", s)
		}
	}
}

// stubResolver replaces DNS resolution for the duration of a test.
func stubResolver(t *testing.T, fn func(ctx context.Context, host string) ([]net.IPAddr, error)) {
	t.Helper()
	orig := lookupIPAddr
	lookupIPAddr = fn
	t.Cleanup(func() { lookupIPAddr = orig })
}

// TestWebFetchBlocksDNSRebinding simulates a rebinding attack: the pre-flight
// resolution returns a public address, then the dial-time resolution returns
// loopback. The pinned-dial transport must catch it and never touch the server.
func TestWebFetchBlocksDNSRebinding(t *testing.T) {
	var hit atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit.Store(true)
	}))
	t.Cleanup(srv.Close)
	port := srv.Listener.Addr().(*net.TCPAddr).Port

	var calls atomic.Int32
	stubResolver(t, func(ctx context.Context, host string) ([]net.IPAddr, error) {
		if calls.Add(1) == 1 {
			return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil // public: passes pre-flight
		}
		return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil // rebound: must be blocked at dial
	})

	fetch := toolByName(NewWebTools(WebConfig{}), "web_fetch")
	res := run(t, fetch, `{"url":"http://rebind.test:`+strconv.Itoa(port)+`/"}`)
	if !res.IsError || !strings.Contains(res.Text(), "private") {
		t.Fatalf("expected rebinding to be blocked, got %q", res.Text())
	}
	if calls.Load() < 2 {
		t.Fatalf("expected a dial-time re-resolution, got %d lookups", calls.Load())
	}
	if hit.Load() {
		t.Fatal("request reached the server despite rebinding block")
	}
}

// TestWebFetchHostnameThroughPinnedDialer exercises the happy path through the
// custom transport: the URL keeps the hostname (Host header) while the dial
// goes to the vetted resolved address.
func TestWebFetchHostnameThroughPinnedDialer(t *testing.T) {
	var gotHost string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		io.WriteString(w, "pinned ok")
	}))
	t.Cleanup(srv.Close)
	port := srv.Listener.Addr().(*net.TCPAddr).Port

	stubResolver(t, func(ctx context.Context, host string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
	})

	fetch := toolByName(NewWebTools(WebConfig{AllowPrivateHosts: true}), "web_fetch")
	res := run(t, fetch, `{"url":"http://pinned.test:`+strconv.Itoa(port)+`/"}`)
	if res.IsError || !strings.Contains(res.Text(), "pinned ok") {
		t.Fatalf("pinned fetch = %q", res.Text())
	}
	if !strings.HasPrefix(gotHost, "pinned.test") {
		t.Fatalf("Host header = %q, want the original hostname", gotHost)
	}
}

type fakeSearch struct{ results []gage.SearchResult }

func (f fakeSearch) Search(ctx context.Context, q string, limit int) ([]gage.SearchResult, error) {
	return f.results, nil
}

func TestWebSearchTool(t *testing.T) {
	fs := fakeSearch{results: []gage.SearchResult{
		{Title: "T1", URL: "http://a", Snippet: "s1"},
		{Title: "T2", URL: "http://b", Snippet: "s2"},
	}}
	tools := NewWebTools(WebConfig{Search: fs})
	search := toolByName(tools, "web_search")
	if search == nil {
		t.Fatal("web_search not present when SearchProvider set")
	}
	res := run(t, search, `{"query":"hi"}`)
	if !strings.Contains(res.Text(), "T1") || !strings.Contains(res.Text(), "http://b") {
		t.Fatalf("search text = %q", res.Text())
	}
}

func TestWebSearchAbsentWithoutProvider(t *testing.T) {
	tools := NewWebTools(WebConfig{})
	if toolByName(tools, "web_search") != nil {
		t.Fatal("web_search should be absent without a SearchProvider")
	}
}
