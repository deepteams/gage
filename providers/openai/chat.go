// Package openai implements the OpenAI-compatible wire formats reused by
// several providers: the Chat Completions API (chat.go) and the Responses API
// (responses.go). It is not a standalone provider; OpenRouter, vLLM, Ollama and
// Codex embed a ChatClient/ResponsesClient configured with their endpoint.
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"

	"github.com/deepteams/gage"
	"github.com/deepteams/gage/providers/shared"
)

var defaultChatHTTP = shared.NewClient("gage/openai")

// ChatClient talks to an OpenAI-compatible /chat/completions endpoint and
// streams the response as gage.Events.
type ChatClient struct {
	// ProviderName is reported by Name() (e.g. "openrouter").
	ProviderName string
	// BaseURL is the API root without a trailing slash (e.g.
	// "https://openrouter.ai/api/v1"). The path "/chat/completions" is appended.
	BaseURL string
	// APIKey, when set, is sent as a Bearer token. Optional (vLLM/Ollama).
	APIKey string
	// Headers are extra headers added to every request.
	Headers map[string]string
	// DefaultModel is used when Request.Model is empty.
	DefaultModel string
	// HTTP is the shared client. If nil, a package default is used.
	HTTP *shared.Client
}

// Name implements gage.Provider.
func (c *ChatClient) Name() string { return c.ProviderName }

func (c *ChatClient) http() *shared.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return defaultChatHTTP
}

// Stream implements gage.Provider.
func (c *ChatClient) Stream(ctx context.Context, req gage.Request) (<-chan gage.Event, error) {
	body, err := c.buildBody(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if c.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	for k, v := range c.Headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := c.http().Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		return nil, &gage.APIError{Provider: c.ProviderName, Status: resp.StatusCode, Body: string(b)}
	}

	out := make(chan gage.Event)
	go c.pump(ctx, resp, out)
	return out, nil
}

func (c *ChatClient) buildBody(req gage.Request) ([]byte, error) {
	model := req.Model
	if model == "" {
		model = c.DefaultModel
	}
	if model == "" {
		return nil, fmt.Errorf("%s: no model specified", c.ProviderName)
	}
	msgs := toChatMessages(req.System, req.Messages)
	body := map[string]any{
		"model":    model,
		"messages": msgs,
		"stream":   true,
		"stream_options": map[string]any{
			"include_usage": true,
		},
	}
	if len(req.Tools) > 0 {
		body["tools"] = toChatTools(req.Tools)
	}
	if err := applyChatOptions(body, req.Options, c.ProviderName); err != nil {
		return nil, err
	}
	maps.Copy(body, req.Options.Extra)
	return json.Marshal(body)
}

// pump reads the SSE stream and emits gage.Events. It always closes out.
func (c *ChatClient) pump(ctx context.Context, resp *http.Response, out chan<- gage.Event) {
	defer close(out)
	defer resp.Body.Close()

	send := func(e gage.Event) bool { return shared.Send(ctx, out, e) }

	if !send(gage.MessageStart()) {
		return
	}

	acc := newToolAccumulator()
	stopReason := gage.StopEndTurn
	var usage *gage.Usage

	err := shared.ScanSSE(resp.Body, func(ev shared.SSEEvent) error {
		var chunk chatChunk
		if err := json.Unmarshal([]byte(ev.Data), &chunk); err != nil {
			// Ignore keepalive/non-JSON payloads.
			return nil
		}
		if chunk.Error != nil {
			// Mid-stream {"error":{...}} payload (OpenRouter/vLLM style): the
			// generation failed; end the stream with an error event.
			return chunk.Error.toAPIError(c.ProviderName)
		}
		if chunk.Usage != nil {
			u := chunk.Usage.toGage()
			usage = &u
		}
		for _, ch := range chunk.Choices {
			if ch.FinishReason != "" {
				stopReason = mapFinishReason(ch.FinishReason)
			}
			d := ch.Delta
			if d.Content != "" {
				if !send(gage.TextDelta(d.Content)) {
					return ctx.Err()
				}
			}
			if d.Reasoning != "" {
				if !send(gage.ReasoningDelta(d.Reasoning)) {
					return ctx.Err()
				}
			}
			if d.ReasoningContent != "" {
				if !send(gage.ReasoningDelta(d.ReasoningContent)) {
					return ctx.Err()
				}
			}
			for _, tc := range d.ToolCalls {
				for _, e := range acc.apply(tc) {
					if !send(e) {
						return ctx.Err()
					}
				}
			}
		}
		return nil
	})

	if err != nil {
		// Do not flush accumulated tool calls: the stream ended mid-call and
		// their arguments may be truncated. Emit only the error.
		send(gage.ErrorEvent(fmt.Errorf("%s: stream: %w", c.ProviderName, err)))
		return
	}
	// The Chat API signals argument completion only via stream end, so
	// ToolCallDone events for every accumulated call are emitted here.
	for _, e := range acc.finish() {
		if !send(e) {
			return
		}
	}
	if usage != nil {
		if !send(gage.UsageEvent(*usage)) {
			return
		}
	}
	send(gage.MessageDone(stopReason))
}

func mapFinishReason(r string) gage.StopReason {
	switch r {
	case "tool_calls", "function_call":
		return gage.StopToolUse
	case "length":
		return gage.StopMaxTokens
	case "stop":
		return gage.StopEndTurn
	case "content_filter":
		return gage.StopContentFilter
	default:
		return gage.StopReason(r)
	}
}
