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
			// Replay signed reasoning items first so the server restores the
			// reasoning context ahead of the turn's message/function calls.
			// Best effort: the original item id and summary are not retained;
			// the API accepts encrypted_content-only reasoning items when
			// store=false. Unsigned reasoning parts are skipped (there is
			// nothing the server would accept back).
			for _, p := range m.Content {
				if p.Kind == gage.PartReasoning && p.Signature != "" {
					out = append(out, map[string]any{
						"type":              "reasoning",
						"encrypted_content": p.Signature,
						"summary":           []any{},
					})
				}
			}
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
				"content": contentToResponsesInput(m.Content),
			})
		}
	}
	return out
}

func contentToResponsesInput(parts []gage.ContentPart) []map[string]any {
	content := make([]map[string]any, 0, len(parts))
	for _, p := range parts {
		switch p.Kind {
		case gage.PartText:
			content = append(content, map[string]any{"type": "input_text", "text": p.Text})
		case gage.PartImage:
			if p.Image == nil {
				continue
			}
			url := p.Image.URL
			if url == "" && p.Image.Data != "" {
				url = "data:" + p.Image.MediaType + ";base64," + p.Image.Data
			}
			if url != "" {
				content = append(content, map[string]any{"type": "input_image", "image_url": url})
			}
		}
	}
	if len(content) == 0 {
		content = append(content, map[string]any{"type": "input_text", "text": ""})
	}
	return content
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

func toResponsesToolChoice(tc gage.ToolChoice) any {
	switch tc.Mode {
	case gage.ToolChoiceNone:
		return "none"
	case gage.ToolChoiceRequired:
		return "required"
	case gage.ToolChoiceTool:
		return map[string]any{"type": "function", "name": tc.Name}
	default:
		return "auto"
	}
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
	// EncryptedContent is the opaque replay token of a reasoning item,
	// present when the request included "reasoning.encrypted_content".
	EncryptedContent string `json:"encrypted_content"`
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
