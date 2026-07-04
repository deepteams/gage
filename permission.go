package gage

import (
	"context"
	"encoding/json"
)

// Decision is the outcome of a permission check.
type Decision int

const (
	// Allow permits the tool execution.
	Allow Decision = iota
	// Deny blocks the tool execution; the agent reports an error result to the model.
	Deny
)

// PermissionRequest describes a tool execution awaiting approval.
type PermissionRequest struct {
	// Tool is the tool being invoked.
	Tool string
	// Input is the raw JSON arguments.
	Input json.RawMessage
	// Agent is the name of the agent requesting execution (may be empty).
	Agent string
	// RunID identifies the agent run requesting execution (may be empty when
	// approval is performed outside agent.Agent).
	RunID string
	// Turn is the agent loop iteration requesting execution. A zero value can be
	// either the first turn or unknown when approval is performed outside
	// agent.Agent.
	Turn int
	// Metadata carries advisory information about the tool's effects.
	Metadata ToolMetadata
	// Summary is a short human-readable description of the concrete invocation.
	Summary string
}

// Approver decides whether a tool may run. It is invoked before every tool
// Execute when configured on an agent (or via the tools permission decorator).
type Approver interface {
	Approve(ctx context.Context, req PermissionRequest) (Decision, error)
}

// ApproverFunc adapts a function into an Approver.
type ApproverFunc func(ctx context.Context, req PermissionRequest) (Decision, error)

func (f ApproverFunc) Approve(ctx context.Context, req PermissionRequest) (Decision, error) {
	return f(ctx, req)
}
