package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/deepteams/gage"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// createMessage serves one sampling/createMessage request with the given
// provider: it maps the MCP request onto a gage.Request, streams the provider
// to completion, and returns the accumulated text as the assistant message.
func createMessage(ctx context.Context, p gage.Provider, params *mcpsdk.CreateMessageParams) (*mcpsdk.CreateMessageResult, error) {
	req, err := samplingRequest(params)
	if err != nil {
		return nil, err
	}

	events, err := p.Stream(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("mcp: sampling: %w", err)
	}
	var text strings.Builder
	var stop gage.StopReason
	for ev := range events {
		switch ev.Type {
		case gage.EventTextDelta:
			text.WriteString(ev.Text)
		case gage.EventMessageDone:
			stop = ev.StopReason
		case gage.EventError:
			if ev.Err != nil {
				return nil, fmt.Errorf("mcp: sampling: %w", ev.Err)
			}
			return nil, fmt.Errorf("mcp: sampling: %s", ev.ErrorString)
		}
	}

	return &mcpsdk.CreateMessageResult{
		Role:       "assistant",
		Content:    &mcpsdk.TextContent{Text: text.String()},
		Model:      p.Name(),
		StopReason: mcpStopReason(stop),
	}, nil
}

// samplingRequest maps an MCP sampling request onto a gage.Request. Only text
// content is supported; anything else is rejected so the server gets a clear
// JSON-RPC error instead of silently degraded input.
func samplingRequest(params *mcpsdk.CreateMessageParams) (gage.Request, error) {
	if params == nil {
		return gage.Request{}, errors.New("mcp: sampling: missing params")
	}
	req := gage.Request{System: params.SystemPrompt}
	for i, m := range params.Messages {
		tc, ok := m.Content.(*mcpsdk.TextContent)
		if !ok {
			return gage.Request{}, fmt.Errorf("mcp: sampling: message %d has unsupported %T content (only text is supported)", i, m.Content)
		}
		role := gage.RoleUser
		if m.Role == "assistant" {
			role = gage.RoleAssistant
		}
		req.Messages = append(req.Messages, gage.Message{
			Role:    role,
			Content: []gage.ContentPart{gage.TextPart(tc.Text)},
		})
	}
	req.Options.MaxTokens = int(params.MaxTokens)
	if params.Temperature != 0 {
		t := params.Temperature
		req.Options.Temperature = &t
	}
	req.Options.StopSequences = params.StopSequences
	return req, nil
}

// mcpStopReason maps a gage stop reason onto the MCP standard values; unknown
// reasons pass through verbatim.
func mcpStopReason(s gage.StopReason) string {
	switch s {
	case gage.StopEndTurn, "":
		return "endTurn"
	case gage.StopMaxTokens:
		return "maxTokens"
	case gage.StopSequence:
		return "stopSequence"
	case gage.StopToolUse:
		return "toolUse"
	default:
		return string(s)
	}
}
