package ollama

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

func embedServer(t *testing.T, response string, status int, capture *[]byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			t.Errorf("path = %q, want /api/embed", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if capture != nil {
			b, _ := io.ReadAll(r.Body)
			*capture = b
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, response)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestEmbedRequestAndMapping(t *testing.T) {
	var reqBody []byte
	srv := embedServer(t, `{"embeddings":[[0.1,0.2],[0.3,0.4]]}`, http.StatusOK, &reqBody)

	e := &Embedder{BaseURL: srv.URL, Model: "nomic-embed-text"}
	if got := e.Name(); got != "ollama" {
		t.Fatalf("Name() = %q", got)
	}
	vecs, err := e.Embed(context.Background(), []string{"alpha", "beta"})
	if err != nil {
		t.Fatal(err)
	}
	want := [][]float32{{0.1, 0.2}, {0.3, 0.4}}
	if !reflect.DeepEqual(vecs, want) {
		t.Fatalf("vecs = %v, want %v", vecs, want)
	}

	var body map[string]any
	if err := json.Unmarshal(reqBody, &body); err != nil {
		t.Fatal(err)
	}
	if body["model"] != "nomic-embed-text" {
		t.Fatalf("model = %v", body["model"])
	}
	input, ok := body["input"].([]any)
	if !ok || len(input) != 2 || input[0] != "alpha" || input[1] != "beta" {
		t.Fatalf("input = %v", body["input"])
	}
}

func TestEmbedCountMismatch(t *testing.T) {
	srv := embedServer(t, `{"embeddings":[[0.1]]}`, http.StatusOK, nil)

	e := &Embedder{BaseURL: srv.URL, Model: "m"}
	_, err := e.Embed(context.Background(), []string{"a", "b"})
	if err == nil || !strings.Contains(err.Error(), "1 vectors for 2 inputs") {
		t.Fatalf("err = %v, want count mismatch", err)
	}
}

func TestEmbedAPIError(t *testing.T) {
	srv := embedServer(t, `{"error":"model not found"}`, http.StatusNotFound, nil)

	e := &Embedder{BaseURL: srv.URL, Model: "missing"}
	_, err := e.Embed(context.Background(), []string{"a"})
	var apiErr *gage.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *gage.APIError", err)
	}
	if apiErr.Status != http.StatusNotFound || apiErr.Provider != "ollama" {
		t.Fatalf("apiErr = %+v", apiErr)
	}
}

func TestEmbedEmptyInput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("no HTTP call expected for empty input")
	}))
	t.Cleanup(srv.Close)

	e := &Embedder{BaseURL: srv.URL, Model: "m"}
	vecs, err := e.Embed(context.Background(), nil)
	if err != nil || vecs != nil {
		t.Fatalf("Embed(nil) = %v, %v; want nil, nil", vecs, err)
	}
}

func TestEmbedModelRequired(t *testing.T) {
	e := &Embedder{BaseURL: "http://127.0.0.1:0"}
	if _, err := e.Embed(context.Background(), []string{"a"}); err == nil {
		t.Fatal("want error for missing model")
	}
}
