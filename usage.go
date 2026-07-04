package gage

// Usage reports token accounting for a generation. Fields are zero when the
// provider does not report them.
type Usage struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	ReasoningTokens  int `json:"reasoning_tokens,omitempty"`
	CacheReadTokens  int `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
}

// Add returns the element-wise sum of two Usage values. It is used to
// accumulate usage across the turns of an agentic loop.
func (u Usage) Add(o Usage) Usage {
	return Usage{
		InputTokens:      u.InputTokens + o.InputTokens,
		OutputTokens:     u.OutputTokens + o.OutputTokens,
		ReasoningTokens:  u.ReasoningTokens + o.ReasoningTokens,
		CacheReadTokens:  u.CacheReadTokens + o.CacheReadTokens,
		CacheWriteTokens: u.CacheWriteTokens + o.CacheWriteTokens,
	}
}

// Total returns the sum of input and output tokens (reasoning included in
// output by most providers).
func (u Usage) Total() int { return u.InputTokens + u.OutputTokens }
