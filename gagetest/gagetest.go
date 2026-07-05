// Package gagetest provides a scripted, in-memory [gage.Provider] for testing
// agents built on gage without any network access.
//
// A [Provider] replays a FIFO queue of scripted turns: each Stream call pops
// the next turn and emits the canonical provider event sequence —
//
//	message_start → (text_delta | reasoning_delta | reasoning_done |
//	tool_call_start/delta/end)* → usage → message_done
//
// — then closes the channel, exactly like a real backend. Text is split into
// multiple deltas so accumulation logic is exercised, every send honors ctx
// cancellation, and the agent loop (including multi-turn tool cycles) runs
// unmodified against it.
//
//	p := gagetest.NewProvider("")
//	p.Enqueue(
//		gagetest.Calls(gagetest.Call("c1", "weather", map[string]any{"city": "Paris"})).WithText("checking..."),
//		gagetest.Text("It is sunny in Paris."),
//	)
//
// Failures are injected with [Provider.EnqueueError] (the stream emits an
// error event, then closes) or [Provider.EnqueueStreamError] (Stream itself
// returns the error). Every request the provider receives is recorded and
// available via [Provider.Requests] for assertions on system prompt, history,
// advertised tools, and options.
//
// See the package example for a complete agent round-trip with a tool.
package gagetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"unicode/utf8"

	"github.com/deepteams/gage"
)

// ErrScriptExhausted is returned (wrapped) by Stream when it is called with no
// scripted turns left: the test forgot to Enqueue one.
var ErrScriptExhausted = errors.New("gagetest: script exhausted")

// defaultUsage is reported for turns without an explicit WithUsage. It is
// small but non-zero so token budgets and compaction thresholds remain
// exercisable in tests.
var defaultUsage = gage.Usage{InputTokens: 8, OutputTokens: 4}

// Turn scripts one full assistant message: optional reasoning, text, and tool
// calls. Zero values get sensible defaults at stream time: usage defaults to a
// small non-zero value, and the stop reason defaults to [gage.StopToolUse]
// when the turn carries tool calls, [gage.StopEndTurn] otherwise.
//
// Build turns with [Text] or [Calls], then chain the With* methods.
type Turn struct {
	text      string
	reasoning string
	signature string
	calls     []gage.ToolCall
	usage     *gage.Usage
	stop      gage.StopReason
}

// Text builds a Turn whose assistant message is plain text.
func Text(s string) Turn { return Turn{text: s} }

// Calls builds a Turn that requests the given tool calls. Combine with
// WithText for the common "narrate then call" shape.
func Calls(calls ...gage.ToolCall) Turn { return Turn{calls: calls} }

// Call builds a tool call for use with [Calls] or [Turn.WithCalls]. input is
// marshaled to JSON; a json.RawMessage, []byte, or string is used verbatim as
// the raw JSON arguments, and nil becomes {}. Call panics if input cannot be
// marshaled — that is a bug in the test, not a scripted failure.
func Call(id, name string, input any) gage.ToolCall {
	var raw json.RawMessage
	switch v := input.(type) {
	case nil:
		raw = json.RawMessage(`{}`)
	case json.RawMessage:
		raw = append(json.RawMessage(nil), v...)
	case []byte:
		raw = append(json.RawMessage(nil), v...)
	case string:
		raw = json.RawMessage(v)
	default:
		b, err := json.Marshal(input)
		if err != nil {
			panic(fmt.Sprintf("gagetest: Call(%q, %q): marshal input: %v", id, name, err))
		}
		raw = b
	}
	return gage.ToolCall{ID: id, Name: name, Input: raw}
}

// WithText sets the turn's visible assistant text.
func (t Turn) WithText(s string) Turn { t.text = s; return t }

// WithReasoning adds a reasoning block, streamed as reasoning deltas followed
// by reasoning_done (with an empty signature).
func (t Turn) WithReasoning(s string) Turn { t.reasoning = s; return t }

// WithSignedReasoning adds a reasoning block whose reasoning_done carries the
// given replay signature, mimicking providers that require signed replay.
func (t Turn) WithSignedReasoning(s, signature string) Turn {
	t.reasoning, t.signature = s, signature
	return t
}

// WithCalls sets the turn's tool calls.
func (t Turn) WithCalls(calls ...gage.ToolCall) Turn { t.calls = calls; return t }

// WithUsage overrides the usage reported by the turn's usage event.
func (t Turn) WithUsage(u gage.Usage) Turn { t.usage = &u; return t }

// WithStop overrides the turn's stop reason.
func (t Turn) WithStop(reason gage.StopReason) Turn { t.stop = reason; return t }

// step is one queue entry: a scripted turn, a mid-stream error, or a
// pre-stream error.
type step struct {
	turn      Turn
	errTurn   error // emit EventError then close
	streamErr error // Stream returns this error
}

// Provider is a scripted, thread-safe [gage.Provider]. Script it with
// Enqueue/EnqueueError/EnqueueStreamError before (or between) Stream calls;
// each Stream call consumes the next entry in FIFO order. Concurrent Stream
// calls are safe and pop distinct entries.
//
// The zero value is not usable; construct with [NewProvider].
type Provider struct {
	name string

	mu       sync.Mutex
	script   []step
	requests []gage.Request
}

var _ gage.Provider = (*Provider)(nil)

// NewProvider builds a scripted provider. name is what Name() reports;
// empty defaults to "gagetest".
func NewProvider(name string) *Provider {
	if name == "" {
		name = "gagetest"
	}
	return &Provider{name: name}
}

// Name identifies the provider for telemetry and logs.
func (p *Provider) Name() string { return p.name }

// Enqueue appends scripted turns to the script, one per future Stream call.
func (p *Provider) Enqueue(turns ...Turn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, t := range turns {
		p.script = append(p.script, step{turn: t})
	}
}

// EnqueueError appends a failing turn: the corresponding Stream call succeeds
// but the stream emits an error event carrying err, then closes. Use it to
// inject mid-conversation failures.
func (p *Provider) EnqueueError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.script = append(p.script, step{errTurn: err})
}

// EnqueueStreamError appends a pre-stream failure: the corresponding Stream
// call returns err directly, before any event is produced.
func (p *Provider) EnqueueStreamError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.script = append(p.script, step{streamErr: err})
}

// Requests returns copies of every request received so far, in order. The
// copies are deep enough that later mutation of the caller's messages, tools,
// or options does not affect them.
func (p *Provider) Requests() []gage.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]gage.Request(nil), p.requests...)
}

// Remaining reports how many scripted entries have not been consumed yet.
// Assert it is zero at the end of a test to prove the whole script ran.
func (p *Provider) Remaining() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.script)
}

// Stream records req and replays the next scripted entry as the canonical
// event sequence. It returns an error wrapping [ErrScriptExhausted] when the
// script is empty. The request is recorded even for error entries.
func (p *Provider) Stream(ctx context.Context, req gage.Request) (<-chan gage.Event, error) {
	p.mu.Lock()
	p.requests = append(p.requests, copyRequest(req))
	if len(p.script) == 0 {
		n := len(p.requests)
		p.mu.Unlock()
		return nil, fmt.Errorf("%w: provider %q got request #%d with nothing enqueued", ErrScriptExhausted, p.name, n)
	}
	st := p.script[0]
	p.script = p.script[1:]
	p.mu.Unlock()

	if st.streamErr != nil {
		return nil, st.streamErr
	}
	ch := make(chan gage.Event)
	go emit(ctx, ch, st)
	return ch, nil
}

// emit streams one scripted entry, honoring ctx on every send, and closes ch.
func emit(ctx context.Context, ch chan<- gage.Event, st step) {
	defer close(ch)
	send := func(e gage.Event) bool {
		select {
		case ch <- e:
			return true
		case <-ctx.Done():
			return false
		}
	}

	if st.errTurn != nil {
		send(gage.ErrorEvent(st.errTurn))
		return
	}

	t := st.turn
	if !send(gage.MessageStart()) {
		return
	}
	if t.reasoning != "" {
		for _, chunk := range splitChunks(t.reasoning) {
			if !send(gage.ReasoningDelta(chunk)) {
				return
			}
		}
		if !send(gage.ReasoningDone(t.signature)) {
			return
		}
	}
	for _, chunk := range splitChunks(t.text) {
		if !send(gage.TextDelta(chunk)) {
			return
		}
	}
	for _, tc := range t.calls {
		if !send(gage.ToolCallStart(gage.ToolCall{ID: tc.ID, Name: tc.Name})) {
			return
		}
		if !send(gage.ToolCallDelta(cloneCall(tc))) {
			return
		}
		if !send(gage.ToolCallDone(cloneCall(tc))) {
			return
		}
	}
	usage := defaultUsage
	if t.usage != nil {
		usage = *t.usage
	}
	if !send(gage.UsageEvent(usage)) {
		return
	}
	stop := t.stop
	if stop == "" {
		if len(t.calls) > 0 {
			stop = gage.StopToolUse
		} else {
			stop = gage.StopEndTurn
		}
	}
	send(gage.MessageDone(stop))
}

// splitChunks splits s into two rune-safe deltas so consumers must
// accumulate; short strings yield a single chunk, empty yields none.
func splitChunks(s string) []string {
	if s == "" {
		return nil
	}
	mid := len(s) / 2
	for mid > 0 && !utf8.RuneStart(s[mid]) {
		mid--
	}
	if mid == 0 {
		return []string{s}
	}
	return []string{s[:mid], s[mid:]}
}

func cloneCall(tc gage.ToolCall) gage.ToolCall {
	tc.Input = append(json.RawMessage(nil), tc.Input...)
	return tc
}

// copyRequest makes a copy of req deep enough for post-hoc assertions:
// message content, tool schemas, and options no longer alias the caller's
// slices, maps, or pointees.
func copyRequest(req gage.Request) gage.Request {
	out := req
	out.Messages = copyMessages(req.Messages)
	if req.Tools != nil {
		out.Tools = make([]gage.ToolSchema, len(req.Tools))
		for i, ts := range req.Tools {
			ts.Parameters = append(gage.JSONSchema(nil), ts.Parameters...)
			out.Tools[i] = ts
		}
	}
	out.Options = copyOptions(req.Options)
	return out
}

func copyMessages(msgs []gage.Message) []gage.Message {
	if msgs == nil {
		return nil
	}
	out := make([]gage.Message, len(msgs))
	for i, m := range msgs {
		m.Content = copyParts(m.Content)
		out[i] = m
	}
	return out
}

func copyParts(parts []gage.ContentPart) []gage.ContentPart {
	if parts == nil {
		return nil
	}
	out := make([]gage.ContentPart, len(parts))
	for i, p := range parts {
		if p.ToolCall != nil {
			tc := cloneCall(*p.ToolCall)
			p.ToolCall = &tc
		}
		if p.ToolResult != nil {
			tr := *p.ToolResult
			tr.Content = copyParts(tr.Content)
			p.ToolResult = &tr
		}
		if p.Image != nil {
			img := *p.Image
			p.Image = &img
		}
		if p.Document != nil {
			doc := *p.Document
			p.Document = &doc
		}
		out[i] = p
	}
	return out
}

func copyOptions(o gage.GenerateOptions) gage.GenerateOptions {
	out := o
	if o.Temperature != nil {
		v := *o.Temperature
		out.Temperature = &v
	}
	if o.TopP != nil {
		v := *o.TopP
		out.TopP = &v
	}
	if o.StopSequences != nil {
		out.StopSequences = append([]string(nil), o.StopSequences...)
	}
	if o.ToolChoice != nil {
		v := *o.ToolChoice
		out.ToolChoice = &v
	}
	if o.ResponseFormat != nil {
		v := *o.ResponseFormat
		v.Schema = append(gage.JSONSchema(nil), v.Schema...)
		out.ResponseFormat = &v
	}
	if o.Extra != nil {
		out.Extra = make(map[string]any, len(o.Extra))
		for k, val := range o.Extra {
			out.Extra[k] = val
		}
	}
	return out
}
