package gage

import "context"

// Checkpoint captures a run suspended mid-turn because one or more tool calls
// await an out-of-band approval (the Approver returned ErrApprovalPending).
// It is fully JSON-serializable so callers can persist it (see SessionStore)
// and resume the run later — in another process if needed — with
// agent.Agent.Resume.
type Checkpoint struct {
	// Messages is the conversation up to and including the assistant message
	// whose tool calls triggered the pause. Tool results of the paused turn
	// are NOT yet appended; they live in Results until the turn completes.
	Messages []Message `json:"messages"`
	// Turn is the loop iteration that paused.
	Turn int `json:"turn"`
	// Usage is the token usage accumulated up to the pause.
	Usage Usage `json:"usage"`
	// StopReason is the stop reason of the paused assistant message.
	StopReason StopReason `json:"stop_reason,omitempty"`
	// Calls are all tool calls of the paused turn, in the order the model
	// issued them.
	Calls []ToolCall `json:"calls"`
	// Results holds the results of the calls that already completed before
	// the pause (approved-and-executed or denied ones). Calls without a
	// matching CallID here are pending a decision.
	Results []ToolResult `json:"results,omitempty"`
}

// Pending returns the tool calls that still await a decision: the Calls with
// no matching entry in Results.
func (c *Checkpoint) Pending() []ToolCall {
	done := make(map[string]bool, len(c.Results))
	for _, r := range c.Results {
		done[r.CallID] = true
	}
	var out []ToolCall
	for _, tc := range c.Calls {
		if !done[tc.ID] {
			out = append(out, tc)
		}
	}
	return out
}

// Session is the persistable state of a conversation with an agent: the
// message history and, when a run is suspended awaiting approval, the resume
// checkpoint.
type Session struct {
	// Messages is the conversation so far (typically Result.Messages after a
	// completed run).
	Messages []Message `json:"messages"`
	// Checkpoint is non-nil while a run is paused awaiting tool approval.
	Checkpoint *Checkpoint `json:"checkpoint,omitempty"`
}

// SessionStore persists and retrieves Sessions. The consumer implements it
// (database, KV store, ...); the sessions package provides in-memory and
// file-based implementations as conveniences. Implementations must be safe
// for concurrent use.
type SessionStore interface {
	// SaveSession persists the session under id, replacing any previous value.
	SaveSession(ctx context.Context, id string, s Session) error
	// LoadSession returns the stored session. It returns an error wrapping
	// ErrSessionNotFound when no session exists for id.
	LoadSession(ctx context.Context, id string) (Session, error)
	// DeleteSession removes the session. Deleting a missing session is a no-op.
	DeleteSession(ctx context.Context, id string) error
	// ListSessions returns the ids of all stored sessions.
	ListSessions(ctx context.Context) ([]string, error)
}
