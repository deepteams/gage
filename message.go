package gage

// Role identifies who produced a Message.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// PartKind is the discriminator of a ContentPart union.
type PartKind string

const (
	PartText       PartKind = "text"
	PartImage      PartKind = "image"
	PartDocument   PartKind = "document"
	PartToolUse    PartKind = "tool_use"
	PartToolResult PartKind = "tool_result"
	PartReasoning  PartKind = "reasoning"
)

// ImageSource carries image data, either as a URL or inline base64 bytes.
type ImageSource struct {
	// URL references a remote image. Mutually exclusive with Data.
	URL string `json:"url,omitempty"`
	// MediaType is the MIME type of Data (e.g. "image/png").
	MediaType string `json:"media_type,omitempty"`
	// Data is base64-encoded image bytes. Mutually exclusive with URL.
	Data string `json:"data,omitempty"`
}

// DocumentSource carries a document (e.g. a PDF), either as a URL or inline
// base64 bytes. Providers that cannot accept documents fail the request with
// ErrUnsupported rather than silently dropping the part.
type DocumentSource struct {
	// URL references a remote document. Mutually exclusive with Data.
	URL string `json:"url,omitempty"`
	// MediaType is the MIME type of Data (e.g. "application/pdf").
	MediaType string `json:"media_type,omitempty"`
	// Data is base64-encoded document bytes. Mutually exclusive with URL.
	Data string `json:"data,omitempty"`
	// Filename optionally names the document; some providers display it to the
	// model or require it for format detection.
	Filename string `json:"filename,omitempty"`
}

// ContentPart is a tagged union: Kind selects which field is meaningful.
type ContentPart struct {
	Kind       PartKind        `json:"kind"`
	Text       string          `json:"text,omitempty"`        // PartText / PartReasoning
	Image      *ImageSource    `json:"image,omitempty"`       // PartImage
	Document   *DocumentSource `json:"document,omitempty"`    // PartDocument
	ToolCall   *ToolCall       `json:"tool_call,omitempty"`   // PartToolUse
	ToolResult *ToolResult     `json:"tool_result,omitempty"` // PartToolResult
	// Signature is an opaque provider token attached to a PartReasoning that
	// must be replayed verbatim with the reasoning text for the provider to
	// accept the block in a later turn (Anthropic thinking signatures, OpenAI
	// Responses encrypted reasoning). Empty when the provider needs none.
	Signature string `json:"signature,omitempty"`
}

// Message is a single turn of the conversation.
type Message struct {
	Role    Role          `json:"role"`
	Content []ContentPart `json:"content"`
	// Name is an optional participant/tool name (provider-dependent).
	Name string `json:"name,omitempty"`
}

// Text returns the concatenation of all text parts (ignoring reasoning).
func (m Message) Text() string {
	var s string
	for _, p := range m.Content {
		if p.Kind == PartText {
			s += p.Text
		}
	}
	return s
}

// ToolCalls returns the tool-use parts of the message, if any.
func (m Message) ToolCalls() []ToolCall {
	var out []ToolCall
	for _, p := range m.Content {
		if p.Kind == PartToolUse && p.ToolCall != nil {
			out = append(out, *p.ToolCall)
		}
	}
	return out
}

// TextPart builds a text ContentPart.
func TextPart(s string) ContentPart { return ContentPart{Kind: PartText, Text: s} }

// ReasoningPart builds a reasoning ContentPart.
func ReasoningPart(s string) ContentPart { return ContentPart{Kind: PartReasoning, Text: s} }

// SignedReasoningPart builds a reasoning ContentPart carrying the provider's
// replay signature.
func SignedReasoningPart(s, signature string) ContentPart {
	return ContentPart{Kind: PartReasoning, Text: s, Signature: signature}
}

// DocumentPart builds a document ContentPart.
func DocumentPart(d DocumentSource) ContentPart {
	return ContentPart{Kind: PartDocument, Document: &d}
}

// ToolUsePart builds a tool-use ContentPart.
func ToolUsePart(tc ToolCall) ContentPart { return ContentPart{Kind: PartToolUse, ToolCall: &tc} }

// ToolResultPart builds a tool-result ContentPart.
func ToolResultPart(tr ToolResult) ContentPart {
	return ContentPart{Kind: PartToolResult, ToolResult: &tr}
}

// UserText is a convenience constructor for a plain user message.
func UserText(s string) Message {
	return Message{Role: RoleUser, Content: []ContentPart{TextPart(s)}}
}

// AssistantText is a convenience constructor for a plain assistant message.
func AssistantText(s string) Message {
	return Message{Role: RoleAssistant, Content: []ContentPart{TextPart(s)}}
}

// ToolResultMessage builds a RoleTool message carrying one tool result.
func ToolResultMessage(tr ToolResult) Message {
	return Message{Role: RoleTool, Content: []ContentPart{ToolResultPart(tr)}}
}
