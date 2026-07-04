package agent

import (
	"context"

	"github.com/deepteams/gage"
)

// Hooks are optional interception points in the agent loop. All fields may be
// nil. Unlike the Observer (which is fire-and-forget), hooks can change the
// run: rewrite requests, rewrite tool inputs, veto or amend tool results.
type Hooks struct {
	// PrepareRequest runs before every provider call and may mutate the
	// request in place (swap models, trim messages, tweak options, filter
	// tools). Returning an error aborts the run with an EventError.
	PrepareRequest func(ctx context.Context, req *gage.Request) error

	// PreToolUse runs before each tool execution and may rewrite the call
	// (typically its Input). Returning an error blocks the execution and
	// reports the error text to the model as an error ToolResult.
	PreToolUse func(ctx context.Context, tc gage.ToolCall) (gage.ToolCall, error)

	// PostToolUse runs after each tool execution (including error results and
	// denials) and may rewrite the result before it is streamed and appended
	// to the conversation — e.g. to redact secrets or truncate output.
	PostToolUse func(ctx context.Context, tc gage.ToolCall, res gage.ToolResult) gage.ToolResult
}
