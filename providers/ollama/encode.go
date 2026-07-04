package ollama

import (
	"encoding/json"
	"fmt"
	"maps"

	"github.com/deepteams/gage"
)

// buildNativeBody maps a gage.Request onto Ollama's /api/chat request body.
func buildNativeBody(req gage.Request, defaultModel string) ([]byte, error) {
	model := req.Model
	if model == "" {
		model = defaultModel
	}
	if model == "" {
		return nil, fmt.Errorf("ollama: no model specified")
	}
	body := map[string]any{
		"model":    model,
		"messages": toNativeMessages(req.System, req.Messages),
		"stream":   true,
	}
	if len(req.Tools) > 0 {
		body["tools"] = toNativeTools(req.Tools)
	}
	if req.Options.ToolChoice != nil {
		// Ollama's native API has no tool_choice parameter; fail fast rather
		// than silently dropping the constraint.
		return nil, gage.Unsupported("ollama", "tool_choice")
	}
	if req.Options.ReasoningEffort != gage.ReasoningNone {
		// Ollama's thinking switch is boolean; any requested effort enables it.
		body["think"] = true
	}
	if rf := req.Options.ResponseFormat; rf != nil {
		switch rf.Type {
		case "", gage.ResponseText:
			// Default free-form output; nothing to send.
		case gage.ResponseJSON:
			body["format"] = "json"
		case gage.ResponseJSONSchema:
			body["format"] = nonEmpty(json.RawMessage(rf.Schema))
		default:
			return nil, gage.Unsupported("ollama", "response_format="+string(rf.Type))
		}
	}
	if opts := toNativeOptions(req.Options); len(opts) > 0 {
		body["options"] = opts
	}
	maps.Copy(body, req.Options.Extra)
	return json.Marshal(body)
}

func toNativeMessages(system string, msgs []gage.Message) []map[string]any {
	out := make([]map[string]any, 0, len(msgs)+1)
	if system != "" {
		out = append(out, map[string]any{"role": "system", "content": system})
	}
	// Ollama correlates tool results to calls by tool name, not id; remember
	// each call's name so results can carry it.
	callNames := map[string]string{}
	for _, m := range msgs {
		switch m.Role {
		case gage.RoleTool:
			for _, p := range m.Content {
				if p.Kind == gage.PartToolResult && p.ToolResult != nil {
					msg := map[string]any{
						"role":    "tool",
						"content": p.ToolResult.Text(),
					}
					// tool_name keeps two results in one turn distinguishable.
					// Prefer the name of the originating call; fall back to
					// the message name, then the raw call id.
					switch {
					case callNames[p.ToolResult.CallID] != "":
						msg["tool_name"] = callNames[p.ToolResult.CallID]
					case m.Name != "":
						msg["tool_name"] = m.Name
					case p.ToolResult.CallID != "":
						msg["tool_name"] = p.ToolResult.CallID
					}
					out = append(out, msg)
				}
			}
		case gage.RoleAssistant:
			// m.Text() ignores PartReasoning: Ollama has no reasoning replay
			// mechanism, so reasoning parts are skipped.
			msg := map[string]any{"role": "assistant", "content": m.Text()}
			var toolCalls []map[string]any
			for _, p := range m.Content {
				if p.Kind == gage.PartToolUse && p.ToolCall != nil {
					callNames[p.ToolCall.ID] = p.ToolCall.Name
					var args any
					_ = json.Unmarshal(nonEmpty(p.ToolCall.Input), &args)
					toolCalls = append(toolCalls, map[string]any{
						"function": map[string]any{
							"name":      p.ToolCall.Name,
							"arguments": args,
						},
					})
				}
			}
			if len(toolCalls) > 0 {
				msg["tool_calls"] = toolCalls
			}
			out = append(out, msg)
		default:
			out = append(out, map[string]any{"role": string(m.Role), "content": m.Text()})
		}
	}
	return out
}

func toNativeTools(tools []gage.ToolSchema) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		var params any
		_ = json.Unmarshal(nonEmpty(t.Parameters), &params)
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  params,
			},
		})
	}
	return out
}

func toNativeOptions(o gage.GenerateOptions) map[string]any {
	opts := map[string]any{}
	if o.Temperature != nil {
		opts["temperature"] = *o.Temperature
	}
	if o.TopP != nil {
		opts["top_p"] = *o.TopP
	}
	if o.MaxTokens > 0 {
		opts["num_predict"] = o.MaxTokens
	}
	if len(o.StopSequences) > 0 {
		opts["stop"] = o.StopSequences
	}
	return opts
}

func nonEmpty(r json.RawMessage) json.RawMessage {
	if len(r) == 0 {
		return json.RawMessage("{}")
	}
	return r
}
