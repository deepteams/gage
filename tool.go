package gage

import (
	"bytes"
	"context"
	"encoding/json"
)

// JSONSchema is a JSON Schema document describing a tool's parameters. It is a
// raw JSON message so callers can supply any valid schema without a struct
// dependency; internal/jsonschema offers helpers to build common shapes.
type JSONSchema = json.RawMessage

// Tool is the executable port for a capability the model can invoke.
type Tool interface {
	// Name is the identifier the model uses to call the tool.
	Name() string
	// Description tells the model what the tool does and when to use it.
	Description() string
	// Schema returns the JSON Schema of the tool's input parameters.
	Schema() JSONSchema
	// Execute runs the tool with the given raw JSON input and returns its
	// result. Returning a non-nil error is reserved for infrastructure failures;
	// tool-level failures should be reported via a ToolResult with IsError set,
	// so the model can see and react to them.
	Execute(ctx context.Context, input json.RawMessage) (ToolResult, error)
}

// ToolMetadata describes a tool's broad operational effects. It is advisory:
// callers can use it in Approvers, UI prompts, audit logs, and policy engines,
// but gage does not impose a policy from these flags.
type ToolMetadata struct {
	// ReadOnly reports that the tool is expected not to mutate external state.
	ReadOnly bool `json:"read_only,omitempty"`
	// Filesystem reports that the tool reads or writes the local filesystem.
	Filesystem bool `json:"filesystem,omitempty"`
	// Network reports that the tool can access network resources.
	Network bool `json:"network,omitempty"`
	// Shell reports that the tool can execute shell commands or subprocesses.
	Shell bool `json:"shell,omitempty"`
	// Destructive reports that the tool may delete, overwrite, or otherwise
	// irreversibly change state.
	Destructive bool `json:"destructive,omitempty"`
	// LongRunning reports that the tool may naturally run for a while.
	LongRunning bool `json:"long_running,omitempty"`
	// RequiresApproval is an advisory hint for clients that want a conservative
	// default policy.
	RequiresApproval bool `json:"requires_approval,omitempty"`
	// Tags are free-form labels for client policy and UI grouping.
	Tags []string `json:"tags,omitempty"`
}

// ToolMetadataProvider is an optional capability implemented by tools that can
// describe their operational effects.
type ToolMetadataProvider interface {
	Metadata() ToolMetadata
}

// ToolCallDescriber is an optional capability implemented by tools that can
// summarize a concrete invocation for approval UIs and audit logs.
type ToolCallDescriber interface {
	DescribeCall(input json.RawMessage) string
}

// MetadataOf returns a tool's advisory metadata, if provided.
func MetadataOf(t Tool) ToolMetadata {
	if p, ok := t.(ToolMetadataProvider); ok {
		return p.Metadata()
	}
	return ToolMetadata{}
}

// CallSummaryOf returns a short human-readable summary for a tool invocation.
func CallSummaryOf(t Tool, input json.RawMessage) string {
	if d, ok := t.(ToolCallDescriber); ok {
		if s := d.DescribeCall(input); s != "" {
			return s
		}
	}
	return t.Name() + " " + compactForSummary(input)
}

func compactForSummary(input json.RawMessage) string {
	if len(input) == 0 {
		return "{}"
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, input); err == nil {
		input = buf.Bytes()
	}
	const max = 512
	if len(input) > max {
		return string(input[:max]) + "...(truncated)"
	}
	return string(input)
}

// ToolRegistry holds the tools available to an agent and exposes their schemas.
type ToolRegistry interface {
	// Register adds a tool. It returns an error if a tool with the same name is
	// already registered.
	Register(t Tool) error
	// Get returns the tool with the given name.
	Get(name string) (Tool, bool)
	// List returns all registered tools.
	List() []Tool
	// Schemas returns the ToolSchema of every registered tool, for Request.Tools.
	Schemas() []ToolSchema
}

// ToolFunc adapts a plain function into a Tool. It is handy for defining ad-hoc
// tools without a dedicated type.
type ToolFunc struct {
	ToolName    string
	Desc        string
	Params      JSONSchema
	Meta        ToolMetadata
	CallSummary func(input json.RawMessage) string
	Fn          func(ctx context.Context, input json.RawMessage) (ToolResult, error)
}

func (t ToolFunc) Name() string        { return t.ToolName }
func (t ToolFunc) Description() string { return t.Desc }
func (t ToolFunc) Schema() JSONSchema  { return t.Params }
func (t ToolFunc) Metadata() ToolMetadata {
	return t.Meta
}
func (t ToolFunc) DescribeCall(input json.RawMessage) string {
	if t.CallSummary == nil {
		return ""
	}
	return t.CallSummary(input)
}
func (t ToolFunc) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	return t.Fn(ctx, input)
}

// SchemaOf builds a ToolSchema from a Tool.
func SchemaOf(t Tool) ToolSchema {
	return ToolSchema{Name: t.Name(), Description: t.Description(), Parameters: t.Schema()}
}
