package tools

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
	tools := NewWebTools(WebConfig{})
	fetch := toolByName(tools, "web_fetch")
	res := run(t, fetch, `{"url":"http://localhost:1234"}`)
	if !res.IsError || !strings.Contains(res.Text(), "private") {
		t.Fatalf("expected private host error, got %q", res.Text())
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
