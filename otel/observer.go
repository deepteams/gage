// Package otelgage adapts the gage agent's Observer port onto OpenTelemetry
// tracing.
//
// NewObserver returns an agent.Observer that maps the agent's lifecycle
// observations onto spans, loosely following the OpenTelemetry GenAI semantic
// conventions where the port exposes the data:
//
//   - run_start / run_end   → root span "invoke_agent <agent>"
//     (gen_ai.operation.name = "invoke_agent", gen_ai.agent.name,
//     gen_ai.provider.name, gage.run.id)
//   - turn_start / turn_end → child span "chat <provider>" per provider call
//     (gen_ai.operation.name = "chat", gen_ai.usage.input_tokens,
//     gen_ai.usage.output_tokens, plus gage.usage.* for cache and reasoning
//     tokens)
//   - tool_start / tool_end → child span "execute_tool <tool>"
//     (gen_ai.operation.name = "execute_tool", gen_ai.tool.name,
//     gen_ai.tool.call.id)
//   - turn_retry            → span event "gage.turn.retry" on the open turn
//     span (or the run span when no turn span is open)
//   - run_paused            → span event "gage.run.paused" on the run span
//
// The agent.Observation port carries the provider name but not the model id,
// so gen_ai.request.model is not emitted; gen_ai.provider.name is set
// instead. Failures (IsError / ErrorString) become an error span status.
//
// Turn spans and tool spans are children of the run span. Tool execution
// happens after the turn's provider call has completed, so tool spans are
// siblings of the turn spans, tagged with gage.turn for correlation.
//
// Begin/end pairs are correlated on the ids the port provides (RunID,
// RunID+Turn, RunID+CallID) in a mutex-protected map; an end observation
// without a matching begin still produces a (synthesized) span, and run_end
// closes any spans left open by a crashed run. The Observer contract is
// fire-and-forget: Observe never returns an error and never panics on
// out-of-order input.
//
// This package is a nested Go module (github.com/deepteams/gage/otel) so the
// core gage module stays free of OpenTelemetry dependencies. Its go.mod
// carries `replace github.com/deepteams/gage => ../`, which makes in-repo
// development build against the sibling checkout; once the repository is
// tagged, consumers resolve github.com/deepteams/gage by version and the
// replace directive only affects builds inside this repository.
package otelgage

import (
	"context"
	"sync"
	"time"

	"github.com/deepteams/gage/agent"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// tracerName is the instrumentation scope name of the spans this package
// produces.
const tracerName = "github.com/deepteams/gage/otel"

// GenAI semantic-convention attribute keys (plus gage.* extensions for data
// the conventions do not cover).
const (
	attrOperationName = attribute.Key("gen_ai.operation.name")
	attrAgentName     = attribute.Key("gen_ai.agent.name")
	attrProviderName  = attribute.Key("gen_ai.provider.name")
	attrUsageInput    = attribute.Key("gen_ai.usage.input_tokens")
	attrUsageOutput   = attribute.Key("gen_ai.usage.output_tokens")
	attrToolName      = attribute.Key("gen_ai.tool.name")
	attrToolCallID    = attribute.Key("gen_ai.tool.call.id")
	attrRunID         = attribute.Key("gage.run.id")
	attrTurn          = attribute.Key("gage.turn")
	attrCacheRead     = attribute.Key("gage.usage.cache_read_tokens")
	attrCacheWrite    = attribute.Key("gage.usage.cache_write_tokens")
	attrReasoning     = attribute.Key("gage.usage.reasoning_tokens")
	attrErrorMessage  = attribute.Key("error.message")
	retryEventName    = "gage.turn.retry"
	pausedEventName   = "gage.run.paused"
	opInvokeAgent     = "invoke_agent"
	opChat            = "chat"
	opExecuteTool     = "execute_tool"
)

// Option configures NewObserver.
type Option func(*observer)

// WithTracerProvider uses tp instead of the global otel.GetTracerProvider().
func WithTracerProvider(tp trace.TracerProvider) Option {
	return func(o *observer) { o.provider = tp }
}

// NewObserver returns an Observer that records agent lifecycle observations
// as OpenTelemetry spans. It is safe for concurrent use (the agent executes
// tools concurrently).
func NewObserver(opts ...Option) agent.Observer {
	o := &observer{runs: make(map[string]*runState)}
	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}
	if o.provider == nil {
		o.provider = otel.GetTracerProvider()
	}
	o.tracer = o.provider.Tracer(tracerName)
	return o
}

type observer struct {
	provider trace.TracerProvider
	tracer   trace.Tracer

	mu   sync.Mutex
	runs map[string]*runState
}

// runState tracks the open spans of one agent run so begin/end observations
// can be correlated. Guarded by observer.mu.
type runState struct {
	// ctx carries the run span for parenting child spans. It is a fresh
	// background-derived context, never the (cancellable) request context.
	ctx   context.Context
	span  trace.Span            // nil when the run_start was never seen
	turns map[int]trace.Span    // open turn spans by turn index
	tools map[string]trace.Span // open tool spans by tool-call id
}

func newRunState() *runState {
	return &runState{
		ctx:   context.Background(),
		turns: make(map[int]trace.Span),
		tools: make(map[string]trace.Span),
	}
}

// Observe implements agent.Observer. It never panics and ignores observation
// types it does not know.
func (o *observer) Observe(ctx context.Context, obs agent.Observation) {
	defer func() { _ = recover() }() // fire-and-forget: never take the agent down

	switch obs.Type {
	case agent.ObservationRunStart:
		o.runStart(ctx, obs)
	case agent.ObservationRunEnd:
		o.runEnd(obs)
	case agent.ObservationTurnStart:
		o.turnStart(obs)
	case agent.ObservationTurnEnd:
		o.turnEnd(obs)
	case agent.ObservationTurnRetry:
		o.turnRetry(obs)
	case agent.ObservationToolStart:
		o.toolStart(obs)
	case agent.ObservationToolEnd:
		o.toolEnd(obs)
	case agent.ObservationRunPaused:
		o.runPaused(obs)
	}
}

func startOpts(obs agent.Observation, kind trace.SpanKind, attrs ...attribute.KeyValue) []trace.SpanStartOption {
	opts := []trace.SpanStartOption{
		trace.WithSpanKind(kind),
		trace.WithAttributes(attrs...),
	}
	if !obs.StartedAt.IsZero() {
		opts = append(opts, trace.WithTimestamp(obs.StartedAt))
	}
	return opts
}

// endSpan applies error status and ends span at StartedAt+Duration when the
// observation carries timing, or "now" otherwise.
func endSpan(span trace.Span, obs agent.Observation) {
	if obs.IsError || obs.ErrorString != "" {
		span.SetStatus(codes.Error, obs.ErrorString)
		if obs.ErrorString != "" {
			span.SetAttributes(attrErrorMessage.String(obs.ErrorString))
		}
	}
	var opts []trace.SpanEndOption
	if !obs.StartedAt.IsZero() && obs.Duration > 0 {
		opts = append(opts, trace.WithTimestamp(obs.StartedAt.Add(obs.Duration)))
	}
	span.End(opts...)
}

func runAttrs(obs agent.Observation) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attrOperationName.String(opInvokeAgent),
		attrRunID.String(obs.RunID),
	}
	if obs.Agent != "" {
		attrs = append(attrs, attrAgentName.String(obs.Agent))
	}
	if obs.Provider != "" {
		attrs = append(attrs, attrProviderName.String(obs.Provider))
	}
	return attrs
}

func (o *observer) runStart(ctx context.Context, obs agent.Observation) {
	name := opInvokeAgent
	if obs.Agent != "" {
		name += " " + obs.Agent
	}
	// Parent under whatever span the caller's context carries, but store a
	// background-derived context so run cancellation values are not retained.
	sctx := trace.ContextWithSpanContext(context.Background(), trace.SpanContextFromContext(ctx))
	_, span := o.tracer.Start(sctx, name, startOpts(obs, trace.SpanKindInternal, runAttrs(obs)...)...)

	st := newRunState()
	st.span = span
	st.ctx = trace.ContextWithSpan(context.Background(), span)

	o.mu.Lock()
	if prev, ok := o.runs[obs.RunID]; ok && prev.span != nil {
		prev.span.End() // defensive: duplicate run id
	}
	o.runs[obs.RunID] = st
	o.mu.Unlock()
}

func (o *observer) runEnd(obs agent.Observation) {
	o.mu.Lock()
	st := o.runs[obs.RunID]
	delete(o.runs, obs.RunID)
	o.mu.Unlock()

	end := time.Now()
	if !obs.StartedAt.IsZero() && obs.Duration > 0 {
		end = obs.StartedAt.Add(obs.Duration)
	}
	if st == nil {
		st = newRunState()
	}
	// Close anything the run left open (e.g. a turn that failed mid-stream).
	for turn, span := range st.turns {
		delete(st.turns, turn)
		span.End(trace.WithTimestamp(end))
	}
	for id, span := range st.tools {
		delete(st.tools, id)
		span.End(trace.WithTimestamp(end))
	}
	span := st.span
	if span == nil {
		// run_end without run_start: synthesize the root span.
		name := opInvokeAgent
		if obs.Agent != "" {
			name += " " + obs.Agent
		}
		_, span = o.tracer.Start(context.Background(), name,
			startOpts(obs, trace.SpanKindInternal, runAttrs(obs)...)...)
	}
	endSpan(span, obs)
}

func turnAttrs(obs agent.Observation) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attrOperationName.String(opChat),
		attrRunID.String(obs.RunID),
		attrTurn.Int(obs.Turn),
	}
	if obs.Provider != "" {
		attrs = append(attrs, attrProviderName.String(obs.Provider))
	}
	return attrs
}

func (o *observer) startTurnSpan(st *runState, obs agent.Observation) trace.Span {
	name := opChat
	if obs.Provider != "" {
		name += " " + obs.Provider
	}
	_, span := o.tracer.Start(st.ctx, name, startOpts(obs, trace.SpanKindClient, turnAttrs(obs)...)...)
	return span
}

func (o *observer) turnStart(obs agent.Observation) {
	o.mu.Lock()
	defer o.mu.Unlock()
	st := o.run(obs.RunID)
	st.turns[obs.Turn] = o.startTurnSpan(st, obs)
}

func (o *observer) turnEnd(obs agent.Observation) {
	o.mu.Lock()
	st := o.run(obs.RunID)
	span, ok := st.turns[obs.Turn]
	delete(st.turns, obs.Turn)
	if !ok {
		span = o.startTurnSpan(st, obs) // end without begin
	}
	o.mu.Unlock()

	span.SetAttributes(
		attrUsageInput.Int(obs.Usage.InputTokens),
		attrUsageOutput.Int(obs.Usage.OutputTokens),
	)
	if obs.Usage.CacheReadTokens > 0 {
		span.SetAttributes(attrCacheRead.Int(obs.Usage.CacheReadTokens))
	}
	if obs.Usage.CacheWriteTokens > 0 {
		span.SetAttributes(attrCacheWrite.Int(obs.Usage.CacheWriteTokens))
	}
	if obs.Usage.ReasoningTokens > 0 {
		span.SetAttributes(attrReasoning.Int(obs.Usage.ReasoningTokens))
	}
	endSpan(span, obs)
}

func (o *observer) turnRetry(obs agent.Observation) {
	o.mu.Lock()
	st := o.run(obs.RunID)
	span, ok := st.turns[obs.Turn]
	if !ok {
		span = st.span
	}
	o.mu.Unlock()
	if span == nil {
		return
	}
	opts := []trace.EventOption{trace.WithAttributes(
		attrTurn.Int(obs.Turn),
		attrErrorMessage.String(obs.ErrorString),
	)}
	if !obs.StartedAt.IsZero() {
		opts = append(opts, trace.WithTimestamp(obs.StartedAt))
	}
	span.AddEvent(retryEventName, opts...)
}

func toolAttrs(obs agent.Observation) []attribute.KeyValue {
	return []attribute.KeyValue{
		attrOperationName.String(opExecuteTool),
		attrToolName.String(obs.Tool),
		attrToolCallID.String(obs.CallID),
		attrRunID.String(obs.RunID),
		attrTurn.Int(obs.Turn),
	}
}

func (o *observer) startToolSpan(st *runState, obs agent.Observation) trace.Span {
	name := opExecuteTool
	if obs.Tool != "" {
		name += " " + obs.Tool
	}
	_, span := o.tracer.Start(st.ctx, name, startOpts(obs, trace.SpanKindInternal, toolAttrs(obs)...)...)
	return span
}

func (o *observer) toolStart(obs agent.Observation) {
	o.mu.Lock()
	defer o.mu.Unlock()
	st := o.run(obs.RunID)
	st.tools[obs.CallID] = o.startToolSpan(st, obs)
}

func (o *observer) toolEnd(obs agent.Observation) {
	o.mu.Lock()
	st := o.run(obs.RunID)
	span, ok := st.tools[obs.CallID]
	delete(st.tools, obs.CallID)
	if !ok {
		span = o.startToolSpan(st, obs) // end without begin
	}
	o.mu.Unlock()
	endSpan(span, obs)
}

func (o *observer) runPaused(obs agent.Observation) {
	o.mu.Lock()
	st := o.run(obs.RunID)
	span := st.span
	o.mu.Unlock()
	if span == nil {
		return
	}
	span.AddEvent(pausedEventName, trace.WithAttributes(attrTurn.Int(obs.Turn)))
}

// run returns the state for id, creating a placeholder (with no run span)
// when the run_start observation was never seen. Callers must hold o.mu.
func (o *observer) run(id string) *runState {
	st, ok := o.runs[id]
	if !ok {
		st = newRunState()
		o.runs[id] = st
	}
	return st
}
