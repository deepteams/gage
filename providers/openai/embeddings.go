package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/deepteams/gage"
	"github.com/deepteams/gage/providers/shared"
)

// defaultEmbeddingsBaseURL is the OpenAI API root used when BaseURL is empty.
const defaultEmbeddingsBaseURL = "https://api.openai.com/v1"

var defaultEmbeddingsHTTP = shared.NewClient("gage/openai")

// Embeddings implements gage.Embedder over an OpenAI-compatible POST
// {BaseURL}/embeddings endpoint. Like ChatClient, it is reusable by any
// server speaking this wire format: point BaseURL at OpenRouter, vLLM, or
// another compatible endpoint and set ProviderName accordingly.
type Embeddings struct {
	// ProviderName is reported by Name() (e.g. "openrouter"). Defaults to
	// "openai".
	ProviderName string
	// BaseURL is the API root without a trailing slash (e.g.
	// "https://api.openai.com/v1"). The path "/embeddings" is appended.
	// Defaults to the OpenAI API.
	BaseURL string
	// APIKey, when set, is sent as a Bearer token. Optional (vLLM).
	APIKey string
	// Model is the embedding model id (required), e.g.
	// "text-embedding-3-small".
	Model string
	// Dimensions, when > 0, asks the server to produce vectors of this size.
	// Omitted from the request when 0.
	Dimensions int
	// HTTP is the shared client. If nil, a package default is used.
	HTTP *shared.Client
}

var _ gage.Embedder = (*Embeddings)(nil)

// Name implements gage.Embedder.
func (e *Embeddings) Name() string {
	if e.ProviderName != "" {
		return e.ProviderName
	}
	return "openai"
}

func (e *Embeddings) http() *shared.Client {
	if e.HTTP != nil {
		return e.HTTP
	}
	return defaultEmbeddingsHTTP
}

// Embed implements gage.Embedder. It returns one vector per input text, in
// input order. An empty input returns (nil, nil) without an HTTP call.
func (e *Embeddings) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	if e.Model == "" {
		return nil, fmt.Errorf("%s: embeddings: no model specified", e.Name())
	}

	payload := map[string]any{
		"model":           e.Model,
		"input":           texts,
		"encoding_format": "float",
	}
	if e.Dimensions > 0 {
		payload["dimensions"] = e.Dimensions
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	base := e.BaseURL
	if base == "" {
		base = defaultEmbeddingsBaseURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.APIKey)
	}

	resp, err := e.http().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		return nil, &gage.APIError{Provider: e.Name(), Status: resp.StatusCode, Body: string(b)}
	}

	var parsed struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("%s: embeddings: decode response: %w", e.Name(), err)
	}
	if len(parsed.Data) != len(texts) {
		return nil, fmt.Errorf("%s: embeddings: got %d vectors for %d inputs", e.Name(), len(parsed.Data), len(texts))
	}
	out := make([][]float32, len(texts))
	for _, d := range parsed.Data {
		if d.Index < 0 || d.Index >= len(texts) {
			return nil, fmt.Errorf("%s: embeddings: vector index %d out of range for %d inputs", e.Name(), d.Index, len(texts))
		}
		if out[d.Index] != nil {
			return nil, fmt.Errorf("%s: embeddings: duplicate vector index %d", e.Name(), d.Index)
		}
		out[d.Index] = d.Embedding
	}
	for i, v := range out {
		if v == nil {
			return nil, fmt.Errorf("%s: embeddings: missing vector for input %d", e.Name(), i)
		}
	}
	return out, nil
}
