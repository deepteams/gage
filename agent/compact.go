package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/deepteams/gage"
)

// Trim returns a Compactor that keeps the first message and roughly the last
// keep messages, dropping the middle. The cut never orphans a tool result:
// it advances past RoleTool messages so every kept result keeps the assistant
// message that requested it. Dropped content is replaced by a short user note
// so the model knows history was elided.
func Trim(keep int) gage.Compactor {
	return gage.CompactorFunc(func(_ context.Context, msgs []gage.Message, _ gage.Usage) ([]gage.Message, gage.Usage, error) {
		if keep <= 0 || len(msgs) <= keep+1 {
			return msgs, gage.Usage{}, nil
		}
		cut := len(msgs) - keep
		// Never start the kept tail on a tool result whose tool_use was dropped.
		for cut < len(msgs) && msgs[cut].Role == gage.RoleTool {
			cut++
		}
		if cut <= 1 || cut >= len(msgs) {
			return msgs, gage.Usage{}, nil
		}
		out := make([]gage.Message, 0, len(msgs)-cut+2)
		out = append(out, msgs[0])
		out = append(out, gage.UserText(fmt.Sprintf("[%d earlier messages elided to fit the context window]", cut-1)))
		out = append(out, msgs[cut:]...)
		return out, gage.Usage{}, nil
	})
}

// Summarize returns a Compactor that replaces the oldest part of the
// conversation with a model-written summary, keeping the first message and
// the last keep messages verbatim. The summary call goes through p (using
// model, which may be empty for provider-pinned models) and does not stream
// to the caller; its token usage is returned so the agent counts it toward
// the run total.
func Summarize(p gage.Provider, model string, keep int) gage.Compactor {
	return gage.CompactorFunc(func(ctx context.Context, msgs []gage.Message, _ gage.Usage) ([]gage.Message, gage.Usage, error) {
		if keep <= 0 || len(msgs) <= keep+1 {
			return msgs, gage.Usage{}, nil
		}
		cut := len(msgs) - keep
		for cut < len(msgs) && msgs[cut].Role == gage.RoleTool {
			cut++
		}
		if cut <= 1 || cut >= len(msgs) {
			return msgs, gage.Usage{}, nil
		}

		var transcript strings.Builder
		for _, m := range msgs[1:cut] {
			transcript.WriteString(string(m.Role))
			transcript.WriteString(": ")
			transcript.WriteString(renderForSummary(m))
			transcript.WriteString("\n")
		}
		req := gage.Request{
			Model:  model,
			System: "You compress agent conversations. Summarize the transcript into the facts, decisions, tool outcomes, and open threads a model needs to continue the task. Be dense and factual.",
			Messages: []gage.Message{
				gage.UserText(transcript.String()),
			},
		}
		stream, err := p.Stream(ctx, req)
		if err != nil {
			return nil, gage.Usage{}, fmt.Errorf("summarize compaction: %w", err)
		}
		var summary strings.Builder
		var used gage.Usage
		for ev := range stream {
			switch ev.Type {
			case gage.EventTextDelta:
				summary.WriteString(ev.Text)
			case gage.EventUsage:
				if ev.Usage != nil {
					used = *ev.Usage
				}
			case gage.EventError:
				return nil, used, fmt.Errorf("summarize compaction: %w", ev.Err)
			}
		}
		if ctx.Err() != nil {
			return nil, used, ctx.Err()
		}

		out := make([]gage.Message, 0, keep+2)
		out = append(out, msgs[0])
		out = append(out, gage.UserText("[Summary of the elided earlier conversation]\n"+summary.String()))
		out = append(out, msgs[cut:]...)
		return out, used, nil
	})
}

// renderForSummary flattens a message into plain text for the summary prompt.
func renderForSummary(m gage.Message) string {
	var b strings.Builder
	for _, p := range m.Content {
		switch p.Kind {
		case gage.PartText:
			b.WriteString(p.Text)
		case gage.PartToolUse:
			if p.ToolCall != nil {
				fmt.Fprintf(&b, "[called %s %s]", p.ToolCall.Name, string(p.ToolCall.Input))
			}
		case gage.PartToolResult:
			if p.ToolResult != nil {
				status := "ok"
				if p.ToolResult.IsError {
					status = "error"
				}
				fmt.Fprintf(&b, "[tool %s: %s]", status, p.ToolResult.Text())
			}
		}
	}
	return b.String()
}
