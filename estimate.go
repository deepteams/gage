package gage

// Token estimation is a provider-agnostic heuristic (roughly 4 bytes of text
// per token, a flat cost per image). It exists so callers — and the agent's
// proactive compaction — can act on conversation size before a provider call,
// without a tokenizer dependency. Reported Usage remains the source of truth
// after each call.

// estimatedImageTokens is the flat heuristic cost of one image part.
const estimatedImageTokens = 1500

// estimatedMessageOverhead accounts for per-message wire framing.
const estimatedMessageOverhead = 4

// EstimateTextTokens roughly estimates the token count of a piece of text.
func EstimateTextTokens(s string) int { return len(s) / 4 }

// EstimateTokens roughly estimates the total token count of a conversation.
// It is intentionally conservative and provider-agnostic: use it for
// thresholds (compaction, budgets), never for billing.
func EstimateTokens(msgs []Message) int {
	n := 0
	for _, m := range msgs {
		n += estimatedMessageOverhead + estimateParts(m.Content)
	}
	return n
}

func estimateParts(parts []ContentPart) int {
	n := 0
	for _, p := range parts {
		switch p.Kind {
		case PartText, PartReasoning:
			n += EstimateTextTokens(p.Text)
		case PartImage:
			n += estimatedImageTokens
		case PartToolUse:
			if p.ToolCall != nil {
				n += EstimateTextTokens(p.ToolCall.Name) + EstimateTextTokens(string(p.ToolCall.Input))
			}
		case PartToolResult:
			if p.ToolResult != nil {
				n += estimateParts(p.ToolResult.Content)
			}
		}
	}
	return n
}
