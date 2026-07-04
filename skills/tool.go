package skills

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/deepteams/gage"
	"github.com/deepteams/gage/internal/jsonschema"
)

// NewTool returns the "skill" tool, which loads a named skill's full
// instructions on demand. Register it on an agent whose system prompt includes
// Set.SystemPrompt so the model knows which skills exist.
func NewTool(set *Set) gage.Tool {
	return &skillTool{set: set}
}

type skillTool struct{ set *Set }

func (t *skillTool) Name() string { return "skill" }
func (t *skillTool) Description() string {
	return "Load the full instructions of a skill by name. Call this before applying a skill listed in the system prompt."
}
func (t *skillTool) Schema() gage.JSONSchema {
	names := make([]string, 0, t.set.Len())
	for _, s := range t.set.List() {
		names = append(names, s.Name)
	}
	prop := jsonschema.Str("The name of the skill to load.")
	if len(names) > 0 {
		prop.Enum = names
	}
	return jsonschema.Object(map[string]jsonschema.Property{"name": prop}, "name")
}

func (t *skillTool) Metadata() gage.ToolMetadata {
	return gage.ToolMetadata{ReadOnly: true, Tags: []string{"skill"}}
}

func (t *skillTool) DescribeCall(input json.RawMessage) string {
	var args struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(input, &args) == nil && args.Name != "" {
		return fmt.Sprintf("skill %q", args.Name)
	}
	return ""
}

func (t *skillTool) Execute(ctx context.Context, input json.RawMessage) (gage.ToolResult, error) {
	var args struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return gage.ToolResult{}, err
	}
	sk, ok := t.set.Get(args.Name)
	if !ok {
		return gage.ErrorResult("", fmt.Sprintf("unknown skill %q", args.Name)), nil
	}
	body, err := sk.Body()
	if err != nil {
		return gage.ErrorResult("", err.Error()), nil
	}
	header := fmt.Sprintf("# Skill: %s\n\n", sk.Name)
	return gage.TextResult("", header+body), nil
}
