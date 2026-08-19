package gage

import "strings"

// ReasoningEffort hints how much internal reasoning the model should spend, for
// providers that support it (Codex/Responses, Anthropic thinking, etc.).
//
// It is an open string, not a closed enum: gateways (llm-router, vLLM,
// OpenRouter) publish their own levels per model, and OpenAI-compatible
// providers forward the value verbatim. The constants below are the portable
// levels, ordered from least to most reasoning; Canonical folds the spellings
// seen in the wild onto them so providers that need a thinking-token budget
// (anthropic, gemini) can still map an arbitrary label.
type ReasoningEffort string

const (
	// ReasoningNone leaves the effort unset: the provider's own default applies
	// and nothing is sent on the wire.
	ReasoningNone ReasoningEffort = ""
	// ReasoningOff asks for reasoning to be disabled explicitly, for providers
	// that can say so (Anthropic thinking.disabled, Gemini budget 0, ollama
	// think:false, OpenAI "none").
	ReasoningOff     ReasoningEffort = "none"
	ReasoningMinimal ReasoningEffort = "minimal"
	ReasoningLow     ReasoningEffort = "low"
	ReasoningMedium  ReasoningEffort = "medium"
	ReasoningHigh    ReasoningEffort = "high"
	ReasoningXHigh   ReasoningEffort = "xhigh"
	ReasoningMax     ReasoningEffort = "max"
)

// reasoningAliases folds alternative spellings onto the portable levels. Keys
// are normalized by reasoningKey (lowercased, separators removed).
var reasoningAliases = map[string]ReasoningEffort{
	"":          ReasoningNone,
	"none":      ReasoningOff,
	"off":       ReasoningOff,
	"disabled":  ReasoningOff,
	"false":     ReasoningOff,
	"no":        ReasoningOff,
	"0":         ReasoningOff,
	"minimal":   ReasoningMinimal,
	"min":       ReasoningMinimal,
	"verylow":   ReasoningMinimal,
	"lowest":    ReasoningMinimal,
	"low":       ReasoningLow,
	"medium":    ReasoningMedium,
	"med":       ReasoningMedium,
	"mid":       ReasoningMedium,
	"moderate":  ReasoningMedium,
	"high":      ReasoningHigh,
	"xhigh":     ReasoningXHigh,
	"extrahigh": ReasoningXHigh,
	"veryhigh":  ReasoningXHigh,
	"max":       ReasoningMax,
	"maximum":   ReasoningMax,
	"highest":   ReasoningMax,
	"ultra":     ReasoningMax,
}

// reasoningKey normalizes a label for alias lookup: lowercase, with spaces,
// dashes and underscores removed ("Extra-High" and "extra_high" both match).
func reasoningKey(e ReasoningEffort) string {
	var b strings.Builder
	for _, r := range strings.ToLower(string(e)) {
		switch r {
		case ' ', '-', '_', '\t':
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Canonical folds e onto one of the portable levels. ok is false when the
// label is not recognized: OpenAI-compatible providers pass such values
// through verbatim (the gateway or backend knows them), while providers that
// must translate the effort into a budget fail with ErrUnsupported rather than
// silently dropping it.
func (e ReasoningEffort) Canonical() (level ReasoningEffort, ok bool) {
	level, ok = reasoningAliases[reasoningKey(e)]
	return level, ok
}

// ResponseFormatType selects how the model's final answer is constrained.
type ResponseFormatType string

const (
	// ResponseText is the default free-form output.
	ResponseText ResponseFormatType = "text"
	// ResponseJSON asks for syntactically valid JSON without a schema.
	ResponseJSON ResponseFormatType = "json"
	// ResponseJSONSchema constrains the output to Schema.
	ResponseJSONSchema ResponseFormatType = "json_schema"
)

// ResponseFormat constrains the shape of the model's final answer (structured
// output). Providers that cannot honor an explicitly requested format must
// fail the request with ErrUnsupported rather than silently ignore it.
type ResponseFormat struct {
	Type ResponseFormatType `json:"type"`
	// Name labels the schema (required by some providers for json_schema).
	Name string `json:"name,omitempty"`
	// Schema is the JSON Schema of the expected output (json_schema only).
	Schema JSONSchema `json:"schema,omitempty"`
	// Strict requests exact schema adherence where the provider supports it.
	Strict bool `json:"strict,omitempty"`
}

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
	// ResponseFormat constrains the model output (JSON mode / JSON Schema).
	ResponseFormat *ResponseFormat
	// PromptCache asks the provider to mark stable prefixes (system prompt,
	// tool schemas, conversation head) as cacheable. It is a hint: providers
	// with implicit caching (OpenAI) ignore it, providers with explicit
	// breakpoints (Anthropic cache_control) act on it. It never fails.
	PromptCache bool
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

// WithResponseFormat constrains the model output.
func WithResponseFormat(rf ResponseFormat) Option {
	return func(o *GenerateOptions) { o.ResponseFormat = &rf }
}

// WithJSONSchema constrains the output to a named JSON Schema (strict).
func WithJSONSchema(name string, schema JSONSchema) Option {
	return func(o *GenerateOptions) {
		o.ResponseFormat = &ResponseFormat{Type: ResponseJSONSchema, Name: name, Schema: schema, Strict: true}
	}
}

// WithPromptCache enables prompt-cache breakpoints on providers that support
// explicit caching.
func WithPromptCache() Option {
	return func(o *GenerateOptions) { o.PromptCache = true }
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
