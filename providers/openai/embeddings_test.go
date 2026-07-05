package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/deepteams/gage"
)

func embeddingsServer(t *testing.T, response string, status int, capture *[]byte, captureAuth *string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			t.Errorf("path = %q, want /embeddings", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if capture != nil {
			b, _ := io.ReadAll(r.Body)
			*capture = b
		}
		if captureAuth != nil {
			*captureAuth = r.Header.Get("Authorization")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, response)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestEmbeddingsRequestAndIndexReorder(t *testing.T) {
	// Vectors returned out of index order must be reordered onto input order.
	resp := `{"data":[
		{"index":1,"embedding":[0.3,0.4]},
		{"index":0,"embedding":[0.1,0.2]}
	]}`
	var reqBody []byte
	var auth string
	srv := embeddingsServer(t, resp, http.StatusOK, &reqBody, &auth)

	e := &Embeddings{BaseURL: srv.URL, APIKey: "sk-test", Model: "text-embedding-3-small", Dimensions: 2}
	vecs, err := e.Embed(context.Background(), []string{"alpha", "beta"})
	if err != nil {
		t.Fatal(err)
	}
	want := [][]float32{{0.1, 0.2}, {0.3, 0.4}}
	if !reflect.DeepEqual(vecs, want) {
		t.Fatalf("vecs = %v, want %v", vecs, want)
	}
	if auth != "Bearer sk-test" {
		t.Fatalf("Authorization = %q", auth)
	}

	var body map[string]any
	if err := json.Unmarshal(reqBody, &body); err != nil {
		t.Fatal(err)
	}
	if body["model"] != "text-embedding-3-small" {
		t.Fatalf("model = %v", body["model"])
	}
	if body["encoding_format"] != "float" {
		t.Fatalf("encoding_format = %v", body["encoding_format"])
	}
	if body["dimensions"] != float64(2) {
		t.Fatalf("dimensions = %v", body["dimensions"])
	}
	input, ok := body["input"].([]any)
	if !ok || len(input) != 2 || input[0] != "alpha" || input[1] != "beta" {
		t.Fatalf("input = %v", body["input"])
	}
}

func TestEmbeddingsOmitsZeroDimensions(t *testing.T) {
	var reqBody []byte
	srv := embeddingsServer(t, `{"data":[{"index":0,"embedding":[1]}]}`, http.StatusOK, &reqBody, nil)

	e := &Embeddings{BaseURL: srv.URL, Model: "m"}
	if _, err := e.Embed(context.Background(), []string{"a"}); err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(reqBody, &body); err != nil {
		t.Fatal(err)
	}
	if _, present := body["dimensions"]; present {
		t.Fatalf("dimensions should be omitted when 0, body = %v", body)
	}
}

func TestEmbeddingsCountMismatch(t *testing.T) {
	srv := embeddingsServer(t, `{"data":[{"index":0,"embedding":[1,2]}]}`, http.StatusOK, nil, nil)

	e := &Embeddings{BaseURL: srv.URL, Model: "m"}
	_, err := e.Embed(context.Background(), []string{"a", "b"})
	if err == nil || !strings.Contains(err.Error(), "1 vectors for 2 inputs") {
		t.Fatalf("err = %v, want count mismatch", err)
	}
}

func TestEmbeddingsAPIError(t *testing.T) {
	srv := embeddingsServer(t, `{"error":{"message":"bad key"}}`, http.StatusUnauthorized, nil, nil)

	e := &Embeddings{BaseURL: srv.URL, Model: "m", ProviderName: "openrouter"}
	_, err := e.Embed(context.Background(), []string{"a"})
	var apiErr *gage.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *gage.APIError", err)
	}
	if apiErr.Status != http.StatusUnauthorized || apiErr.Provider != "openrouter" {
		t.Fatalf("apiErr = %+v", apiErr)
	}
	if !errors.Is(err, gage.ErrAuth) {
		t.Fatalf("401 should match gage.ErrAuth, got %v", err)
	}
}

func TestEmbeddingsEmptyInput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("no HTTP call expected for empty input")
	}))
	t.Cleanup(srv.Close)

	e := &Embeddings{BaseURL: srv.URL, Model: "m"}
	vecs, err := e.Embed(context.Background(), nil)
	if err != nil || vecs != nil {
		t.Fatalf("Embed(nil) = %v, %v; want nil, nil", vecs, err)
	}
}

func TestEmbeddingsName(t *testing.T) {
	if got := (&Embeddings{}).Name(); got != "openai" {
		t.Fatalf("default Name() = %q, want openai", got)
	}
	if got := (&Embeddings{ProviderName: "vllm"}).Name(); got != "vllm" {
		t.Fatalf("Name() = %q, want vllm", got)
	}
}

func TestEmbeddingsModelRequired(t *testing.T) {
	e := &Embeddings{BaseURL: "http://127.0.0.1:0"}
	if _, err := e.Embed(context.Background(), []string{"a"}); err == nil {
		t.Fatal("want error for missing model")
	}
}
