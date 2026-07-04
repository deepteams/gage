package gage

import "context"

// Compactor shrinks a conversation that is approaching the model's context
// window. The agent loop invokes it between turns when the configured token
// threshold is crossed; implementations may summarize old turns, drop them, or
// rewrite the history entirely.
//
// Implementations must preserve the invariants providers rely on: every
// PartToolUse must keep its matching PartToolResult (drop or keep them as a
// pair), and the first message should remain a user message.
type Compactor interface {
	// Compact returns the replacement conversation. usage is the token usage
	// of the latest provider call, giving the current input size. Returning
	// the input slice unchanged (and a nil error) is a valid no-op.
	Compact(ctx context.Context, msgs []Message, usage Usage) ([]Message, error)
}

// CompactorFunc adapts a function into a Compactor.
type CompactorFunc func(ctx context.Context, msgs []Message, usage Usage) ([]Message, error)

func (f CompactorFunc) Compact(ctx context.Context, msgs []Message, usage Usage) ([]Message, error) {
	return f(ctx, msgs, usage)
}
