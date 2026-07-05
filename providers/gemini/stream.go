package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/deepteams/gage"
	"github.com/deepteams/gage/providers/shared"
)

// pump reads the streamGenerateContent SSE stream and emits gage.Events. It
// owns closing out and selects on ctx.Done() for every send (via shared.Send).
func (c *Client) pump(ctx context.Context, resp *http.Response, out chan<- gage.Event) {
	defer close(out)
	defer resp.Body.Close()

	send := func(e gage.Event) bool { return shared.Send(ctx, out, e) }
	if !send(gage.MessageStart()) {
		return
	}

	var (
		usage     gage.Usage
		haveUsage bool
		finish    string
		sawTool   bool
		callIdx   int
		// Open reasoning block state: Gemini streams thought parts without
		// explicit block boundaries; a non-thought part (or end of stream)
		// closes the block. The replay signature arrives on a thought part.
		inThought  bool
		thoughtSig string
	)
	closeThought := func() bool {
		if !inThought {
			return true
		}
		inThought = false
		sig := thoughtSig
		thoughtSig = ""
		return send(gage.ReasoningDone(sig))
	}

	err := shared.ScanSSE(resp.Body, func(ev shared.SSEEvent) error {
		var chunk wireChunk
		if err := json.Unmarshal([]byte(ev.Data), &chunk); err != nil {
			return nil
		}
		if chunk.Error != nil {
			return fmt.Errorf("stream error: %s", chunk.Error.Message)
		}
		if chunk.UsageMetadata != nil {
			// Usage snapshots are cumulative; the latest one wins.
			usage = chunk.UsageMetadata.toGage()
			haveUsage = true
		}
		if len(chunk.Candidates) == 0 {
			return nil
		}
		cand := chunk.Candidates[0]
		for _, p := range cand.Content.Parts {
			switch {
			case p.FunctionCall != nil:
				if !closeThought() {
					return ctx.Err()
				}
				args := p.FunctionCall.Args
				if len(args) == 0 {
					args = json.RawMessage("{}")
				}
				// Gemini does not assign call ids; generate one so results
				// can be correlated.
				call := gage.ToolCall{
					ID:    fmt.Sprintf("call_%d", callIdx),
					Name:  p.FunctionCall.Name,
					Input: args,
				}
				callIdx++
				sawTool = true
				if !send(gage.ToolCallStart(gage.ToolCall{ID: call.ID, Name: call.Name})) {
					return ctx.Err()
				}
				if !send(gage.ToolCallDelta(call)) {
					return ctx.Err()
				}
				if !send(gage.ToolCallDone(call)) {
					return ctx.Err()
				}
			case p.Thought:
				inThought = true
				if p.ThoughtSignature != "" {
					thoughtSig = p.ThoughtSignature
				}
				if p.Text != "" {
					if !send(gage.ReasoningDelta(p.Text)) {
						return ctx.Err()
					}
				}
			case p.Text != "":
				if !closeThought() {
					return ctx.Err()
				}
				if !send(gage.TextDelta(p.Text)) {
					return ctx.Err()
				}
			}
		}
		if cand.FinishReason != "" {
			finish = cand.FinishReason
		}
		return nil
	})
	if err != nil {
		send(gage.ErrorEvent(fmt.Errorf("%s: %w", c.ProviderName, err)))
		return
	}
	if !closeThought() {
		return
	}
	if haveUsage {
		if !send(gage.UsageEvent(usage)) {
			return
		}
	}
	send(gage.MessageDone(mapStopReason(finish, sawTool)))
}

// mapStopReason normalizes Gemini finish reasons onto the portable StopReason
// constants. Gemini reports STOP even when the turn ended on a function call,
// so a tool call in the turn takes precedence. Unknown values pass through
// verbatim.
func mapStopReason(finish string, sawTool bool) gage.StopReason {
	switch finish {
	case "", "STOP":
		if sawTool {
			return gage.StopToolUse
		}
		return gage.StopEndTurn
	case "MAX_TOKENS":
		return gage.StopMaxTokens
	default:
		return gage.StopReason(finish)
	}
}

// ---- wire types (streamed chunks) ----

type wireChunk struct {
	Candidates []struct {
		Content struct {
			Parts []wirePart `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata *wireUsage `json:"usageMetadata"`
	Error         *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type wirePart struct {
	Text             string `json:"text"`
	Thought          bool   `json:"thought"`
	ThoughtSignature string `json:"thoughtSignature"`
	FunctionCall     *struct {
		Name string          `json:"name"`
		Args json.RawMessage `json:"args"`
	} `json:"functionCall"`
}

type wireUsage struct {
	PromptTokenCount        int `json:"promptTokenCount"`
	CandidatesTokenCount    int `json:"candidatesTokenCount"`
	ThoughtsTokenCount      int `json:"thoughtsTokenCount"`
	CachedContentTokenCount int `json:"cachedContentTokenCount"`
}

func (u wireUsage) toGage() gage.Usage {
	return gage.Usage{
		InputTokens:     u.PromptTokenCount,
		OutputTokens:    u.CandidatesTokenCount,
		ReasoningTokens: u.ThoughtsTokenCount,
		CacheReadTokens: u.CachedContentTokenCount,
	}
}
