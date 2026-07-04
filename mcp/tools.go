package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

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

func adaptTool(session *mcpsdk.ClientSession, server string, t *mcpsdk.Tool) gage.Tool {
	return &mcpTool{
		session:  session,
		fullName: server + "__" + t.Name,
		rawName:  t.Name,
		desc:     t.Description,
		schema:   marshalSchema(t.InputSchema),
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
	return gage.ToolResult{Content: contentParts(res.Content), IsError: res.IsError}, nil
}

// contentParts maps MCP content blocks onto gage content parts: text stays
// text, images become PartImage, embedded resources are mapped like a resource
// read, and anything else is rendered as its JSON for visibility.
func contentParts(content []mcpsdk.Content) []gage.ContentPart {
	out := make([]gage.ContentPart, 0, len(content))
	for _, c := range content {
		out = append(out, contentPart(c))
	}
	return out
}

func contentPart(c mcpsdk.Content) gage.ContentPart {
	switch v := c.(type) {
	case *mcpsdk.TextContent:
		return gage.TextPart(v.Text)
	case *mcpsdk.ImageContent:
		return imagePart(v.Data, v.MIMEType)
	case *mcpsdk.EmbeddedResource:
		if v.Resource != nil {
			return resourcePart(v.Resource)
		}
		return gage.TextPart("[empty embedded resource]")
	default:
		if raw, err := c.MarshalJSON(); err == nil {
			return gage.TextPart(string(raw))
		}
		return gage.TextPart(fmt.Sprintf("[unrenderable %T content]", c))
	}
}

// imagePart builds a PartImage from raw image bytes and their MIME type.
func imagePart(data []byte, mimeType string) gage.ContentPart {
	return gage.ContentPart{
		Kind: gage.PartImage,
		Image: &gage.ImageSource{
			MediaType: mimeType,
			Data:      base64.StdEncoding.EncodeToString(data),
		},
	}
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
