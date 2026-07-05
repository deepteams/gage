package gage

import "context"

// Embedder is the port for computing vector embeddings of text. Adapters live
// under providers/ (OpenAI-compatible APIs, Ollama); consumers can plug any
// implementation into retrieval layers such as memory.Store.
type Embedder interface {
	// Embed returns one vector per input text, in the same order. An empty
	// input returns an empty (or nil) slice. Implementations must not return
	// fewer vectors than inputs without an error.
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	// Name identifies the embedder for telemetry and logs.
	Name() string
}
