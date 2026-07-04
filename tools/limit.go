package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/deepteams/gage"
)

// LimitConcurrency wraps a Tool with a semaphore limiting concurrent
// executions. A max <= 0 leaves the tool unwrapped.
func LimitConcurrency(t gage.Tool, max int) gage.Tool {
	if max <= 0 {
		return t
	}
	return &limitedTool{forwarding: forwarding{t}, sem: make(chan struct{}, max)}
}

// LimitConcurrencyAll wraps every tool with LimitConcurrency.
func LimitConcurrencyAll(tools []gage.Tool, max int) []gage.Tool {
	if max <= 0 {
		return tools
	}
	out := make([]gage.Tool, len(tools))
	for i, t := range tools {
		out[i] = LimitConcurrency(t, max)
	}
	return out
}

type limitedTool struct {
	forwarding
	sem chan struct{}
}

func (t *limitedTool) Execute(ctx context.Context, input json.RawMessage) (gage.ToolResult, error) {
	select {
	case t.sem <- struct{}{}:
		defer func() { <-t.sem }()
	case <-ctx.Done():
		// Cancellation while waiting is an infrastructure failure, not a tool
		// failure the model should react to.
		return gage.ToolResult{}, fmt.Errorf("tool %s: concurrency wait: %w", t.Name(), ctx.Err())
	}
	return t.Tool.Execute(ctx, input)
}

// LimitResultSize wraps a Tool so the total text content of its results is
// capped at maxBytes; oversized text is cut and a "...(result truncated)"
// marker appended, so giant results (MCP servers, custom tools) cannot blow
// the context window. Non-text content parts pass through untouched. A
// maxBytes <= 0 leaves the tool unwrapped.
func LimitResultSize(t gage.Tool, maxBytes int) gage.Tool {
	if maxBytes <= 0 {
		return t
	}
	return &sizeLimitedTool{forwarding: forwarding{t}, max: maxBytes}
}

// LimitResultSizeAll wraps every tool with LimitResultSize.
func LimitResultSizeAll(tools []gage.Tool, maxBytes int) []gage.Tool {
	if maxBytes <= 0 {
		return tools
	}
	out := make([]gage.Tool, len(tools))
	for i, t := range tools {
		out[i] = LimitResultSize(t, maxBytes)
	}
	return out
}

type sizeLimitedTool struct {
	forwarding
	max int
}

const truncationMarker = "\n...(result truncated)"

func (t *sizeLimitedTool) Execute(ctx context.Context, input json.RawMessage) (gage.ToolResult, error) {
	res, err := t.Tool.Execute(ctx, input)
	if err != nil {
		return res, err
	}
	total := 0
	for _, p := range res.Content {
		if p.Kind == gage.PartText {
			total += len(p.Text)
		}
	}
	if total <= t.max {
		return res, nil
	}
	budget := t.max
	content := make([]gage.ContentPart, 0, len(res.Content)+1)
	for _, p := range res.Content {
		if p.Kind != gage.PartText {
			content = append(content, p)
			continue
		}
		if budget <= 0 {
			continue // drop wholly over-budget text parts
		}
		if len(p.Text) > budget {
			p.Text = p.Text[:budget]
		}
		budget -= len(p.Text)
		content = append(content, p)
	}
	content = append(content, gage.TextPart(truncationMarker))
	res.Content = content
	return res, nil
}
