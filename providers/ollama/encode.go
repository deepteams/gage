package ollama

import (
	"encoding/json"

	"github.com/deepteams/gage"
)

// buildNativeBody maps a gage.Request onto Ollama's /api/chat request body.
func buildNativeBody(req gage.Request, defaultModel string) ([]byte, error) {
	model := req.Model
	if model == "" {
		model = defaultModel
	}
	body := map[string]any{
		"model":    model,
		"messages": toNativeMessages(req.System, req.Messages),
		"stream":   true,
	}
	if len(req.Tools) > 0 {
		body["tools"] = toNativeTools(req.Tools)
	}
	if opts := toNativeOptions(req.Options); len(opts) > 0 {
		body["options"] = opts
	}
	return json.Marshal(body)
}

func toNativeMessages(system string, msgs []gage.Message) []map[string]any {
	out := make([]map[string]any, 0, len(msgs)+1)
	if system != "" {
		out = append(out, map[string]any{"role": "system", "content": system})
	}
	for _, m := range msgs {
		switch m.Role {
		case gage.RoleTool:
			for _, p := range m.Content {
				if p.Kind == gage.PartToolResult && p.ToolResult != nil {
					out = append(out, map[string]any{
						"role":    "tool",
						"content": p.ToolResult.Text(),
					})
				}
			}
		case gage.RoleAssistant:
			msg := map[string]any{"role": "assistant", "content": m.Text()}
			var toolCalls []map[string]any
			for _, p := range m.Content {
				if p.Kind == gage.PartToolUse && p.ToolCall != nil {
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
