package gemini

import (
	"encoding/json"
	"maps"

	"github.com/deepteams/gage"
)

// buildBody maps a gage.Request onto a generateContent request body. It fails
// fast with gage.ErrUnsupported for explicitly requested options Gemini
// cannot honor. PromptCache is ignored by design: Gemini caches implicitly
// and the option is a hint per the library contract.
func (c *Client) buildBody(req gage.Request) ([]byte, error) {
	b := map[string]any{"contents": toContents(req.Messages)}
	if req.System != "" {
		b["systemInstruction"] = map[string]any{
			"parts": []map[string]any{{"text": req.System}},
		}
	}
	if len(req.Tools) > 0 {
		b["tools"] = toTools(req.Tools)
	}
	if tc := req.Options.ToolChoice; tc != nil {
		fcc, err := toFunctionCallingConfig(c.ProviderName, *tc)
		if err != nil {
			return nil, err
		}
		b["toolConfig"] = map[string]any{"functionCallingConfig": fcc}
	}
	gc, err := generationConfig(c.ProviderName, req.Options)
	if err != nil {
		return nil, err
	}
	if len(gc) > 0 {
		b["generationConfig"] = gc
	}
	maps.Copy(b, req.Options.Extra)
	return json.Marshal(b)
}

// toContents maps the conversation onto Gemini contents. Tool results are
// correlated back to their function name through a callID→name map built from
// the assistant tool-use parts seen earlier in the history (Gemini
// functionResponse parts carry the function name, not a call id).
func toContents(msgs []gage.Message) []map[string]any {
	names := map[string]string{} // ToolCall.ID → tool name
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		var (
			role  string
			parts []map[string]any
		)
		switch m.Role {
		case gage.RoleTool:
			role = "user"
			parts = toolResultParts(m, names)
		case gage.RoleAssistant:
			role = "model"
			parts = assistantParts(m, names)
		default:
			role = "user"
			parts = userParts(m)
		}
		if len(parts) == 0 {
			continue // Gemini rejects contents with empty parts.
		}
		out = append(out, map[string]any{"role": role, "parts": parts})
	}
	return out
}

func toolResultParts(m gage.Message, names map[string]string) []map[string]any {
	var parts []map[string]any
	for _, p := range m.Content {
		if p.Kind != gage.PartToolResult || p.ToolResult == nil {
			continue
		}
		name := names[p.ToolResult.CallID]
		if name == "" {
			// No pending call recorded; fall back to the call id so the
			// request stays well-formed.
			name = p.ToolResult.CallID
		}
		parts = append(parts, map[string]any{
			"functionResponse": map[string]any{
				"name": name,
				// The response must be a JSON object.
				"response": map[string]any{"result": p.ToolResult.Text()},
			},
		})
	}
	return parts
}

func assistantParts(m gage.Message, names map[string]string) []map[string]any {
	var parts []map[string]any
	for _, p := range m.Content {
		switch p.Kind {
		case gage.PartText:
			parts = append(parts, map[string]any{"text": p.Text})
		case gage.PartReasoning:
			// Only signed thoughts are replayable; unsigned reasoning parts
			// are skipped (Gemini would reject a thought without its
			// signature in a multi-turn function-calling exchange).
			if p.Signature == "" {
				continue
			}
			parts = append(parts, map[string]any{
				"text":             p.Text,
				"thought":          true,
				"thoughtSignature": p.Signature,
			})
		case gage.PartToolUse:
			if p.ToolCall == nil {
				continue
			}
			names[p.ToolCall.ID] = p.ToolCall.Name
			var args any
			_ = json.Unmarshal(nonEmpty(p.ToolCall.Input), &args)
			parts = append(parts, map[string]any{
				"functionCall": map[string]any{
					"name": p.ToolCall.Name,
					"args": args,
				},
			})
		}
	}
	return parts
}

func userParts(m gage.Message) []map[string]any {
	var parts []map[string]any
	for _, p := range m.Content {
		switch p.Kind {
		case gage.PartText:
			parts = append(parts, map[string]any{"text": p.Text})
		case gage.PartImage:
			if p.Image == nil {
				continue
			}
			parts = append(parts, mediaPart(p.Image.URL, p.Image.MediaType, p.Image.Data))
		case gage.PartDocument:
			if p.Document == nil {
				continue
			}
			parts = append(parts, mediaPart(p.Document.URL, p.Document.MediaType, p.Document.Data))
		}
	}
	return parts
}

// mediaPart builds an inlineData part for base64 bytes or a fileData part for
// a URL reference. Images and documents share the same wire shape; only the
// mime type differs.
func mediaPart(url, mediaType, data string) map[string]any {
	if url != "" {
		fd := map[string]any{"fileUri": url}
		if mediaType != "" {
			fd["mimeType"] = mediaType
		}
		return map[string]any{"fileData": fd}
	}
	return map[string]any{
		"inlineData": map[string]any{"mimeType": mediaType, "data": data},
	}
}

func toTools(tools []gage.ToolSchema) []map[string]any {
	decls := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		var params any
		_ = json.Unmarshal(nonEmpty(t.Parameters), &params)
		decls = append(decls, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"parameters":  params,
		})
	}
	return []map[string]any{{"functionDeclarations": decls}}
}

func toFunctionCallingConfig(provider string, tc gage.ToolChoice) (map[string]any, error) {
	switch tc.Mode {
	case gage.ToolChoiceAuto, "":
		return map[string]any{"mode": "AUTO"}, nil
	case gage.ToolChoiceNone:
		return map[string]any{"mode": "NONE"}, nil
	case gage.ToolChoiceRequired:
		return map[string]any{"mode": "ANY"}, nil
	case gage.ToolChoiceTool:
		return map[string]any{"mode": "ANY", "allowedFunctionNames": []string{tc.Name}}, nil
	default:
		return nil, gage.Unsupported(provider, "tool_choice="+string(tc.Mode))
	}
}

// thinkingBudgets maps the portable effort levels onto Gemini thinking-token
// budgets. The values are a documented heuristic, mirroring how the Anthropic
// adapter maps effort to budget_tokens.
var thinkingBudgets = map[gage.ReasoningEffort]int{
	gage.ReasoningLow:    1024,
	gage.ReasoningMedium: 8192,
	gage.ReasoningHigh:   24576,
}

func generationConfig(provider string, o gage.GenerateOptions) (map[string]any, error) {
	gc := map[string]any{}
	if o.Temperature != nil {
		gc["temperature"] = *o.Temperature
	}
	if o.TopP != nil {
		gc["topP"] = *o.TopP
	}
	if o.MaxTokens > 0 {
		gc["maxOutputTokens"] = o.MaxTokens
	}
	if len(o.StopSequences) > 0 {
		gc["stopSequences"] = o.StopSequences
	}
	if rf := o.ResponseFormat; rf != nil && rf.Type != "" && rf.Type != gage.ResponseText {
		switch rf.Type {
		case gage.ResponseJSON:
			gc["responseMimeType"] = "application/json"
		case gage.ResponseJSONSchema:
			gc["responseMimeType"] = "application/json"
			// The schema is passed through verbatim: responseJsonSchema
			// accepts standard JSON Schema.
			gc["responseJsonSchema"] = json.RawMessage(nonEmpty(rf.Schema))
		default:
			return nil, gage.Unsupported(provider, "response_format="+string(rf.Type))
		}
	}
	if o.ReasoningEffort != gage.ReasoningNone {
		budget, ok := thinkingBudgets[o.ReasoningEffort]
		if !ok {
			return nil, gage.Unsupported(provider, "reasoning_effort="+string(o.ReasoningEffort))
		}
		gc["thinkingConfig"] = map[string]any{
			"includeThoughts": true,
			"thinkingBudget":  budget,
		}
	}
	return gc, nil
}

func nonEmpty(r json.RawMessage) json.RawMessage {
	if len(r) == 0 {
		return json.RawMessage("{}")
	}
	return r
}

// countBody builds the countTokens request body: the encoded contents plus
// systemInstruction and tools (they all contribute input tokens).
func (c *Client) countBody(req gage.Request) ([]byte, error) {
	b := map[string]any{"contents": toContents(req.Messages)}
	if req.System != "" {
		b["systemInstruction"] = map[string]any{
			"parts": []map[string]any{{"text": req.System}},
		}
	}
	if len(req.Tools) > 0 {
		b["tools"] = toTools(req.Tools)
	}
	return json.Marshal(b)
}
