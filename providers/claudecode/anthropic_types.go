package claudecode

import (
	"encoding/json"

	"github.com/deepteams/gage"
)

// ---- Request encoding (Anthropic Messages API) ----

func toAnthropicMessages(msgs []gage.Message) []map[string]any {
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case gage.RoleTool:
			var content []map[string]any
			for _, p := range m.Content {
				if p.Kind == gage.PartToolResult && p.ToolResult != nil {
					content = append(content, map[string]any{
						"type":        "tool_result",
						"tool_use_id": p.ToolResult.CallID,
						"content":     p.ToolResult.Text(),
						"is_error":    p.ToolResult.IsError,
					})
				}
			}
			out = append(out, map[string]any{"role": "user", "content": content})
		case gage.RoleAssistant:
			out = append(out, map[string]any{"role": "assistant", "content": assistantContent(m)})
		default:
			out = append(out, map[string]any{"role": "user", "content": userContent(m)})
		}
	}
	return out
}

func assistantContent(m gage.Message) []map[string]any {
	var content []map[string]any
	for _, p := range m.Content {
		switch p.Kind {
		case gage.PartText:
			content = append(content, map[string]any{"type": "text", "text": p.Text})
		case gage.PartToolUse:
			if p.ToolCall != nil {
				var input any
				_ = json.Unmarshal(nonEmpty(p.ToolCall.Input), &input)
				content = append(content, map[string]any{
					"type":  "tool_use",
					"id":    p.ToolCall.ID,
					"name":  p.ToolCall.Name,
					"input": input,
				})
			}
		}
	}
	return content
}

func userContent(m gage.Message) any {
	// Text-only content can be sent as a plain string.
	hasNonText := false
	for _, p := range m.Content {
		if p.Kind != gage.PartText {
			hasNonText = true
		}
	}
	if !hasNonText {
		return m.Text()
	}
	var content []map[string]any
	for _, p := range m.Content {
		switch p.Kind {
		case gage.PartText:
			content = append(content, map[string]any{"type": "text", "text": p.Text})
		case gage.PartImage:
			if p.Image == nil {
				continue
			}
			if p.Image.URL != "" {
				content = append(content, map[string]any{
					"type":   "image",
					"source": map[string]any{"type": "url", "url": p.Image.URL},
				})
			} else if p.Image.Data != "" {
				content = append(content, map[string]any{
					"type": "image",
					"source": map[string]any{
						"type":       "base64",
						"media_type": p.Image.MediaType,
						"data":       p.Image.Data,
					},
				})
			}
		}
	}
	return content
}

func toAnthropicTools(tools []gage.ToolSchema) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		var schema any
		_ = json.Unmarshal(nonEmpty(t.Parameters), &schema)
		out = append(out, map[string]any{
			"name":         t.Name,
			"description":  t.Description,
			"input_schema": schema,
		})
	}
	return out
}

func applyAnthropicOptions(body map[string]any, o gage.GenerateOptions) {
	if o.Temperature != nil {
		body["temperature"] = *o.Temperature
	}
	if o.TopP != nil {
		body["top_p"] = *o.TopP
	}
	if len(o.StopSequences) > 0 {
		body["stop_sequences"] = o.StopSequences
	}
	if o.ReasoningEffort != gage.ReasoningNone {
		// Map effort to a thinking token budget.
		budget := map[gage.ReasoningEffort]int{
			gage.ReasoningLow:    2048,
			gage.ReasoningMedium: 8192,
			gage.ReasoningHigh:   16384,
		}[o.ReasoningEffort]
		if budget > 0 {
			body["thinking"] = map[string]any{"type": "enabled", "budget_tokens": budget}
		}
	}
	if o.ToolChoice != nil {
		body["tool_choice"] = toAnthropicToolChoice(*o.ToolChoice)
	}
}

func toAnthropicToolChoice(tc gage.ToolChoice) map[string]any {
	switch tc.Mode {
	case gage.ToolChoiceNone:
		return map[string]any{"type": "none"}
	case gage.ToolChoiceRequired:
		return map[string]any{"type": "any"}
	case gage.ToolChoiceTool:
		return map[string]any{"type": "tool", "name": tc.Name}
	default:
		return map[string]any{"type": "auto"}
	}
}

func nonEmpty(r json.RawMessage) json.RawMessage {
	if len(r) == 0 {
		return json.RawMessage("{}")
	}
	return r
}

// ---- Response decoding (streamed events) ----

type anthropicEvent struct {
	Type         string            `json:"type"`
	Index        int               `json:"index"`
	Message      *anthropicMessage `json:"message"`
	ContentBlock struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"content_block"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage *anthropicUsage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type anthropicMessage struct {
	Usage *anthropicUsage `json:"usage"`
}

type anthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

func (u anthropicUsage) toGage() gage.Usage {
	return gage.Usage{
		InputTokens:      u.InputTokens,
		OutputTokens:     u.OutputTokens,
		CacheReadTokens:  u.CacheReadInputTokens,
		CacheWriteTokens: u.CacheCreationInputTokens,
	}
}
