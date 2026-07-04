package duckduckgo

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

const sampleHTML = `
<div class="result">
  <a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fa">Example <b>A</b></a>
  <a class="result__snippet" href="#">Snippet about A &amp; things</a>
</div>
<div class="result">
  <a class="result__a" href="https://example.org/b">Example B</a>
  <a class="result__snippet" href="#">Snippet B</a>
</div>
`

func TestSearchParsing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, sampleHTML)
	}))
	t.Cleanup(srv.Close)

	p := &Provider{Endpoint: srv.URL}
	results, err := p.Search(context.Background(), "example", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d: %+v", len(results), results)
	}
	if results[0].Title != "Example A" {
		t.Fatalf("title = %q", results[0].Title)
	}
	if results[0].URL != "https://example.com/a" {
		t.Fatalf("url decode = %q", results[0].URL)
	}
	if results[0].Snippet != "Snippet about A & things" {
		t.Fatalf("snippet = %q", results[0].Snippet)
	}
}

func TestSearchLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, sampleHTML)
	}))
	t.Cleanup(srv.Close)
	p := &Provider{Endpoint: srv.URL}
	results, err := p.Search(context.Background(), "example", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
}
