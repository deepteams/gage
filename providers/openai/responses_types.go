package openai

import (
	"encoding/json"

	"github.com/deepteams/gage"
)

// ---- Request encoding (Responses API "input" items) ----

func toResponsesInput(msgs []gage.Message) []map[string]any {
	var out []map[string]any
	for _, m := range msgs {
		switch m.Role {
		case gage.RoleTool:
			for _, p := range m.Content {
				if p.Kind == gage.PartToolResult && p.ToolResult != nil {
					out = append(out, map[string]any{
						"type":    "function_call_output",
						"call_id": p.ToolResult.CallID,
						"output":  p.ToolResult.Text(),
					})
				}
			}
		case gage.RoleAssistant:
			// Emit text as an output message and each tool use as a function_call.
			if txt := m.Text(); txt != "" {
				out = append(out, map[string]any{
					"type":    "message",
					"role":    "assistant",
					"content": []map[string]any{{"type": "output_text", "text": txt}},
				})
			}
			for _, p := range m.Content {
				if p.Kind == gage.PartToolUse && p.ToolCall != nil {
					out = append(out, map[string]any{
						"type":      "function_call",
						"call_id":   p.ToolCall.ID,
						"name":      p.ToolCall.Name,
						"arguments": string(nonEmptyRaw(p.ToolCall.Input)),
					})
				}
			}
		default:
			out = append(out, map[string]any{
				"type":    "message",
				"role":    string(m.Role),
				"content": []map[string]any{{"type": "input_text", "text": m.Text()}},
			})
		}
	}
	return out
}

func toResponsesTools(tools []gage.ToolSchema) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		out = append(out, map[string]any{
			"type":        "function",
			"name":        t.Name,
			"description": t.Description,
			"parameters":  rawSchema(t.Parameters),
		})
	}
	return out
}

func nonEmptyRaw(r json.RawMessage) json.RawMessage {
	if len(r) == 0 {
		return json.RawMessage("{}")
	}
	return r
}

// ---- Response decoding (streamed events) ----

type responsesEvent struct {
	Type      string           `json:"type"`
	Delta     string           `json:"delta"`
	ItemID    string           `json:"item_id"`
	Arguments string           `json:"arguments"`
	Item      *responsesItem   `json:"item"`
	Response  *responsesResult `json:"response"`
}

type responsesItem struct {
	Type   string `json:"type"`
	ID     string `json:"id"`
	CallID string `json:"call_id"`
	Name   string `json:"name"`
}

type responsesResult struct {
	Usage *responsesUsage `json:"usage"`
	Error *responsesError `json:"error"`
}

type responsesError struct {
	Message string `json:"message"`
}

type responsesUsage struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	InputTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	OutputTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

func (u responsesUsage) toGage() gage.Usage {
	return gage.Usage{
		InputTokens:     u.InputTokens,
		OutputTokens:    u.OutputTokens,
		ReasoningTokens: u.OutputTokensDetails.ReasoningTokens,
		CacheReadTokens: u.InputTokensDetails.CachedTokens,
	}
}
