package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/deepteams/gage"
	"github.com/deepteams/gage/jsonschema"
)

// AsTool exposes the agent as a gage.Tool, enabling sub-agent delegation: a
// parent agent can call this tool with a task string, and the sub-agent runs
// its own loop and returns its final text. Streaming events from the sub-agent
// are consumed internally; only the final answer is returned to the parent.
func (a *Agent) AsTool(name, description string) gage.Tool {
	return &subAgentTool{agent: a, name: name, desc: description}
}

type subAgentTool struct {
	agent *Agent
	name  string
	desc  string
}

func (t *subAgentTool) Name() string        { return t.name }
func (t *subAgentTool) Description() string { return t.desc }
func (t *subAgentTool) Schema() gage.JSONSchema {
	return jsonschema.Object(map[string]jsonschema.Property{
		"task": jsonschema.Str("The task or question to delegate to the sub-agent."),
	}, "task")
}

func (t *subAgentTool) Metadata() gage.ToolMetadata {
	return gage.ToolMetadata{LongRunning: true, RequiresApproval: true, Tags: []string{"subagent"}}
}

func (t *subAgentTool) DescribeCall(input json.RawMessage) string {
	var args struct {
		Task string `json:"task"`
	}
	if json.Unmarshal(input, &args) == nil && args.Task != "" {
		return fmt.Sprintf("%s task %q", t.name, args.Task)
	}
	return ""
}

func (t *subAgentTool) Execute(ctx context.Context, input json.RawMessage) (gage.ToolResult, error) {
	var args struct {
		Task string `json:"task"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return gage.ToolResult{}, err
	}
	res, err := t.agent.RunSync(ctx, []gage.Message{gage.UserText(args.Task)})
	if err != nil {
		// A failed, cancelled, or timed-out sub-agent must surface as an
		// error result, never as an empty success.
		return gage.ErrorResult("", fmt.Sprintf("sub-agent %s failed: %v", t.name, err)), nil
	}
	return gage.TextResult("", res.Text), nil
}
