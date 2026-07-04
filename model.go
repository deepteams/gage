package gage

// ModelInfo describes a model advertised by a provider.
type ModelInfo struct {
	// ID is the provider-specific model identifier (e.g. "anthropic/claude-3.5").
	ID string `json:"id"`
	// Name is a human-friendly label, when available.
	Name string `json:"name,omitempty"`
	// ContextWindow is the max total tokens, when known.
	ContextWindow int `json:"context_window,omitempty"`
	// MaxOutputTokens is the max tokens the model can emit, when known.
	MaxOutputTokens int `json:"max_output_tokens,omitempty"`
}

// ModelRef points at a model on a given provider. It is a convenience for
// callers that route across providers; the library itself only needs the
// Model string inside a Request.
type ModelRef struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

func (r ModelRef) String() string {
	if r.Provider == "" {
		return r.Model
	}
	return r.Provider + "/" + r.Model
}
