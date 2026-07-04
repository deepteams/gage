package openai

import (
	"encoding/json"

	"github.com/deepteams/gage"
)

// ---- Request encoding ----

func toChatMessages(system string, msgs []gage.Message) []map[string]any {
	out := make([]map[string]any, 0, len(msgs)+1)
	if system != "" {
		out = append(out, map[string]any{"role": "system", "content": system})
	}
	for _, m := range msgs {
		switch m.Role {
		case gage.RoleTool:
			// Each tool result becomes a separate "tool" message.
			for _, p := range m.Content {
				if p.Kind == gage.PartToolResult && p.ToolResult != nil {
					out = append(out, map[string]any{
						"role":         "tool",
						"tool_call_id": p.ToolResult.CallID,
						"content":      p.ToolResult.Text(),
					})
				}
			}
		case gage.RoleAssistant:
			msg := map[string]any{"role": "assistant"}
			var text string
			var toolCalls []map[string]any
			for _, p := range m.Content {
				switch p.Kind {
				case gage.PartText:
					text += p.Text
				case gage.PartToolUse:
					if p.ToolCall != nil {
						toolCalls = append(toolCalls, map[string]any{
							"id":   p.ToolCall.ID,
							"type": "function",
							"function": map[string]any{
								"name":      p.ToolCall.Name,
								"arguments": string(rawOrEmpty(p.ToolCall.Input)),
							},
						})
					}
				}
			}
			if text != "" {
				msg["content"] = text
			}
			if len(toolCalls) > 0 {
				msg["tool_calls"] = toolCalls
			}
			out = append(out, msg)
		default:
			out = append(out, map[string]any{"role": string(m.Role), "content": contentToChat(m.Content)})
		}
	}
	return out
}

// contentToChat renders user/system content. Text-only content collapses to a
// string; mixed content (with images) uses the array form.
func contentToChat(parts []gage.ContentPart) any {
	hasImage := false
	for _, p := range parts {
		if p.Kind == gage.PartImage {
			hasImage = true
		}
	}
	if !hasImage {
		var s string
		for _, p := range parts {
			if p.Kind == gage.PartText {
				s += p.Text
			}
		}
		return s
	}
	arr := make([]map[string]any, 0, len(parts))
	for _, p := range parts {
		switch p.Kind {
		case gage.PartText:
			arr = append(arr, map[string]any{"type": "text", "text": p.Text})
		case gage.PartImage:
			if p.Image == nil {
				continue
			}
			url := p.Image.URL
			if url == "" && p.Image.Data != "" {
				url = "data:" + p.Image.MediaType + ";base64," + p.Image.Data
			}
			arr = append(arr, map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}})
		}
	}
	return arr
}

func toChatTools(tools []gage.ToolSchema) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  rawSchema(t.Parameters),
			},
		})
	}
	return out
}

func applyChatOptions(body map[string]any, o gage.GenerateOptions) {
	if o.Temperature != nil {
		body["temperature"] = *o.Temperature
	}
	if o.TopP != nil {
		body["top_p"] = *o.TopP
	}
	if o.MaxTokens > 0 {
		body["max_tokens"] = o.MaxTokens
	}
	if len(o.StopSequences) > 0 {
		body["stop"] = o.StopSequences
	}
	if o.ToolChoice != nil {
		body["tool_choice"] = toChatToolChoice(*o.ToolChoice)
	}
	if o.ReasoningEffort != gage.ReasoningNone {
		body["reasoning_effort"] = string(o.ReasoningEffort)
	}
}

func toChatToolChoice(tc gage.ToolChoice) any {
	switch tc.Mode {
	case gage.ToolChoiceNone:
		return "none"
	case gage.ToolChoiceRequired:
		return "required"
	case gage.ToolChoiceTool:
		return map[string]any{"type": "function", "function": map[string]any{"name": tc.Name}}
	default:
		return "auto"
	}
}

func rawSchema(s gage.JSONSchema) json.RawMessage {
	if len(s) == 0 {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return json.RawMessage(s)
}

func rawOrEmpty(r json.RawMessage) json.RawMessage {
	if len(r) == 0 {
		return json.RawMessage(`{}`)
	}
	return r
}

// ---- Response decoding ----

type chatChunk struct {
	Choices []chatChoice `json:"choices"`
	Usage   *chatUsage   `json:"usage"`
}

type chatChoice struct {
	Delta        chatDelta `json:"delta"`
	FinishReason string    `json:"finish_reason"`
}

type chatDelta struct {
	Content          string         `json:"content"`
	Reasoning        string         `json:"reasoning"`
	ReasoningContent string         `json:"reasoning_content"`
	ToolCalls        []chatToolCall `json:"tool_calls"`
}

type chatToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type chatUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	PromptTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

func (u chatUsage) toGage() gage.Usage {
	return gage.Usage{
		InputTokens:     u.PromptTokens,
		OutputTokens:    u.CompletionTokens,
		ReasoningTokens: u.CompletionTokensDetails.ReasoningTokens,
		CacheReadTokens: u.PromptTokensDetails.CachedTokens,
	}
}
