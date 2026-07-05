package gage

import "context"

// ToolSchema is the declaration of a tool exposed to the model. It is distinct
// from the executable Tool port: a Provider only needs the schema to advertise
// the tool, while the agent needs the Tool to run it.
type ToolSchema struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Parameters  JSONSchema `json:"parameters"`
}

// Request is everything a provider needs to produce one assistant turn.
type Request struct {
	// Model is the provider-specific model identifier. May be empty when the
	// provider is pinned to a single model.
	Model string
	// Messages is the conversation history (excluding the System prompt).
	Messages []Message
	// Tools are the schemas advertised to the model.
	Tools []ToolSchema
	// System is the system prompt (may be empty).
	System string
	// Options are the generation parameters.
	Options GenerateOptions
}

// Provider is the core port for a model backend. Implementations map a Request
// onto their wire protocol and stream the response back as Events.
type Provider interface {
	// Stream starts a generation and returns a read-only channel of Events. The
	// channel is closed when generation finishes (after EventMessageDone) or on
	// a terminal error (after EventError). Cancelling ctx must stop the stream
	// and close the channel. Stream returns an error only for failures that
	// occur before streaming begins (e.g. request construction, initial dial).
	Stream(ctx context.Context, req Request) (<-chan Event, error)
	// Name identifies the provider for telemetry and logs.
	Name() string
}

// ModelLister is an optional capability: a provider that can enumerate models.
type ModelLister interface {
	Models(ctx context.Context) ([]ModelInfo, error)
}

// TokenCounter is an optional capability: a provider that can count the exact
// input tokens of a request through its API (Anthropic count_tokens, Gemini
// countTokens). It costs an extra HTTP round-trip; EstimateTokens remains the
// free heuristic when precision is not required.
type TokenCounter interface {
	CountTokens(ctx context.Context, req Request) (int, error)
}
