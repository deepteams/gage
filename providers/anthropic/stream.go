package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/deepteams/gage"
	"github.com/deepteams/gage/providers/shared"
)

// pump reads the Messages SSE stream and emits gage.Events. It owns closing
// out.
func (c *Client) pump(ctx context.Context, resp *http.Response, out chan<- gage.Event) {
	defer close(out)
	defer resp.Body.Close()

	send := func(e gage.Event) bool { return shared.Send(ctx, out, e) }
	if !send(gage.MessageStart()) {
		return
	}

	// Track content blocks by index (text, thinking, redacted_thinking or
	// tool_use).
	blocks := map[int]*block{}
	stopReason := gage.StopEndTurn
	var usage gage.Usage
	haveUsage := false

	err := shared.ScanSSE(resp.Body, func(ev shared.SSEEvent) error {
		var e wireEvent
		if err := json.Unmarshal([]byte(ev.Data), &e); err != nil {
			return nil
		}
		switch e.Type {
		case "message_start":
			if e.Message != nil && e.Message.Usage != nil {
				usage = e.Message.Usage.toGage()
				haveUsage = true
			}
		case "content_block_start":
			b := &block{kind: e.ContentBlock.Type}
			blocks[e.Index] = b
			switch b.kind {
			case "tool_use":
				b.id = e.ContentBlock.ID
				b.name = e.ContentBlock.Name
				if !send(gage.ToolCallStart(gage.ToolCall{ID: b.id, Name: b.name})) {
					return ctx.Err()
				}
				stopReason = gage.StopToolUse
			case "redacted_thinking":
				// The opaque payload arrives whole on the start event. It is
				// preserved as the block signature, marked so the encoder can
				// replay it as a redacted_thinking block.
				b.signature = RedactedSignaturePrefix + e.ContentBlock.Data
			}
		case "content_block_delta":
			b := blocks[e.Index]
			switch e.Delta.Type {
			case "text_delta":
				if !send(gage.TextDelta(e.Delta.Text)) {
					return ctx.Err()
				}
			case "thinking_delta":
				if !send(gage.ReasoningDelta(e.Delta.Thinking)) {
					return ctx.Err()
				}
			case "signature_delta":
				if b != nil {
					b.signature += e.Delta.Signature
				}
			case "input_json_delta":
				if b != nil && b.kind == "tool_use" {
					b.input = append(b.input, e.Delta.PartialJSON...)
					if !send(gage.ToolCallDelta(gage.ToolCall{ID: b.id, Name: b.name, Input: cloneRaw(b.input)})) {
						return ctx.Err()
					}
				}
			}
		case "content_block_stop":
			b := blocks[e.Index]
			if b == nil {
				return nil
			}
			switch b.kind {
			case "tool_use":
				input := b.input
				if len(input) == 0 {
					input = []byte("{}")
				}
				if !send(gage.ToolCallDone(gage.ToolCall{ID: b.id, Name: b.name, Input: cloneRaw(input)})) {
					return ctx.Err()
				}
			case "thinking", "redacted_thinking":
				if !send(gage.ReasoningDone(b.signature)) {
					return ctx.Err()
				}
			}
		case "message_delta":
			if e.Delta.StopReason != "" {
				stopReason = mapStopReason(e.Delta.StopReason)
			}
			if e.Usage != nil {
				// message_delta usage carries cumulative totals for the whole
				// message (output_tokens is the final count): replace the
				// message_start figures, never add.
				mergeUsage(&usage, e.Usage.toGage())
				haveUsage = true
			}
		case "error":
			msg := "stream error"
			if e.Error != nil {
				msg = e.Error.Message
			}
			return fmt.Errorf("%s", msg)
		}
		return nil
	})
	if err != nil {
		send(gage.ErrorEvent(fmt.Errorf("%s: %w", c.ProviderName, err)))
		return
	}
	if haveUsage {
		if !send(gage.UsageEvent(usage)) {
			return
		}
	}
	send(gage.MessageDone(stopReason))
}

type block struct {
	kind      string
	id        string
	name      string
	input     []byte
	signature string
}

// mergeUsage overwrites the fields of dst that u reports (non-zero). Anthropic
// usage snapshots are cumulative, so later values replace earlier ones.
func mergeUsage(dst *gage.Usage, u gage.Usage) {
	if u.InputTokens > 0 {
		dst.InputTokens = u.InputTokens
	}
	if u.OutputTokens > 0 {
		dst.OutputTokens = u.OutputTokens
	}
	if u.CacheReadTokens > 0 {
		dst.CacheReadTokens = u.CacheReadTokens
	}
	if u.CacheWriteTokens > 0 {
		dst.CacheWriteTokens = u.CacheWriteTokens
	}
}

func mapStopReason(r string) gage.StopReason {
	switch r {
	case "end_turn":
		return gage.StopEndTurn
	case "tool_use":
		return gage.StopToolUse
	case "max_tokens":
		return gage.StopMaxTokens
	case "stop_sequence":
		return gage.StopSequence
	case "refusal":
		return gage.StopRefusal
	default:
		return gage.StopReason(r)
	}
}

func cloneRaw(b []byte) json.RawMessage {
	return json.RawMessage(append([]byte(nil), b...))
}

// ---- wire types (streamed events) ----

type wireEvent struct {
	Type         string       `json:"type"`
	Index        int          `json:"index"`
	Message      *wireMessage `json:"message"`
	ContentBlock struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
		// Data is the opaque payload of a redacted_thinking block.
		Data string `json:"data"`
	} `json:"content_block"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		Signature   string `json:"signature"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage *wireUsage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type wireMessage struct {
	Usage *wireUsage `json:"usage"`
}

type wireUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

func (u wireUsage) toGage() gage.Usage {
	return gage.Usage{
		InputTokens:      u.InputTokens,
		OutputTokens:     u.OutputTokens,
		CacheReadTokens:  u.CacheReadInputTokens,
		CacheWriteTokens: u.CacheCreationInputTokens,
	}
}
