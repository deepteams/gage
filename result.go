package gage

// StopReason explains why a generation ended. Providers normalize their wire
// values onto these constants; unknown values pass through verbatim.
type StopReason string

const (
	// StopEndTurn is the normal completion of an assistant message.
	StopEndTurn StopReason = "end_turn"
	// StopToolUse means the model stopped to call one or more tools.
	StopToolUse StopReason = "tool_use"
	// StopMaxTokens means generation was truncated by the token limit.
	StopMaxTokens StopReason = "max_tokens"
	// StopSequence means a configured stop sequence was hit.
	StopSequence StopReason = "stop_sequence"
	// StopContentFilter means the provider suppressed the output.
	StopContentFilter StopReason = "content_filter"
	// StopRefusal means the model refused to answer.
	StopRefusal StopReason = "refusal"
)

// Truncated reports whether the message ended before the model was done.
func (s StopReason) Truncated() bool { return s == StopMaxTokens }

// Result summarizes a completed agent run. It travels on the terminal
// EventDone so streaming consumers get it for free, and is also returned by
// the blocking helpers.
type Result struct {
	// Messages is the full conversation: the input messages followed by every
	// assistant message and tool result produced during the run.
	Messages []Message `json:"messages"`
	// Text is the text of the final assistant message.
	Text string `json:"text"`
	// StopReason is the stop reason of the final assistant message.
	StopReason StopReason `json:"stop_reason,omitempty"`
	// Usage is the token usage accumulated across every provider call of the run.
	Usage Usage `json:"usage"`
	// Turns is the number of provider calls the run made.
	Turns int `json:"turns"`
}

// LastAssistant returns the final assistant message of the run, if any.
func (r *Result) LastAssistant() (Message, bool) {
	for i := len(r.Messages) - 1; i >= 0; i-- {
		if r.Messages[i].Role == RoleAssistant {
			return r.Messages[i], true
		}
	}
	return Message{}, false
}
