package gage

// ReasoningEffort hints how much internal reasoning the model should spend, for
// providers that support it (Codex/Responses, Anthropic thinking, etc.).
type ReasoningEffort string

const (
	ReasoningNone   ReasoningEffort = ""
	ReasoningLow    ReasoningEffort = "low"
	ReasoningMedium ReasoningEffort = "medium"
	ReasoningHigh   ReasoningEffort = "high"
)

// GenerateOptions carries per-request generation parameters. All fields are
// optional; providers apply their own defaults for zero values. Pointer fields
// distinguish "unset" from a meaningful zero (e.g. Temperature 0).
type GenerateOptions struct {
	Temperature     *float64
	TopP            *float64
	MaxTokens       int
	StopSequences   []string
	ToolChoice      *ToolChoice
	ReasoningEffort ReasoningEffort
	// Extra passes provider-specific fields verbatim into the request body.
	// Keys are merged at the top level; use with care.
	Extra map[string]any
}

// Option mutates GenerateOptions. Providers and the agent accept variadic
// Options for ergonomic configuration.
type Option func(*GenerateOptions)

// WithTemperature sets the sampling temperature.
func WithTemperature(t float64) Option {
	return func(o *GenerateOptions) { o.Temperature = &t }
}

// WithTopP sets nucleus sampling.
func WithTopP(p float64) Option {
	return func(o *GenerateOptions) { o.TopP = &p }
}

// WithMaxTokens caps the number of generated tokens.
func WithMaxTokens(n int) Option {
	return func(o *GenerateOptions) { o.MaxTokens = n }
}

// WithStopSequences sets stop sequences.
func WithStopSequences(seqs ...string) Option {
	return func(o *GenerateOptions) { o.StopSequences = seqs }
}

// WithToolChoice constrains tool selection.
func WithToolChoice(tc ToolChoice) Option {
	return func(o *GenerateOptions) { o.ToolChoice = &tc }
}

// WithReasoningEffort sets the reasoning effort hint.
func WithReasoningEffort(e ReasoningEffort) Option {
	return func(o *GenerateOptions) { o.ReasoningEffort = e }
}

// WithExtra sets a provider-specific field.
func WithExtra(key string, value any) Option {
	return func(o *GenerateOptions) {
		if o.Extra == nil {
			o.Extra = map[string]any{}
		}
		o.Extra[key] = value
	}
}

// ApplyOptions builds a GenerateOptions from a base value and a set of Options.
func ApplyOptions(base GenerateOptions, opts ...Option) GenerateOptions {
	for _, opt := range opts {
		opt(&base)
	}
	return base
}
