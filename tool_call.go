package gage

import "encoding/json"

// ToolCall is a request from the model to invoke a tool.
type ToolCall struct {
	// ID uniquely identifies this call within a message; used to correlate the
	// result. Providers that do not supply one get a generated id.
	ID string `json:"id"`
	// Name is the tool being called.
	Name string `json:"name"`
	// Input holds the raw JSON arguments. It may be built incrementally while
	// streaming (see Event) and is only guaranteed complete on EventToolCallDone.
	Input json.RawMessage `json:"input"`
}

// ToolResult is the outcome of executing a ToolCall.
type ToolResult struct {
	// CallID matches the originating ToolCall.ID.
	CallID string `json:"call_id"`
	// Content is the result payload, most often a single text part.
	Content []ContentPart `json:"content"`
	// IsError reports that the tool failed; the content describes the error.
	IsError bool `json:"is_error,omitempty"`
}

// Text returns the concatenated text content of the result.
func (r ToolResult) Text() string {
	var s string
	for _, p := range r.Content {
		if p.Kind == PartText {
			s += p.Text
		}
	}
	return s
}

// TextResult builds a successful ToolResult carrying a single text part.
func TextResult(callID, text string) ToolResult {
	return ToolResult{CallID: callID, Content: []ContentPart{TextPart(text)}}
}

// ErrorResult builds a failed ToolResult carrying an error message.
func ErrorResult(callID, msg string) ToolResult {
	return ToolResult{CallID: callID, Content: []ContentPart{TextPart(msg)}, IsError: true}
}

// ToolChoiceMode controls whether/which tool the model must call.
type ToolChoiceMode string

const (
	// ToolChoiceAuto lets the model decide (default).
	ToolChoiceAuto ToolChoiceMode = "auto"
	// ToolChoiceNone forbids tool calls.
	ToolChoiceNone ToolChoiceMode = "none"
	// ToolChoiceRequired forces the model to call some tool.
	ToolChoiceRequired ToolChoiceMode = "required"
	// ToolChoiceTool forces a specific tool named by ToolChoice.Name.
	ToolChoiceTool ToolChoiceMode = "tool"
)

// ToolChoice expresses a tool-selection constraint for a request.
type ToolChoice struct {
	Mode ToolChoiceMode `json:"mode"`
	Name string         `json:"name,omitempty"` // used when Mode == ToolChoiceTool
}
