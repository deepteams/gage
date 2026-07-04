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

var defaultResponsesHTTP = shared.NewClient("gage/openai-responses")

// Authorizer decorates an outgoing request with auth headers just before it is
// sent. It runs on every attempt so implementations can refresh tokens. Codex
// uses it to inject its OAuth bearer token and ChatGPT-Account-Id.
type Authorizer func(ctx context.Context, req *http.Request) error

// ResponsesClient talks to an OpenAI Responses API endpoint and streams the
// result as gage.Events. It is the base for the Codex provider.
type ResponsesClient struct {
	ProviderName string
	// URL is the full endpoint (e.g.
	// "https://chatgpt.com/backend-api/codex/responses").
	URL          string
	DefaultModel string
	// Authorize sets auth headers per attempt. Optional.
	Authorize Authorizer
	// Headers are static extra headers.
	Headers map[string]string
	// Store maps to the request "store" field. Codex requires false.
	Store bool
	// HTTP is the shared client. If nil, a package default is used.
	HTTP *shared.Client
}

// Name implements gage.Provider.
func (c *ResponsesClient) Name() string { return c.ProviderName }

func (c *ResponsesClient) http() *shared.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return defaultResponsesHTTP
}

// Stream implements gage.Provider.
func (c *ResponsesClient) Stream(ctx context.Context, req gage.Request) (<-chan gage.Event, error) {
	body, err := c.buildBody(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	for k, v := range c.Headers {
		httpReq.Header.Set(k, v)
	}
	if c.Authorize != nil {
		if err := c.Authorize(ctx, httpReq); err != nil {
			return nil, err
		}
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

func (c *ResponsesClient) buildBody(req gage.Request) ([]byte, error) {
	model := req.Model
	if model == "" {
		model = c.DefaultModel
	}
	body := map[string]any{
		"model":  model,
		"input":  toResponsesInput(req.Messages),
		"stream": true,
		"store":  c.Store,
	}
	if req.System != "" {
		body["instructions"] = req.System
	}
	if len(req.Tools) > 0 {
		body["tools"] = toResponsesTools(req.Tools)
	}
	if req.Options.ReasoningEffort != gage.ReasoningNone {
		body["reasoning"] = map[string]any{"effort": string(req.Options.ReasoningEffort)}
	}
	if req.Options.MaxTokens > 0 {
		body["max_output_tokens"] = req.Options.MaxTokens
	}
	if req.Options.Temperature != nil {
		body["temperature"] = *req.Options.Temperature
	}
	if req.Options.TopP != nil {
		body["top_p"] = *req.Options.TopP
	}
	maps.Copy(body, req.Options.Extra)
	return json.Marshal(body)
}

func (c *ResponsesClient) pump(ctx context.Context, resp *http.Response, out chan<- gage.Event) {
	defer close(out)
	defer resp.Body.Close()

	send := func(e gage.Event) bool {
		select {
		case out <- e:
			return true
		case <-ctx.Done():
			return false
		}
	}
	if !send(gage.MessageStart()) {
		return
	}

	// Track streamed function calls by their output item id.
	calls := map[string]*gage.ToolCall{}
	stopReason := "end_turn"

	err := shared.ScanSSE(resp.Body, func(ev shared.SSEEvent) error {
		var re responsesEvent
		if err := json.Unmarshal([]byte(ev.Data), &re); err != nil {
			return nil
		}
		// Prefer the explicit event name, fall back to the "type" field.
		typ := ev.Event
		if typ == "" {
			typ = re.Type
		}
		switch typ {
		case "response.output_text.delta":
			if re.Delta != "" && !send(gage.TextDelta(re.Delta)) {
				return ctx.Err()
			}
		case "response.reasoning_text.delta", "response.reasoning_summary_text.delta":
			if re.Delta != "" && !send(gage.ReasoningDelta(re.Delta)) {
				return ctx.Err()
			}
		case "response.output_item.added":
			if re.Item != nil && re.Item.Type == "function_call" {
				tc := &gage.ToolCall{ID: itemCallID(re.Item), Name: re.Item.Name}
				calls[re.Item.ID] = tc
				stopReason = "tool_use"
				if !send(gage.ToolCallStart(*tc)) {
					return ctx.Err()
				}
			}
		case "response.function_call_arguments.delta":
			if tc := calls[re.ItemID]; tc != nil && re.Delta != "" {
				tc.Input = append(tc.Input, re.Delta...)
				if !send(gage.ToolCallDelta(gage.ToolCall{ID: tc.ID, Name: tc.Name, Input: cloneRaw(tc.Input)})) {
					return ctx.Err()
				}
			}
		case "response.function_call_arguments.done":
			if tc := calls[re.ItemID]; tc != nil {
				input := json.RawMessage(re.Arguments)
				if len(input) == 0 {
					input = cloneRaw(tc.Input)
				}
				if len(input) == 0 {
					input = json.RawMessage("{}")
				}
				if !send(gage.ToolCallDone(gage.ToolCall{ID: tc.ID, Name: tc.Name, Input: input})) {
					return ctx.Err()
				}
			}
		case "response.completed":
			if re.Response != nil && re.Response.Usage != nil {
				if !send(gage.UsageEvent(re.Response.Usage.toGage())) {
					return ctx.Err()
				}
			}
		case "response.failed", "error":
			msg := "responses stream failed"
			if re.Response != nil && re.Response.Error != nil {
				msg = re.Response.Error.Message
			}
			return fmt.Errorf("%s", msg)
		}
		return nil
	})
	if err != nil {
		send(gage.ErrorEvent(fmt.Errorf("%s: %w", c.ProviderName, err)))
		return
	}
	send(gage.MessageDone(stopReason))
}

func itemCallID(it *responsesItem) string {
	if it.CallID != "" {
		return it.CallID
	}
	return it.ID
}

func cloneRaw(b []byte) json.RawMessage {
	return json.RawMessage(append([]byte(nil), b...))
}
