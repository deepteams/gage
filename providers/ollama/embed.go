package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/deepteams/gage"
	"github.com/deepteams/gage/providers/shared"
)

var defaultEmbedHTTP = shared.NewClient("gage/ollama")

// Embedder implements gage.Embedder over Ollama's native POST /api/embed
// endpoint.
type Embedder struct {
	// BaseURL is the Ollama server root without a trailing slash. Defaults to
	// DefaultBaseURL. The path "/api/embed" is appended.
	BaseURL string
	// Model is the embedding model (required), e.g. "nomic-embed-text".
	Model string
	// HTTP is the shared client. If nil, a package default is used.
	HTTP *shared.Client
}

var _ gage.Embedder = (*Embedder)(nil)

// Name implements gage.Embedder.
func (e *Embedder) Name() string { return "ollama" }

func (e *Embedder) client() *shared.Client {
	if e.HTTP != nil {
		return e.HTTP
	}
	return defaultEmbedHTTP
}

// Embed implements gage.Embedder. It returns one vector per input text, in
// input order. An empty input returns (nil, nil) without an HTTP call.
func (e *Embedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	if e.Model == "" {
		return nil, fmt.Errorf("ollama: embed: no model specified")
	}

	body, err := json.Marshal(map[string]any{
		"model": e.Model,
		"input": texts,
	})
	if err != nil {
		return nil, err
	}

	base := DefaultBaseURL
	if e.BaseURL != "" {
		base = strings.TrimRight(e.BaseURL, "/")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		return nil, &gage.APIError{Provider: "ollama", Status: resp.StatusCode, Body: string(b)}
	}

	var parsed struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("ollama: embed: decode response: %w", err)
	}
	if len(parsed.Embeddings) != len(texts) {
		return nil, fmt.Errorf("ollama: embed: got %d vectors for %d inputs", len(parsed.Embeddings), len(texts))
	}
	return parsed.Embeddings, nil
}
