package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/deepteams/gage"
	"github.com/deepteams/gage/providers/shared"
	"github.com/deepteams/gage/providers/shared/oauth"
)

// DefaultModel is used when Request.Model is empty.
const DefaultModel = "claude-sonnet-4-5"

// DefaultMaxTokens is applied when the request does not set MaxTokens (the
// Messages API requires it).
const DefaultMaxTokens = 8192

type provider struct {
	url          string
	defaultModel string
	maxTokens    int
	authorize    func(ctx context.Context, req *http.Request) error
	ts           *oauth.TokenSource
	http         *shared.Client
}

// Option configures the Claude Code provider.
type Option func(*provider)

// WithDefaultModel overrides the default model.
func WithDefaultModel(m string) Option { return func(p *provider) { p.defaultModel = m } }

// WithMaxTokens overrides the default max tokens.
func WithMaxTokens(n int) Option { return func(p *provider) { p.maxTokens = n } }

// WithHTTPClient sets a shared HTTP client.
func WithHTTPClient(h *shared.Client) Option { return func(p *provider) { p.http = h } }

// WithMessagesURL overrides the endpoint (testing).
func WithMessagesURL(u string) Option { return func(p *provider) { p.url = u } }

// New builds a Claude Code provider backed by the given TokenStore, which must
// already hold valid credentials (obtained via Login). The provider refreshes
// them transparently. console selects the console OAuth host for refreshes.
func New(store gage.TokenStore, console bool, opts ...Option) gage.Provider {
	ts := &oauth.TokenSource{Config: OAuthConfig(console), Store: store}
	p := &provider{
		url:          MessagesURL,
		defaultModel: DefaultModel,
		maxTokens:    DefaultMaxTokens,
		authorize:    authorizer(ts),
		ts:           ts,
		http:         shared.NewClient("gage/claudecode"),
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

func (p *provider) Name() string { return "claudecode" }

// Stream implements gage.Provider with a single auth-refresh retry on 401.
func (p *provider) Stream(ctx context.Context, req gage.Request) (<-chan gage.Event, error) {
	ch, err := p.stream(ctx, req)
	if err != nil {
		if apiErr, ok := errors.AsType[*gage.APIError](err); ok && apiErr.Status == 401 {
			if _, rerr := p.ts.ForceRefresh(ctx); rerr == nil {
				return p.stream(ctx, req)
			}
		}
	}
	return ch, err
}

func (p *provider) stream(ctx context.Context, req gage.Request) (<-chan gage.Event, error) {
	body, err := p.buildBody(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if p.authorize != nil {
		if err := p.authorize(ctx, httpReq); err != nil {
			return nil, err
		}
	}

	resp, err := p.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		return nil, &gage.APIError{Provider: "claudecode", Status: resp.StatusCode, Body: string(b)}
	}

	out := make(chan gage.Event)
	go p.pump(ctx, resp, out)
	return out, nil
}

func (p *provider) buildBody(req gage.Request) ([]byte, error) {
	model := req.Model
	if model == "" {
		model = p.defaultModel
	}
	maxTokens := req.Options.MaxTokens
	if maxTokens <= 0 {
		maxTokens = p.maxTokens
	}
	body := map[string]any{
		"model":      model,
		"max_tokens": maxTokens,
		"stream":     true,
		"messages":   toAnthropicMessages(req.Messages),
		"system":     systemBlocks(req.System),
	}
	if len(req.Tools) > 0 {
		body["tools"] = toAnthropicTools(req.Tools)
	}
	applyAnthropicOptions(body, req.Options)
	return json.Marshal(body)
}

// systemBlocks builds the system array with the required Claude Code spoof as
// the first block, followed by the caller's system prompt (if any).
func systemBlocks(system string) []map[string]any {
	blocks := []map[string]any{{"type": "text", "text": SystemSpoof}}
	if system != "" {
		blocks = append(blocks, map[string]any{"type": "text", "text": system})
	}
	return blocks
}

func (p *provider) pump(ctx context.Context, resp *http.Response, out chan<- gage.Event) {
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

	// Track content blocks by index (text, thinking, or tool_use).
	blocks := map[int]*anthropicBlock{}
	stopReason := "end_turn"
	var usage gage.Usage
	haveUsage := false

	err := shared.ScanSSE(resp.Body, func(ev shared.SSEEvent) error {
		var e anthropicEvent
		if err := json.Unmarshal([]byte(ev.Data), &e); err != nil {
			return nil
		}
		switch e.Type {
		case "message_start":
			if e.Message != nil && e.Message.Usage != nil {
				usage = usage.Add(e.Message.Usage.toGage())
				haveUsage = true
			}
		case "content_block_start":
			b := &anthropicBlock{kind: e.ContentBlock.Type}
			blocks[e.Index] = b
			if b.kind == "tool_use" {
				b.id = e.ContentBlock.ID
				b.name = e.ContentBlock.Name
				if !send(gage.ToolCallStart(gage.ToolCall{ID: b.id, Name: b.name})) {
					return ctx.Err()
				}
				stopReason = "tool_use"
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
			case "input_json_delta":
				if b != nil && b.kind == "tool_use" {
					b.input = append(b.input, e.Delta.PartialJSON...)
					if !send(gage.ToolCallDelta(gage.ToolCall{ID: b.id, Name: b.name, Input: cloneRaw(b.input)})) {
						return ctx.Err()
					}
				}
			}
		case "content_block_stop":
			if b := blocks[e.Index]; b != nil && b.kind == "tool_use" {
				input := b.input
				if len(input) == 0 {
					input = []byte("{}")
				}
				if !send(gage.ToolCallDone(gage.ToolCall{ID: b.id, Name: b.name, Input: cloneRaw(input)})) {
					return ctx.Err()
				}
			}
		case "message_delta":
			if e.Delta.StopReason != "" {
				stopReason = mapStopReason(e.Delta.StopReason)
			}
			if e.Usage != nil {
				usage = usage.Add(e.Usage.toGage())
				haveUsage = true
			}
		case "error":
			msg := "claudecode stream error"
			if e.Error != nil {
				msg = e.Error.Message
			}
			return fmt.Errorf("%s", msg)
		}
		return nil
	})
	if err != nil {
		send(gage.ErrorEvent(fmt.Errorf("claudecode: %w", err)))
		return
	}
	if haveUsage {
		if !send(gage.UsageEvent(usage)) {
			return
		}
	}
	send(gage.MessageDone(stopReason))
}

type anthropicBlock struct {
	kind  string
	id    string
	name  string
	input []byte
}

func mapStopReason(r string) string {
	switch r {
	case "tool_use":
		return "tool_use"
	case "max_tokens":
		return "max_tokens"
	case "end_turn", "stop_sequence":
		return "end_turn"
	default:
		return r
	}
}

func cloneRaw(b []byte) json.RawMessage {
	return json.RawMessage(append([]byte(nil), b...))
}
