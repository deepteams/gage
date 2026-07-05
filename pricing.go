package gage

// Pricing holds a model's USD rates per million tokens. Zero-valued fields
// simply contribute nothing, so a table can fill only the rates it knows.
// The pricing sub-package ships a dated snapshot for common models; rates
// drift, so treat any built-in table as a default to override, never as a
// billing source of truth.
type Pricing struct {
	// InputPerMTok is the rate for non-cached input tokens.
	InputPerMTok float64 `json:"input_per_mtok,omitempty"`
	// OutputPerMTok is the rate for output tokens (reasoning tokens are billed
	// as output by providers that report them).
	OutputPerMTok float64 `json:"output_per_mtok,omitempty"`
	// CacheReadPerMTok is the rate for prompt-cache reads.
	CacheReadPerMTok float64 `json:"cache_read_per_mtok,omitempty"`
	// CacheWritePerMTok is the rate for prompt-cache writes.
	CacheWritePerMTok float64 `json:"cache_write_per_mtok,omitempty"`
}

// Cost returns the USD cost of u at rates p. Usage fields a provider does not
// report are zero and cost nothing.
func (p Pricing) Cost(u Usage) float64 {
	const mtok = 1e6
	return float64(u.InputTokens)*p.InputPerMTok/mtok +
		float64(u.OutputTokens)*p.OutputPerMTok/mtok +
		float64(u.CacheReadTokens)*p.CacheReadPerMTok/mtok +
		float64(u.CacheWriteTokens)*p.CacheWritePerMTok/mtok
}
