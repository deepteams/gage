package anthropic

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	"github.com/deepteams/gage"
)

// RedactedSignaturePrefix marks a gage.PartReasoning that preserves an
// Anthropic redacted_thinking block. Such parts have empty Text and a
// Signature of RedactedSignaturePrefix + <opaque data>; the stream pump
// produces them (as an EventReasoningDone with that Signature) and the encoder
// replays them as {"type":"redacted_thinking","data":<opaque data>} blocks.
const RedactedSignaturePrefix = "redacted:"

// buildBody maps a gage.Request onto a Messages API request body. structured
// reports whether output_format was used (the caller adds the beta header).
func (c *Client) buildBody(req gage.Request) (body []byte, structured bool, err error) {
	model := req.Model
	if model == "" {
		model = c.DefaultModel
	}
	if model == "" {
		return nil, false, fmt.Errorf("%s: no model specified", c.ProviderName)
	}
	maxTokens := req.Options.MaxTokens
	if maxTokens <= 0 {
		maxTokens = c.MaxTokens
	}
	if maxTokens <= 0 {
		maxTokens = DefaultMaxTokens
	}
	b := map[string]any{
		"model":      model,
		"max_tokens": maxTokens,
		"stream":     true,
		"messages":   toMessages(req.Messages),
	}
	if sys := systemBlocks(c.SystemPrefix, req.System); len(sys) > 0 {
		b["system"] = sys
	}
	if len(req.Tools) > 0 {
		b["tools"] = toTools(req.Tools)
	}
	applyOptions(b, req.Options)
	if rf := req.Options.ResponseFormat; rf != nil && rf.Type != "" && rf.Type != gage.ResponseText {
		if c.DisableResponseFormat {
			return nil, false, gage.Unsupported(c.ProviderName, "response_format")
		}
		// The Messages API only has schema-constrained output; there is no
		// schemaless JSON mode.
		if rf.Type != gage.ResponseJSONSchema {
			return nil, false, gage.Unsupported(c.ProviderName, "response_format="+string(rf.Type))
		}
		b["output_format"] = map[string]any{
			"type":   "json_schema",
			"schema": nonEmpty(json.RawMessage(rf.Schema)),
		}
		structured = true
	}
	maps.Copy(b, req.Options.Extra)
	if req.Options.PromptCache {
		applyPromptCache(b)
	}
	body, err = json.Marshal(b)
	return body, structured, err
}

// systemBlocks builds the system array: the prefix block first (claudecode's
// required spoof), then the caller's system prompt.
func systemBlocks(prefix, system string) []map[string]any {
	var blocks []map[string]any
	if prefix != "" {
		blocks = append(blocks, map[string]any{"type": "text", "text": prefix})
	}
	if system != "" {
		blocks = append(blocks, map[string]any{"type": "text", "text": system})
	}
	return blocks
}

func toMessages(msgs []gage.Message) []map[string]any {
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
	// Thinking blocks must be replayed ahead of the text/tool_use blocks of
	// the same message for interleaved-thinking tool use to be accepted.
	for _, p := range m.Content {
		if p.Kind != gage.PartReasoning {
			continue
		}
		if strings.HasPrefix(p.Signature, RedactedSignaturePrefix) {
			content = append(content, map[string]any{
				"type": "redacted_thinking",
				"data": strings.TrimPrefix(p.Signature, RedactedSignaturePrefix),
			})
			continue
		}
		content = append(content, map[string]any{
			"type":      "thinking",
			"thinking":  p.Text,
			"signature": p.Signature,
		})
	}
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

func toTools(tools []gage.ToolSchema) []map[string]any {
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

func applyOptions(body map[string]any, o gage.GenerateOptions) {
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
		body["tool_choice"] = toToolChoice(*o.ToolChoice)
	}
}

func toToolChoice(tc gage.ToolChoice) map[string]any {
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

// applyPromptCache sets ephemeral cache_control breakpoints on the last system
// block, the last tool schema, and the last content block of the final
// message, marking the whole request prefix cacheable.
func applyPromptCache(body map[string]any) {
	cc := func() map[string]any { return map[string]any{"type": "ephemeral"} }
	if sys, ok := body["system"].([]map[string]any); ok && len(sys) > 0 {
		sys[len(sys)-1]["cache_control"] = cc()
	}
	if tools, ok := body["tools"].([]map[string]any); ok && len(tools) > 0 {
		tools[len(tools)-1]["cache_control"] = cc()
	}
	msgs, ok := body["messages"].([]map[string]any)
	if !ok || len(msgs) == 0 {
		return
	}
	last := msgs[len(msgs)-1]
	switch content := last["content"].(type) {
	case []map[string]any:
		if len(content) > 0 {
			content[len(content)-1]["cache_control"] = cc()
		}
	case string:
		// Plain-string content has no place for cache_control; switch to the
		// block form.
		last["content"] = []map[string]any{{"type": "text", "text": content, "cache_control": cc()}}
	}
}

func nonEmpty(r json.RawMessage) json.RawMessage {
	if len(r) == 0 {
		return json.RawMessage("{}")
	}
	return r
}
