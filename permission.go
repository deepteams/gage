package gage

import (
	"bytes"
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

// PermissionCacheKeyFunc derives the cache key used by RememberingBy. Returning
// an empty key disables caching for that request.
type PermissionCacheKeyFunc func(req PermissionRequest) string

// ToolPermissionKey caches remembered decisions by tool name only. This is
// convenient for broad policies such as "always allow read-only tools", but it
// is too coarse for tools whose risk depends on arguments.
func ToolPermissionKey(req PermissionRequest) string { return req.Tool }

// ToolAndInputPermissionKey caches remembered decisions by tool name and
// canonical JSON input. It is the safer default for approvals of write, shell,
// network, and other argument-sensitive tools.
func ToolAndInputPermissionKey(req PermissionRequest) string {
	return req.Tool + "\x00" + canonicalPermissionInput(req.Input)
}

func canonicalPermissionInput(input json.RawMessage) string {
	if len(input) == 0 {
		return "{}"
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, input); err == nil {
		return buf.String()
	}
	return string(input)
}

// Remembering wraps an Approver so decisions marked Remember are cached per
// tool name and reused without consulting the inner Approver again. It is
// concurrency-safe. The cache lives for the lifetime of the wrapper: scope it
// to a session by creating one wrapper per session.
//
// For tools whose risk depends on their arguments, prefer RememberingPerInput
// or RememberingBy with a policy-specific key.
func Remembering(inner Approver) Approver {
	return RememberingBy(inner, ToolPermissionKey)
}

// RememberingPerInput wraps an Approver so remembered decisions are cached by
// tool name plus canonical JSON input. This avoids reusing an approval for one
// path, command, URL, or payload on a different invocation of the same tool.
func RememberingPerInput(inner Approver) Approver {
	return RememberingBy(inner, ToolAndInputPermissionKey)
}

// RememberingBy wraps an Approver with a caller-defined cache key. Decisions
// are cached only when the approval has Remember set and key returns non-empty.
func RememberingBy(inner Approver, key PermissionCacheKeyFunc) Approver {
	if key == nil {
		key = ToolPermissionKey
	}
	return &rememberingApprover{inner: inner, key: key, cache: map[string]Approval{}}
}

type rememberingApprover struct {
	inner Approver
	key   PermissionCacheKeyFunc
	mu    sync.RWMutex
	cache map[string]Approval
}

func (r *rememberingApprover) Approve(ctx context.Context, req PermissionRequest) (Approval, error) {
	key := r.key(req)
	if key == "" {
		return r.inner.Approve(ctx, req)
	}
	r.mu.RLock()
	cached, ok := r.cache[key]
	r.mu.RUnlock()
	if ok {
		// Cached decisions may apply beyond one concrete invocation: drop any
		// per-call input rewrite.
		cached.UpdatedInput = nil
		return cached, nil
	}
	approval, err := r.inner.Approve(ctx, req)
	if err != nil {
		return approval, err
	}
	if approval.Remember {
		r.mu.Lock()
		r.cache[key] = approval
		r.mu.Unlock()
	}
	return approval, nil
}
