package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/deepteams/gage"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// mcpTool adapts a discovered MCP tool to the gage.Tool port.
type mcpTool struct {
	session  *mcpsdk.ClientSession
	fullName string // "<server>__<tool>" exposed to the model
	rawName  string // original tool name on the server
	desc     string
	schema   gage.JSONSchema
}

func (c *Client) adapt(t *mcpsdk.Tool) gage.Tool {
	schema := marshalSchema(t.InputSchema)
	return &mcpTool{
		session:  c.session,
		fullName: c.name + "__" + t.Name,
		rawName:  t.Name,
		desc:     t.Description,
		schema:   schema,
	}
}

func (t *mcpTool) Name() string            { return t.fullName }
func (t *mcpTool) Description() string     { return t.desc }
func (t *mcpTool) Schema() gage.JSONSchema { return t.schema }
func (t *mcpTool) Metadata() gage.ToolMetadata {
	return gage.ToolMetadata{RequiresApproval: true, Tags: []string{"mcp"}}
}
func (t *mcpTool) DescribeCall(input json.RawMessage) string {
	return gage.CallSummaryOf(gage.ToolFunc{ToolName: t.fullName}, input)
}

func (t *mcpTool) Execute(ctx context.Context, input json.RawMessage) (gage.ToolResult, error) {
	var args any
	if len(input) > 0 {
		if err := json.Unmarshal(input, &args); err != nil {
			return gage.ErrorResult("", "invalid arguments: "+err.Error()), nil
		}
	}
	res, err := t.session.CallTool(ctx, &mcpsdk.CallToolParams{Name: t.rawName, Arguments: args})
	if err != nil {
		// Protocol-level failure (tool missing, transport error): surface to model.
		return gage.ErrorResult("", fmt.Sprintf("mcp call %s: %v", t.rawName, err)), nil
	}
	text := contentText(res.Content)
	if res.IsError {
		return gage.ErrorResult("", text), nil
	}
	return gage.TextResult("", text), nil
}

// contentText flattens MCP content blocks into a single string. Non-text blocks
// are rendered as their JSON for visibility.
func contentText(content []mcpsdk.Content) string {
	var b strings.Builder
	for i, c := range content {
		if i > 0 {
			b.WriteByte('\n')
		}
		if tc, ok := c.(*mcpsdk.TextContent); ok {
			b.WriteString(tc.Text)
			continue
		}
		if raw, err := c.MarshalJSON(); err == nil {
			b.Write(raw)
		}
	}
	return b.String()
}

// marshalSchema normalizes an MCP input schema (typically map[string]any) into
// a gage.JSONSchema. A nil or invalid schema yields a permissive object schema.
func marshalSchema(s any) gage.JSONSchema {
	if s == nil {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	if raw, ok := s.(json.RawMessage); ok && len(raw) > 0 {
		return raw
	}
	b, err := json.Marshal(s)
	if err != nil || len(b) == 0 {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return b
}
