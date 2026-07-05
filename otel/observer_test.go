package otelgage

import (
	"context"
	"testing"
	"time"

	"github.com/deepteams/gage"
	"github.com/deepteams/gage/agent"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func newTestObserver(t *testing.T) (agent.Observer, *tracetest.SpanRecorder) {
	t.Helper()
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	return NewObserver(WithTracerProvider(tp)), sr
}

func spanByName(t *testing.T, spans []sdktrace.ReadOnlySpan, name string) sdktrace.ReadOnlySpan {
	t.Helper()
	for _, s := range spans {
		if s.Name() == name {
			return s
		}
	}
	t.Fatalf("no span named %q; got %v", name, spanNames(spans))
	return nil
}

func spanNames(spans []sdktrace.ReadOnlySpan) []string {
	names := make([]string, len(spans))
	for i, s := range spans {
		names[i] = s.Name()
	}
	return names
}

func attrValue(t *testing.T, s sdktrace.ReadOnlySpan, key attribute.Key) attribute.Value {
	t.Helper()
	for _, kv := range s.Attributes() {
		if kv.Key == key {
			return kv.Value
		}
	}
	t.Fatalf("span %q has no attribute %q; attrs: %v", s.Name(), key, s.Attributes())
	return attribute.Value{}
}

func hasAttr(s sdktrace.ReadOnlySpan, key attribute.Key) bool {
	for _, kv := range s.Attributes() {
		if kv.Key == key {
			return true
		}
	}
	return false
}

func TestFullRunSpanHierarchy(t *testing.T) {
	obs, sr := newTestObserver(t)
	ctx := context.Background()
	t0 := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)

	base := agent.Observation{RunID: "r1", Agent: "billing", Provider: "anthropic"}

	runStart := base
	runStart.Type = agent.ObservationRunStart
	runStart.StartedAt = t0
	obs.Observe(ctx, runStart)

	turnStart := base
	turnStart.Type = agent.ObservationTurnStart
	turnStart.Turn = 0
	turnStart.StartedAt = t0
	obs.Observe(ctx, turnStart)

	retry := base
	retry.Type = agent.ObservationTurnRetry
	retry.Turn = 0
	retry.StartedAt = t0.Add(500 * time.Millisecond)
	retry.ErrorString = "rate limited"
	obs.Observe(ctx, retry)

	turnEnd := base
	turnEnd.Type = agent.ObservationTurnEnd
	turnEnd.Turn = 0
	turnEnd.StartedAt = t0
	turnEnd.Duration = 2 * time.Second
	turnEnd.Usage = gage.Usage{
		InputTokens:      100,
		OutputTokens:     20,
		CacheReadTokens:  5,
		CacheWriteTokens: 7,
		ReasoningTokens:  3,
	}
	obs.Observe(ctx, turnEnd)

	toolStart := base
	toolStart.Type = agent.ObservationToolStart
	toolStart.Turn = 0
	toolStart.Tool = "web_fetch"
	toolStart.CallID = "call_1"
	toolStart.StartedAt = t0.Add(2 * time.Second)
	obs.Observe(ctx, toolStart)

	toolEnd := base
	toolEnd.Type = agent.ObservationToolEnd
	toolEnd.Turn = 0
	toolEnd.Tool = "web_fetch"
	toolEnd.CallID = "call_1"
	toolEnd.StartedAt = t0.Add(2 * time.Second)
	toolEnd.Duration = time.Second
	toolEnd.IsError = true
	toolEnd.ErrorString = "boom"
	obs.Observe(ctx, toolEnd)

	runEnd := base
	runEnd.Type = agent.ObservationRunEnd
	runEnd.StartedAt = t0
	runEnd.Duration = 5 * time.Second
	obs.Observe(ctx, runEnd)

	spans := sr.Ended()
	if len(spans) != 3 {
		t.Fatalf("want 3 ended spans, got %d: %v", len(spans), spanNames(spans))
	}

	run := spanByName(t, spans, "invoke_agent billing")
	turn := spanByName(t, spans, "chat anthropic")
	tool := spanByName(t, spans, "execute_tool web_fetch")

	// Hierarchy: turn and tool spans are children of the run span, one trace.
	if turn.Parent().SpanID() != run.SpanContext().SpanID() {
		t.Error("turn span is not a child of the run span")
	}
	if tool.Parent().SpanID() != run.SpanContext().SpanID() {
		t.Error("tool span is not a child of the run span")
	}
	for _, s := range []sdktrace.ReadOnlySpan{turn, tool} {
		if s.SpanContext().TraceID() != run.SpanContext().TraceID() {
			t.Errorf("span %q not in the run's trace", s.Name())
		}
	}

	// Run span attributes and timing.
	if got := attrValue(t, run, "gen_ai.operation.name").AsString(); got != "invoke_agent" {
		t.Errorf("run operation = %q", got)
	}
	if got := attrValue(t, run, "gen_ai.agent.name").AsString(); got != "billing" {
		t.Errorf("agent name = %q", got)
	}
	if got := attrValue(t, run, "gage.run.id").AsString(); got != "r1" {
		t.Errorf("run id = %q", got)
	}
	if run.Status().Code == codes.Error {
		t.Error("successful run must not carry error status")
	}
	if d := run.EndTime().Sub(run.StartTime()); d != 5*time.Second {
		t.Errorf("run span duration = %v, want 5s", d)
	}

	// Turn span: chat operation, provider, usage, retry event.
	if got := attrValue(t, turn, "gen_ai.operation.name").AsString(); got != "chat" {
		t.Errorf("turn operation = %q", got)
	}
	if got := attrValue(t, turn, "gen_ai.provider.name").AsString(); got != "anthropic" {
		t.Errorf("turn provider = %q", got)
	}
	if got := attrValue(t, turn, "gen_ai.usage.input_tokens").AsInt64(); got != 100 {
		t.Errorf("input tokens = %d", got)
	}
	if got := attrValue(t, turn, "gen_ai.usage.output_tokens").AsInt64(); got != 20 {
		t.Errorf("output tokens = %d", got)
	}
	if got := attrValue(t, turn, "gage.usage.cache_read_tokens").AsInt64(); got != 5 {
		t.Errorf("cache read tokens = %d", got)
	}
	if got := attrValue(t, turn, "gage.usage.cache_write_tokens").AsInt64(); got != 7 {
		t.Errorf("cache write tokens = %d", got)
	}
	if got := attrValue(t, turn, "gage.usage.reasoning_tokens").AsInt64(); got != 3 {
		t.Errorf("reasoning tokens = %d", got)
	}
	if d := turn.EndTime().Sub(turn.StartTime()); d != 2*time.Second {
		t.Errorf("turn span duration = %v, want 2s", d)
	}
	var sawRetry bool
	for _, ev := range turn.Events() {
		if ev.Name == "gage.turn.retry" {
			sawRetry = true
		}
	}
	if !sawRetry {
		t.Error("turn span is missing the gage.turn.retry event")
	}

	// Tool span: name/id attributes and error status.
	if got := attrValue(t, tool, "gen_ai.operation.name").AsString(); got != "execute_tool" {
		t.Errorf("tool operation = %q", got)
	}
	if got := attrValue(t, tool, "gen_ai.tool.name").AsString(); got != "web_fetch" {
		t.Errorf("tool name = %q", got)
	}
	if got := attrValue(t, tool, "gen_ai.tool.call.id").AsString(); got != "call_1" {
		t.Errorf("tool call id = %q", got)
	}
	if tool.Status().Code != codes.Error {
		t.Error("failed tool span must carry error status")
	}
	if tool.Status().Description != "boom" {
		t.Errorf("tool error description = %q", tool.Status().Description)
	}
	if d := tool.EndTime().Sub(tool.StartTime()); d != time.Second {
		t.Errorf("tool span duration = %v, want 1s", d)
	}
}

func TestRunEndErrorStatus(t *testing.T) {
	obs, sr := newTestObserver(t)
	ctx := context.Background()

	obs.Observe(ctx, agent.Observation{Type: agent.ObservationRunStart, RunID: "r2"})
	obs.Observe(ctx, agent.Observation{
		Type:        agent.ObservationRunEnd,
		RunID:       "r2",
		IsError:     true,
		ErrorString: "budget exceeded",
	})

	spans := sr.Ended()
	if len(spans) != 1 {
		t.Fatalf("want 1 span, got %d", len(spans))
	}
	run := spans[0]
	if run.Status().Code != codes.Error {
		t.Error("failed run must carry error status")
	}
	if run.Status().Description != "budget exceeded" {
		t.Errorf("status description = %q", run.Status().Description)
	}
	if got := attrValue(t, run, "error.message").AsString(); got != "budget exceeded" {
		t.Errorf("error.message = %q", got)
	}
}

func TestEndWithoutBegin(t *testing.T) {
	obs, sr := newTestObserver(t)
	ctx := context.Background()
	t0 := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)

	// No run_start, no tool_start, no turn_start — ends must still record.
	obs.Observe(ctx, agent.Observation{
		Type: agent.ObservationToolEnd, RunID: "ghost", Tool: "grep",
		CallID: "c9", StartedAt: t0, Duration: time.Second,
		IsError: true, ErrorString: "not found",
	})
	obs.Observe(ctx, agent.Observation{
		Type: agent.ObservationTurnEnd, RunID: "ghost", Turn: 3,
		StartedAt: t0, Duration: time.Second,
		Usage: gage.Usage{InputTokens: 42},
	})
	obs.Observe(ctx, agent.Observation{
		Type: agent.ObservationRunEnd, RunID: "ghost", Agent: "ghostly",
		StartedAt: t0, Duration: 2 * time.Second,
	})

	spans := sr.Ended()
	if len(spans) != 3 {
		t.Fatalf("want 3 synthesized spans, got %d: %v", len(spans), spanNames(spans))
	}
	tool := spanByName(t, spans, "execute_tool grep")
	if tool.Status().Code != codes.Error {
		t.Error("synthesized tool span must carry error status")
	}
	turn := spanByName(t, spans, "chat")
	if got := attrValue(t, turn, "gen_ai.usage.input_tokens").AsInt64(); got != 42 {
		t.Errorf("synthesized turn input tokens = %d", got)
	}
	if got := attrValue(t, turn, "gage.turn").AsInt64(); got != 3 {
		t.Errorf("synthesized turn index = %d", got)
	}
	spanByName(t, spans, "invoke_agent ghostly") // run span synthesized too
}

func TestRunEndClosesDanglingSpans(t *testing.T) {
	obs, sr := newTestObserver(t)
	ctx := context.Background()

	obs.Observe(ctx, agent.Observation{Type: agent.ObservationRunStart, RunID: "r3", Provider: "openai"})
	obs.Observe(ctx, agent.Observation{Type: agent.ObservationTurnStart, RunID: "r3", Turn: 0, Provider: "openai"})
	obs.Observe(ctx, agent.Observation{Type: agent.ObservationToolStart, RunID: "r3", Tool: "bash", CallID: "c1"})
	// The run dies mid-turn: no turn_end, no tool_end.
	obs.Observe(ctx, agent.Observation{
		Type: agent.ObservationRunEnd, RunID: "r3",
		IsError: true, ErrorString: "stream failed",
	})

	spans := sr.Ended()
	if len(spans) != 3 {
		t.Fatalf("want 3 ended spans (dangling closed), got %d: %v", len(spans), spanNames(spans))
	}
	spanByName(t, spans, "chat openai")
	spanByName(t, spans, "execute_tool bash")
}

func TestRunPausedEvent(t *testing.T) {
	obs, sr := newTestObserver(t)
	ctx := context.Background()

	obs.Observe(ctx, agent.Observation{Type: agent.ObservationRunStart, RunID: "r4"})
	obs.Observe(ctx, agent.Observation{Type: agent.ObservationRunPaused, RunID: "r4", Turn: 2})
	obs.Observe(ctx, agent.Observation{Type: agent.ObservationRunEnd, RunID: "r4"})

	spans := sr.Ended()
	if len(spans) != 1 {
		t.Fatalf("want 1 span, got %d", len(spans))
	}
	var sawPaused bool
	for _, ev := range spans[0].Events() {
		if ev.Name == "gage.run.paused" {
			sawPaused = true
		}
	}
	if !sawPaused {
		t.Error("run span is missing the gage.run.paused event")
	}
}

func TestRetryWithoutTurnFallsBackToRunSpan(t *testing.T) {
	obs, sr := newTestObserver(t)
	ctx := context.Background()

	obs.Observe(ctx, agent.Observation{Type: agent.ObservationRunStart, RunID: "r5"})
	obs.Observe(ctx, agent.Observation{
		Type: agent.ObservationTurnRetry, RunID: "r5", Turn: 1,
		ErrorString: "overloaded",
	})
	obs.Observe(ctx, agent.Observation{Type: agent.ObservationRunEnd, RunID: "r5"})

	spans := sr.Ended()
	if len(spans) != 1 {
		t.Fatalf("want 1 span, got %d", len(spans))
	}
	var sawRetry bool
	for _, ev := range spans[0].Events() {
		if ev.Name == "gage.turn.retry" {
			sawRetry = true
		}
	}
	if !sawRetry {
		t.Error("retry without an open turn must land on the run span")
	}
}

func TestConcurrentToolObservations(t *testing.T) {
	obs, sr := newTestObserver(t)
	ctx := context.Background()

	obs.Observe(ctx, agent.Observation{Type: agent.ObservationRunStart, RunID: "r6"})

	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func(i int) {
			defer func() { done <- struct{}{} }()
			id := string(rune('a' + i))
			obs.Observe(ctx, agent.Observation{
				Type: agent.ObservationToolStart, RunID: "r6",
				Tool: "t", CallID: id,
			})
			obs.Observe(ctx, agent.Observation{
				Type: agent.ObservationToolEnd, RunID: "r6",
				Tool: "t", CallID: id,
			})
		}(i)
	}
	for i := 0; i < 8; i++ {
		<-done
	}
	obs.Observe(ctx, agent.Observation{Type: agent.ObservationRunEnd, RunID: "r6"})

	spans := sr.Ended()
	if len(spans) != 9 { // 8 tool spans + run span
		t.Fatalf("want 9 spans, got %d", len(spans))
	}
}

func TestUnknownObservationTypeIgnored(t *testing.T) {
	obs, sr := newTestObserver(t)
	obs.Observe(context.Background(), agent.Observation{Type: "made_up", RunID: "x"})
	if n := len(sr.Ended()); n != 0 {
		t.Fatalf("unknown observation produced %d spans", n)
	}
}

func TestNoTurnAttrLeakOnRunSpan(t *testing.T) {
	// Sanity: run span never carries tool attributes.
	obs, sr := newTestObserver(t)
	ctx := context.Background()
	obs.Observe(ctx, agent.Observation{Type: agent.ObservationRunStart, RunID: "r7"})
	obs.Observe(ctx, agent.Observation{Type: agent.ObservationRunEnd, RunID: "r7"})
	run := sr.Ended()[0]
	if hasAttr(run, "gen_ai.tool.name") || hasAttr(run, "gen_ai.usage.input_tokens") {
		t.Error("run span carries child-span attributes")
	}
}
