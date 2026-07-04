package openrouter

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deepteams/gage"
)

func TestProviderSendsOpenRouterHeaders(t *testing.T) {
	var gotPath, gotAuth, gotReferer, gotTitle string
	var reqBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotReferer = r.Header.Get("HTTP-Referer")
		gotTitle = r.Header.Get("X-Title")
		b, _ := io.ReadAll(r.Body)
		reqBody = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`+"\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)

	p := New("key", WithBaseURL(srv.URL), WithDefaultModel("model"), WithReferer("https://example.com", "Example"))
	ch, err := p.Stream(context.Background(), gage.Request{Messages: []gage.Message{gage.UserText("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	if gotPath != "/chat/completions" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotAuth != "Bearer key" || gotReferer != "https://example.com" || gotTitle != "Example" {
		t.Fatalf("headers auth=%q referer=%q title=%q", gotAuth, gotReferer, gotTitle)
	}
	if !strings.Contains(reqBody, `"model":"model"`) {
		t.Fatalf("request body = %s", reqBody)
	}
}
