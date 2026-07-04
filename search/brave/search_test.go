package brave

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSearchSendsTokenAndParsesResults(t *testing.T) {
	var gotToken, gotQuery, gotCount string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("X-Subscription-Token")
		gotQuery = r.URL.Query().Get("q")
		gotCount = r.URL.Query().Get("count")
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"web":{"results":[{"title":"A","url":"https://a","description":"one"},{"title":"B","url":"https://b","description":"two"}]}}`)
	}))
	t.Cleanup(srv.Close)

	p := New("secret")
	p.Endpoint = srv.URL
	results, err := p.Search(context.Background(), "hello", 1)
	if err != nil {
		t.Fatal(err)
	}
	if gotToken != "secret" || gotQuery != "hello" || gotCount != "1" {
		t.Fatalf("request token=%q query=%q count=%q", gotToken, gotQuery, gotCount)
	}
	if len(results) != 1 || results[0].Title != "A" || results[0].URL != "https://a" || results[0].Snippet != "one" {
		t.Fatalf("results = %+v", results)
	}
}
