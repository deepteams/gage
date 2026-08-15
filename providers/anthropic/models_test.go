package anthropic

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deepteams/gage"
)

func TestModelsPaginates(t *testing.T) {
	var gotPaths []string
	var gotKeys, gotVersions []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.URL.Path+"?"+r.URL.RawQuery)
		gotKeys = append(gotKeys, r.Header.Get("x-api-key"))
		gotVersions = append(gotVersions, r.Header.Get("anthropic-version"))
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("after_id") == "" {
			io.WriteString(w, `{"data":[{"id":"claude-a","display_name":"Claude A"}],"has_more":true,"last_id":"claude-a"}`)
			return
		}
		io.WriteString(w, `{"data":[{"id":"claude-b","display_name":"Claude B"}],"has_more":false,"last_id":"claude-b"}`)
	}))
	t.Cleanup(srv.Close)

	p := New(Config{APIKey: "key", BaseURL: srv.URL})
	lister, ok := p.(gage.ModelLister)
	if !ok {
		t.Fatal("anthropic provider does not implement gage.ModelLister")
	}
	infos, err := lister.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []gage.ModelInfo{
		{ID: "claude-a", Name: "Claude A"},
		{ID: "claude-b", Name: "Claude B"},
	}
	if len(infos) != len(want) {
		t.Fatalf("got %d models, want %d", len(infos), len(want))
	}
	for i := range want {
		if infos[i] != want[i] {
			t.Fatalf("model %d = %+v, want %+v", i, infos[i], want[i])
		}
	}
	if len(gotPaths) != 2 || !strings.HasPrefix(gotPaths[0], "/v1/models?limit=1000") ||
		!strings.Contains(gotPaths[1], "after_id=claude-a") {
		t.Fatalf("paths = %v", gotPaths)
	}
	for i := range gotPaths {
		if gotKeys[i] != "key" || gotVersions[i] != Version {
			t.Fatalf("request %d headers key=%q version=%q", i, gotKeys[i], gotVersions[i])
		}
	}
}

func TestModelsNonStandardURLFails(t *testing.T) {
	c := &Client{ProviderName: "anthropic", URL: "https://example.com/custom/endpoint"}
	if _, err := c.Models(context.Background()); err == nil {
		t.Fatal("expected error for non-standard messages URL")
	}
}
