package openai

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/deepteams/gage"
)

func TestChatClientModels(t *testing.T) {
	var gotPath, gotAuth, gotExtra string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotExtra = r.Header.Get("X-Extra")
		w.Header().Set("Content-Type", "application/json")
		// One OpenRouter-style entry, one vLLM-style entry, one bare entry.
		io.WriteString(w, `{"data":[
			{"id":"meta/llama-3","name":"Llama 3","context_length":131072,"top_provider":{"max_completion_tokens":4096}},
			{"id":"qwen2.5","max_model_len":32768},
			{"id":"plain"}
		]}`)
	}))
	t.Cleanup(srv.Close)

	c := &ChatClient{
		ProviderName: "test",
		BaseURL:      srv.URL + "/v1",
		APIKey:       "key",
		Headers:      map[string]string{"X-Extra": "yes"},
	}
	infos, err := c.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/models" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotAuth != "Bearer key" || gotExtra != "yes" {
		t.Fatalf("headers auth=%q extra=%q", gotAuth, gotExtra)
	}
	want := []gage.ModelInfo{
		{ID: "meta/llama-3", Name: "Llama 3", ContextWindow: 131072, MaxOutputTokens: 4096},
		{ID: "qwen2.5", ContextWindow: 32768},
		{ID: "plain"},
	}
	if len(infos) != len(want) {
		t.Fatalf("got %d models, want %d", len(infos), len(want))
	}
	for i := range want {
		if infos[i] != want[i] {
			t.Fatalf("model %d = %+v, want %+v", i, infos[i], want[i])
		}
	}
}

func TestChatClientModelsAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"nope"}`, http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	c := &ChatClient{ProviderName: "test", BaseURL: srv.URL + "/v1"}
	_, err := c.Models(context.Background())
	var apiErr *gage.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized {
		t.Fatalf("err = %v, want 401 APIError", err)
	}
	if !errors.Is(err, gage.ErrAuth) {
		t.Fatalf("err = %v, want ErrAuth match", err)
	}
}
