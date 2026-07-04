package tavily

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSearchSendsBearerAndParsesResults(t *testing.T) {
	var gotAuth string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"results":[{"title":"A","url":"https://a","content":"one"}]}`)
	}))
	t.Cleanup(srv.Close)

	p := New("secret")
	p.Endpoint = srv.URL
	results, err := p.Search(context.Background(), "hello", 3)
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer secret" {
		t.Fatalf("auth = %q", gotAuth)
	}
	if gotBody["query"] != "hello" || gotBody["max_results"].(float64) != 3 {
		t.Fatalf("body = %+v", gotBody)
	}
	if len(results) != 1 || results[0].Title != "A" || results[0].Snippet != "one" {
		t.Fatalf("results = %+v", results)
	}
}
