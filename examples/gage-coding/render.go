package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/deepteams/gage"
)

// renderer prints the agent's event stream. It resets its partial state on
// every message_start: after a mid-stream retry (agent.Config.MaxStreamRetries)
// the turn restarts and the previous partial output must be discarded.
type renderer struct {
	out       io.Writer
	midText   bool
	reasoning bool
}

func (r *renderer) handle(ev gage.Event) {
	switch ev.Type {
	case gage.EventMessageStart:
		if r.midText || r.reasoning {
			fmt.Fprint(r.out, "\x1b[0m\n(retrying turn)\n")
		}
		r.midText, r.reasoning = false, false
	case gage.EventTextDelta:
		if r.reasoning {
			fmt.Fprint(r.out, "\x1b[0m\n")
			r.reasoning = false
		}
		fmt.Fprint(r.out, ev.Text)
		r.midText = true
	case gage.EventReasoningDelta:
		if !r.reasoning {
			fmt.Fprint(r.out, "\x1b[2m") // dim thinking text
			r.reasoning = true
		}
		fmt.Fprint(r.out, ev.Text)
	case gage.EventReasoningDone:
		if r.reasoning {
			fmt.Fprint(r.out, "\x1b[0m\n")
			r.reasoning = false
		}
	case gage.EventToolCallDone:
		r.breakLine()
		fmt.Fprintf(r.out, "\x1b[36m⏺ %s\x1b[0m %s\n", ev.ToolCall.Name, compactJSON(ev.ToolCall.Input, 120))
	case gage.EventToolResult:
		status := "ok"
		if ev.ToolResult.IsError {
			status = "\x1b[31merror\x1b[0m"
		}
		fmt.Fprintf(r.out, "  ⎿ %s %s\n", status, truncate(resultText(ev.ToolResult), 160))
	case gage.EventMessageDone:
		r.breakLine()
	case gage.EventError:
		r.breakLine()
		fmt.Fprintf(r.out, "\x1b[31m✗ %v\x1b[0m\n", ev.Err)
	}
}

// breakLine closes any open text/reasoning run before printing a block line.
func (r *renderer) breakLine() {
	if r.reasoning {
		fmt.Fprint(r.out, "\x1b[0m")
		r.reasoning = false
	}
	if r.midText {
		fmt.Fprintln(r.out)
		r.midText = false
	}
}

func resultText(tr *gage.ToolResult) string {
	var b strings.Builder
	for _, p := range tr.Content {
		if p.Kind == gage.PartText {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

func compactJSON(raw json.RawMessage, max int) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return truncate(string(raw), max)
	}
	return truncate(buf.String(), max)
}

func truncate(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if runes := []rune(s); len(runes) > max {
		return string(runes[:max]) + "…"
	}
	return s
}
