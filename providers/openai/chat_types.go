package openai

import (
	"encoding/json"
	"fmt"

	"github.com/deepteams/gage"
)

// ---- Request encoding ----

func toChatMessages(system string, msgs []gage.Message, provider string) ([]map[string]any, error) {
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
				case gage.PartReasoning:
					// Skipped: Chat Completions has no reasoning replay mechanism.
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
			if len(toolCalls) > 0 {
				msg["tool_calls"] = toolCalls
			}
			// Never emit an assistant message with neither content nor
			// tool_calls (many servers reject it); fall back to "" content.
			if text != "" || len(toolCalls) == 0 {
				msg["content"] = text
			}
			out = append(out, msg)
		default:
			content, err := contentToChat(m.Content, provider)
			if err != nil {
				return nil, err
			}
			out = append(out, map[string]any{"role": string(m.Role), "content": content})
		}
	}
	return out, nil
}

// contentToChat renders user/system content. Text-only content collapses to a
// string; mixed content (with images or documents) uses the array form.
func contentToChat(parts []gage.ContentPart, provider string) (any, error) {
	textOnly := true
	for _, p := range parts {
		if p.Kind == gage.PartImage || p.Kind == gage.PartDocument {
			textOnly = false
		}
	}
	if textOnly {
		var s string
		for _, p := range parts {
			if p.Kind == gage.PartText {
				s += p.Text
			}
		}
		return s, nil
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
		case gage.PartDocument:
			if p.Document == nil {
				continue
			}
			if p.Document.Data == "" {
				if p.Document.URL != "" {
					// Chat Completions has no file-URL input; fail fast rather
					// than silently dropping the document.
					return nil, gage.Unsupported(provider, "document URL parts")
				}
				return nil, fmt.Errorf("%s: document part has neither url nor data", provider)
			}
			mediaType := p.Document.MediaType
			if mediaType == "" {
				mediaType = "application/pdf"
			}
			filename := p.Document.Filename
			if filename == "" {
				filename = "document.pdf"
			}
			arr = append(arr, map[string]any{
				"type": "file",
				"file": map[string]any{
					"filename":  filename,
					"file_data": "data:" + mediaType + ";base64," + p.Document.Data,
				},
			})
		}
	}
	return arr, nil
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

func applyChatOptions(body map[string]any, o gage.GenerateOptions, provider string) error {
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
		// Sent verbatim: the effort is an open string, so a gateway's own
		// levels (llm-router model profiles, vLLM, OpenRouter) reach the
		// backend unchanged.
		body["reasoning_effort"] = string(o.ReasoningEffort)
	}
	if rf := o.ResponseFormat; rf != nil {
		switch rf.Type {
		case "", gage.ResponseText:
			// Default free-form output; nothing to send.
		case gage.ResponseJSON:
			body["response_format"] = map[string]any{"type": "json_object"}
		case gage.ResponseJSONSchema:
			body["response_format"] = map[string]any{
				"type": "json_schema",
				"json_schema": map[string]any{
					"name":   rf.Name,
					"schema": rawSchema(rf.Schema),
					"strict": rf.Strict,
				},
			}
		default:
			return gage.Unsupported(provider, "response_format="+string(rf.Type))
		}
	}
	return nil
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
	// Error decodes in-stream {"error":{...}} payloads (OpenRouter, vLLM and
	// other OpenAI-compatible servers report mid-stream failures this way).
	Error *chatError `json:"error"`
}

type chatError struct {
	Message string          `json:"message"`
	Type    string          `json:"type"`
	Code    json.RawMessage `json:"code"` // int or string depending on server
}

// toAPIError converts an in-stream error payload into a *gage.APIError so
// callers can errors.Is-match ErrAuth/ErrRateLimited on numeric codes.
func (e *chatError) toAPIError(provider string) error {
	status := 0
	var i int
	if len(e.Code) > 0 && json.Unmarshal(e.Code, &i) == nil {
		status = i
	}
	return &gage.APIError{Provider: provider, Status: status, Body: e.Message}
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
