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
	return &limitedTool{Tool: t, sem: make(chan struct{}, max)}
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
	gage.Tool
	sem chan struct{}
}

func (t *limitedTool) Execute(ctx context.Context, input json.RawMessage) (gage.ToolResult, error) {
	select {
	case t.sem <- struct{}{}:
		defer func() { <-t.sem }()
	case <-ctx.Done():
		return gage.ErrorResult("", fmt.Sprintf("tool %s concurrency wait cancelled: %v", t.Name(), ctx.Err())), nil
	}
	return t.Tool.Execute(ctx, input)
}

func (t *limitedTool) Metadata() gage.ToolMetadata {
	return gage.MetadataOf(t.Tool)
}

func (t *limitedTool) DescribeCall(input json.RawMessage) string {
	return gage.CallSummaryOf(t.Tool, input)
}
