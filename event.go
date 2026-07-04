package gage

import "encoding/json"

// EventType tags a streaming Event.
type EventType string

const (
	// EventMessageStart marks the beginning of an assistant message.
	EventMessageStart EventType = "message_start"
	// EventTextDelta carries a chunk of visible assistant text.
	EventTextDelta EventType = "text_delta"
	// EventReasoningDelta carries a chunk of reasoning/thinking text.
	EventReasoningDelta EventType = "reasoning_delta"
	// EventReasoningDone closes a reasoning block. Signature carries the
	// provider's opaque replay token for the block, when one exists. Providers
	// that stream reasoning without block boundaries may omit this event.
	EventReasoningDone EventType = "reasoning_done"
	// EventToolCallStart signals a tool call has begun; ToolCall has ID+Name.
	EventToolCallStart EventType = "tool_call_start"
	// EventToolCallDelta carries a partial chunk of the tool call arguments;
	// ToolCall.Input holds the accumulated JSON so far.
	EventToolCallDelta EventType = "tool_call_delta"
	// EventToolCallDone signals the tool call arguments are complete.
	EventToolCallDone EventType = "tool_call_end"
	// EventToolResult carries the result of executing a tool (emitted by the agent).
	EventToolResult EventType = "tool_result"
	// EventUsage carries a token-usage update.
	EventUsage EventType = "usage"
	// EventMessageDone marks the end of an assistant message with a StopReason.
	EventMessageDone EventType = "message_done"
	// EventError carries a terminal error; the stream closes after it.
	EventError EventType = "error"
	// EventDone marks the end of the entire stream (emitted by the agent).
	EventDone EventType = "done"
)

// Event is the unified streaming unit produced by Providers and Agents. It is a
// tagged struct (rather than an interface) so it serializes cleanly to SSE/JSON
// and routes naturally through a select. Only the fields relevant to Type are
// populated.
type Event struct {
	Type EventType `json:"type"`
	// Text holds delta text for EventTextDelta / EventReasoningDelta.
	Text string `json:"text,omitempty"`
	// ToolCall is set for EventToolCallStart/Delta/Done.
	ToolCall *ToolCall `json:"tool_call,omitempty"`
	// ToolResult is set for EventToolResult.
	ToolResult *ToolResult `json:"tool_result,omitempty"`
	// Usage is set for EventUsage.
	Usage *Usage `json:"usage,omitempty"`
	// StopReason is set for EventMessageDone.
	StopReason StopReason `json:"stop_reason,omitempty"`
	// Signature is set for EventReasoningDone when the provider requires the
	// reasoning block to be replayed with an opaque token.
	Signature string `json:"signature,omitempty"`
	// Result summarizes the whole run. It is set on the terminal EventDone
	// emitted by an agent (never by raw providers).
	Result *Result `json:"result,omitempty"`
	// Err is set for EventError. It is not serialized directly; use ErrorString.
	Err error `json:"-"`
	// ErrorString mirrors Err for JSON transports.
	ErrorString string `json:"error,omitempty"`
	// Turn is the agent loop iteration (0 for raw provider events).
	Turn int `json:"turn,omitempty"`
	// Raw carries the provider's raw event payload for debugging/extension.
	Raw json.RawMessage `json:"raw,omitempty"`
}

// TextDelta builds an EventTextDelta.
func TextDelta(s string) Event { return Event{Type: EventTextDelta, Text: s} }

// ReasoningDelta builds an EventReasoningDelta.
func ReasoningDelta(s string) Event { return Event{Type: EventReasoningDelta, Text: s} }

// ReasoningDone builds an EventReasoningDone carrying the block's replay
// signature (may be empty).
func ReasoningDone(signature string) Event {
	return Event{Type: EventReasoningDone, Signature: signature}
}

// MessageStart builds an EventMessageStart.
func MessageStart() Event { return Event{Type: EventMessageStart} }

// ToolCallStart builds an EventToolCallStart.
func ToolCallStart(tc ToolCall) Event { return Event{Type: EventToolCallStart, ToolCall: &tc} }

// ToolCallDelta builds an EventToolCallDelta.
func ToolCallDelta(tc ToolCall) Event { return Event{Type: EventToolCallDelta, ToolCall: &tc} }

// ToolCallDone builds an EventToolCallDone.
func ToolCallDone(tc ToolCall) Event { return Event{Type: EventToolCallDone, ToolCall: &tc} }

// ToolResultEvent builds an EventToolResult.
func ToolResultEvent(tr ToolResult) Event { return Event{Type: EventToolResult, ToolResult: &tr} }

// UsageEvent builds an EventUsage.
func UsageEvent(u Usage) Event { return Event{Type: EventUsage, Usage: &u} }

// MessageDone builds an EventMessageDone.
func MessageDone(stopReason StopReason) Event {
	return Event{Type: EventMessageDone, StopReason: stopReason}
}

// ErrorEvent builds an EventError, populating both Err and ErrorString.
func ErrorEvent(err error) Event {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	return Event{Type: EventError, Err: err, ErrorString: msg}
}

// DoneEvent builds an EventDone carrying the run summary.
func DoneEvent(res *Result) Event { return Event{Type: EventDone, Result: res} }

// WithTurn returns a copy of the event tagged with the given loop turn.
func (e Event) WithTurn(turn int) Event {
	e.Turn = turn
	return e
}
