package tools

import (
	"context"
	"encoding/json"

	"github.com/deepteams/gage"
)

// Guard wraps a Tool so an Approver is consulted before every execution. A Deny
// decision returns an error ToolResult (visible to the model) without running
// the tool. agentName, if set, is passed to the Approver for context.
func Guard(t gage.Tool, approver gage.Approver, agentName string) gage.Tool {
	if approver == nil {
		return t
	}
	return &guarded{Tool: t, approver: approver, agent: agentName}
}

// GuardAll wraps each tool with Guard.
func GuardAll(tools []gage.Tool, approver gage.Approver, agentName string) []gage.Tool {
	if approver == nil {
		return tools
	}
	out := make([]gage.Tool, len(tools))
	for i, t := range tools {
		out[i] = Guard(t, approver, agentName)
	}
	return out
}

type guarded struct {
	gage.Tool
	approver gage.Approver
	agent    string
}

func (g *guarded) Execute(ctx context.Context, input json.RawMessage) (gage.ToolResult, error) {
	decision, err := g.approver.Approve(ctx, gage.PermissionRequest{
		Tool:     g.Tool.Name(),
		Input:    input,
		Agent:    g.agent,
		Metadata: gage.MetadataOf(g.Tool),
		Summary:  gage.CallSummaryOf(g.Tool, input),
	})
	if err != nil {
		return gage.ToolResult{}, err
	}
	if decision == gage.Deny {
		return gage.ErrorResult("", "permission denied for tool "+g.Tool.Name()), nil
	}
	return g.Tool.Execute(ctx, input)
}

func (g *guarded) Metadata() gage.ToolMetadata {
	return gage.MetadataOf(g.Tool)
}

func (g *guarded) DescribeCall(input json.RawMessage) string {
	return gage.CallSummaryOf(g.Tool, input)
}
