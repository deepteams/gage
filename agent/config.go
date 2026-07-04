// Package agent runs the agentic loop: it calls a gage.Provider, executes the
// tool calls the model requests, feeds the results back, and iterates until the
// model produces a final answer or a limit is reached. All progress is streamed
// to the caller as gage.Events.
package agent

import (
	"time"

	"github.com/deepteams/gage"
	"github.com/deepteams/gage/skills"
)

// Config configures an Agent. Provider is required; everything else is optional.
type Config struct {
	// Provider is the model backend. Required.
	Provider gage.Provider
	// Model overrides the provider's default model (optional).
	Model string
	// System is the base system prompt.
	System string
	// Registry holds the tools the agent may call. If nil, the agent runs
	// without tools (single turn).
	Registry gage.ToolRegistry
	// Skills, if set, are advertised in the system prompt; register skills.NewTool
	// on Registry to let the model load them.
	Skills *skills.Set
	// Approver, if set, gates every tool execution.
	Approver gage.Approver
	// Hooks intercept and may alter the run (requests, tool inputs, results).
	Hooks Hooks
	// Observer, if set, receives structured lifecycle observations for audit,
	// metrics, and tracing. Observer panics are recovered and ignored.
	Observer Observer
	// Compactor, if set together with CompactAfter, shrinks the conversation
	// between turns once the provider-reported input tokens reach the
	// threshold.
	Compactor gage.Compactor
	// CompactAfter is the input-token threshold that triggers Compactor.
	CompactAfter int
	// Options are the default generation options per turn.
	Options gage.GenerateOptions
	// MaxTurns caps loop iterations (default 16). A turn is one provider call
	// plus any tool executions it triggers.
	MaxTurns int
	// MaxParallelTools bounds how many of a turn's tool calls run
	// concurrently. 0 or 1 means sequential execution. Tool-result events are
	// emitted as executions finish, but results are fed back to the model in
	// the order the calls were made.
	MaxParallelTools int
	// Timeout bounds a single Run (default: no timeout). Uses context.WithTimeout.
	Timeout time.Duration
	// ToolTimeout bounds each individual tool execution (default: no per-tool
	// timeout). Tools should still honor ctx for cooperative cancellation.
	ToolTimeout time.Duration
	// Name identifies the agent (used in permission requests and sub-agents).
	Name string
}

func (c Config) maxTurns() int {
	if c.MaxTurns > 0 {
		return c.MaxTurns
	}
	return 16
}

// systemPrompt combines the base system prompt with the skills preamble.
func (c Config) systemPrompt() string {
	base := c.System
	if c.Skills != nil {
		if sp := c.Skills.SystemPrompt(); sp != "" {
			if base != "" {
				base += "\n\n"
			}
			base += sp
		}
	}
	return base
}
