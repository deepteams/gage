package gage

import (
	"context"
	"encoding/json"
	"sync"
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

// Approval is the outcome of a permission check.
type Approval struct {
	// Allow permits the tool execution; false blocks it and reports an error
	// result to the model.
	Allow bool
	// Reason is shown to the model on denial so it can adapt (optional).
	Reason string
	// UpdatedInput, if non-nil on an allowed call, replaces the tool input
	// before execution (e.g. a sanitized path or a narrowed command).
	UpdatedInput json.RawMessage
	// Remember asks the caller to reuse this decision for future invocations
	// of the same tool without consulting the Approver again (see Remembering).
	Remember bool
}

// Allowed is a convenience Approval that permits execution.
func Allowed() Approval { return Approval{Allow: true} }

// Denied is a convenience Approval that blocks execution with a reason.
func Denied(reason string) Approval { return Approval{Allow: false, Reason: reason} }

// Approver decides whether a tool may run. It is invoked before every tool
// Execute when configured on an agent (or via the tools permission decorator).
type Approver interface {
	Approve(ctx context.Context, req PermissionRequest) (Approval, error)
}

// ApproverFunc adapts a function into an Approver.
type ApproverFunc func(ctx context.Context, req PermissionRequest) (Approval, error)

func (f ApproverFunc) Approve(ctx context.Context, req PermissionRequest) (Approval, error) {
	return f(ctx, req)
}

// Remembering wraps an Approver so decisions marked Remember are cached per
// tool name and reused without consulting the inner Approver again. It is
// concurrency-safe. The cache lives for the lifetime of the wrapper: scope it
// to a session by creating one wrapper per session.
func Remembering(inner Approver) Approver {
	return &rememberingApprover{inner: inner, cache: map[string]Approval{}}
}

type rememberingApprover struct {
	inner Approver
	mu    sync.RWMutex
	cache map[string]Approval
}

func (r *rememberingApprover) Approve(ctx context.Context, req PermissionRequest) (Approval, error) {
	r.mu.RLock()
	cached, ok := r.cache[req.Tool]
	r.mu.RUnlock()
	if ok {
		// Cached decisions apply to the tool, not one concrete invocation:
		// drop any per-call input rewrite.
		cached.UpdatedInput = nil
		return cached, nil
	}
	approval, err := r.inner.Approve(ctx, req)
	if err != nil {
		return approval, err
	}
	if approval.Remember {
		r.mu.Lock()
		r.cache[req.Tool] = approval
		r.mu.Unlock()
	}
	return approval, nil
}
