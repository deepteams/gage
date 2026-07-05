package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deepteams/gage"
	"github.com/deepteams/gage/tools"
)

func TestTokenBudgetStopsBeforeTools(t *testing.T) {
	reg := tools.NewRegistry()
	var executed bool
	reg.MustRegister(tools.ToolFuncMust("echo", "e", func(ctx context.Context, input json.RawMessage) (gage.ToolResult, error) {
		executed = true
		return gage.TextResult("", "ok"), nil
	}))
	mp := &mockProvider{turns: [][]gage.Event{
		{gage.MessageStart(), toolCallDone("c1", "echo", `{}`), gage.UsageEvent(gage.Usage{InputTokens: 90, OutputTokens: 20}), gage.MessageDone(gage.StopToolUse)},
	}}
	ag, _ := New(Config{Provider: mp, Registry: reg, TokenBudget: 100})
	_, err := ag.RunSync(context.Background(), []gage.Message{gage.UserText("go")})
	if !errors.Is(err, gage.ErrBudgetExceeded) {
		t.Fatalf("err = %v, want ErrBudgetExceeded", err)
	}
	if executed {
		t.Fatal("tool executed past budget")
	}
}

func TestTokenBudgetDoesNotTruncateFinalAnswer(t *testing.T) {
	// A final answer that exceeds the budget is still delivered: the budget
	// gates further tool turns, not the answer already paid for.
	mp := &mockProvider{turns: [][]gage.Event{
		{gage.MessageStart(), gage.TextDelta("answer"), gage.UsageEvent(gage.Usage{InputTokens: 500, OutputTokens: 50}), gage.MessageDone(gage.StopEndTurn)},
	}}
	ag, _ := New(Config{Provider: mp, TokenBudget: 100})
	res, err := ag.RunSync(context.Background(), []gage.Message{gage.UserText("go")})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "answer" {
		t.Fatalf("text = %q", res.Text)
	}
}

func TestToolRepeatGuard(t *testing.T) {
	reg := tools.NewRegistry()
	executions := 0
	reg.MustRegister(tools.ToolFuncMust("loop", "l", func(ctx context.Context, input json.RawMessage) (gage.ToolResult, error) {
		executions++
		return gage.TextResult("", "again"), nil
	}))
	// The model repeats the exact same call forever.
	same := []gage.Event{gage.MessageStart(), toolCallDone("c", "loop", `{"q":"x"}`), gage.MessageDone(gage.StopToolUse)}
	mp := &mockProvider{turns: [][]gage.Event{same, same, same, same, same, same}}
	ag, _ := New(Config{Provider: mp, Registry: reg, MaxToolRepeats: 2})
	ch, _ := ag.Run(context.Background(), []gage.Message{gage.UserText("go")})

	var warned bool
	var terminal error
	for e := range ch {
		switch e.Type {
		case gage.EventToolResult:
			if e.ToolResult.IsError && strings.Contains(e.ToolResult.Text(), "loop detected") {
				warned = true
			}
		case gage.EventError:
			terminal = e.Err
		}
	}
	if executions != 2 {
		t.Fatalf("tool executed %d times, want 2 (threshold)", executions)
	}
	if !warned {
		t.Fatal("model never received the loop warning result")
	}
	if !errors.Is(terminal, gage.ErrLoopDetected) {
		t.Fatalf("terminal = %v, want ErrLoopDetected", terminal)
	}
}

func TestToolRepeatGuardResetsOnDifferentInput(t *testing.T) {
	reg := tools.NewRegistry()
	executions := 0
	reg.MustRegister(tools.ToolFuncMust("step", "s", func(ctx context.Context, input json.RawMessage) (gage.ToolResult, error) {
		executions++
		return gage.TextResult("", "ok"), nil
	}))
	mp := &mockProvider{turns: [][]gage.Event{
		{gage.MessageStart(), toolCallDone("c1", "step", `{"n":1}`), gage.MessageDone(gage.StopToolUse)},
		{gage.MessageStart(), toolCallDone("c2", "step", `{"n":2}`), gage.MessageDone(gage.StopToolUse)},
		{gage.MessageStart(), toolCallDone("c3", "step", `{"n":3}`), gage.MessageDone(gage.StopToolUse)},
		{gage.MessageStart(), gage.TextDelta("done"), gage.MessageDone(gage.StopEndTurn)},
	}}
	ag, _ := New(Config{Provider: mp, Registry: reg, MaxToolRepeats: 1})
	res, err := ag.RunSync(context.Background(), []gage.Message{gage.UserText("go")})
	if err != nil {
		t.Fatal(err)
	}
	if executions != 3 || res.Text != "done" {
		t.Fatalf("executions = %d text = %q", executions, res.Text)
	}
}

// flakyProvider fails its first n Stream calls, then delegates to inner.
type flakyProvider struct {
	mu       sync.Mutex
	failures int
	failWith error
	// midStream, when true, fails by emitting an error event after partial
	// content instead of failing the Stream call itself.
	midStream bool
	inner     *mockProvider
	attempts  int
}

func (f *flakyProvider) Name() string { return "flaky" }

func (f *flakyProvider) Stream(ctx context.Context, req gage.Request) (<-chan gage.Event, error) {
	f.mu.Lock()
	f.attempts++
	failing := f.attempts <= f.failures
	f.mu.Unlock()
	if !failing {
		return f.inner.Stream(ctx, req)
	}
	if !f.midStream {
		return nil, f.failWith
	}
	ch := make(chan gage.Event)
	go func() {
		defer close(ch)
		for _, e := range []gage.Event{gage.MessageStart(), gage.TextDelta("partial"), gage.ErrorEvent(f.failWith)} {
			select {
			case ch <- e:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

func TestStreamRetryOnInitialFailure(t *testing.T) {
	fp := &flakyProvider{
		failures: 2,
		failWith: &gage.APIError{Provider: "flaky", Status: 500, Body: "boom"},
		inner: &mockProvider{turns: [][]gage.Event{
			{gage.MessageStart(), gage.TextDelta("ok"), gage.MessageDone(gage.StopEndTurn)},
		}},
	}
	ag, _ := New(Config{Provider: fp, MaxStreamRetries: 2, RetryBaseDelay: time.Millisecond})
	res, err := ag.RunSync(context.Background(), []gage.Message{gage.UserText("go")})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "ok" {
		t.Fatalf("text = %q", res.Text)
	}
	if fp.attempts != 3 {
		t.Fatalf("attempts = %d, want 3", fp.attempts)
	}
}

func TestStreamRetryMidStreamSuppressesErrorEvent(t *testing.T) {
	fp := &flakyProvider{
		failures:  1,
		failWith:  &gage.APIError{Provider: "flaky", Status: 529, Body: "overloaded"},
		midStream: true,
		inner: &mockProvider{turns: [][]gage.Event{
			{gage.MessageStart(), gage.TextDelta("ok"), gage.MessageDone(gage.StopEndTurn)},
		}},
	}
	ag, _ := New(Config{Provider: fp, MaxStreamRetries: 1, RetryBaseDelay: time.Millisecond})
	ch, _ := ag.Run(context.Background(), []gage.Message{gage.UserText("go")})
	var sawError, sawDone bool
	starts := 0
	for e := range ch {
		switch e.Type {
		case gage.EventMessageStart:
			starts++
		case gage.EventError:
			sawError = true
		case gage.EventDone:
			sawDone = true
		}
	}
	if sawError {
		t.Fatal("retryable mid-stream error leaked to the consumer")
	}
	if !sawDone {
		t.Fatal("run did not complete after retry")
	}
	// The consumer sees the turn restart via a second message_start.
	if starts != 2 {
		t.Fatalf("message_start count = %d, want 2", starts)
	}
}

func TestStreamRetrySkipsNonRetryable(t *testing.T) {
	fp := &flakyProvider{
		failures: 5,
		failWith: &gage.APIError{Provider: "flaky", Status: 401, Body: "bad key"},
		inner:    &mockProvider{},
	}
	ag, _ := New(Config{Provider: fp, MaxStreamRetries: 3, RetryBaseDelay: time.Millisecond})
	_, err := ag.RunSync(context.Background(), []gage.Message{gage.UserText("go")})
	if !errors.Is(err, gage.ErrAuth) {
		t.Fatalf("err = %v, want ErrAuth", err)
	}
	if fp.attempts != 1 {
		t.Fatalf("attempts = %d, want 1 (auth errors must not be retried)", fp.attempts)
	}
}

func TestProactiveCompactionRunsBeforeFirstCall(t *testing.T) {
	mp := &mockProvider{turns: [][]gage.Event{
		{gage.MessageStart(), gage.TextDelta("done"), gage.MessageDone(gage.StopEndTurn)},
	}}
	comp := gage.CompactorFunc(func(ctx context.Context, msgs []gage.Message, u gage.Usage) ([]gage.Message, gage.Usage, error) {
		return []gage.Message{gage.UserText("[compacted]")}, gage.Usage{}, nil
	})
	big := strings.Repeat("x", 4000) // ~1000 estimated tokens
	ag, _ := New(Config{Provider: mp, Compactor: comp, CompactAfter: 500})
	if _, err := ag.RunSync(context.Background(), []gage.Message{gage.UserText(big)}); err != nil {
		t.Fatal(err)
	}
	mp.mu.Lock()
	msgs := mp.lastReq.Messages
	mp.mu.Unlock()
	if len(msgs) != 1 || msgs[0].Text() != "[compacted]" {
		t.Fatalf("provider saw uncompacted conversation: %+v", msgs)
	}
}

func TestCompactorUsageCountsTowardBudget(t *testing.T) {
	mp := &mockProvider{turns: [][]gage.Event{
		{gage.MessageStart(), gage.TextDelta("done"), gage.MessageDone(gage.StopEndTurn)},
	}}
	comp := gage.CompactorFunc(func(ctx context.Context, msgs []gage.Message, u gage.Usage) ([]gage.Message, gage.Usage, error) {
		return []gage.Message{gage.UserText("[compacted]")}, gage.Usage{InputTokens: 100, OutputTokens: 20}, nil
	})
	big := strings.Repeat("x", 4000)
	ag, _ := New(Config{Provider: mp, Compactor: comp, CompactAfter: 500, TokenBudget: 50})
	_, err := ag.RunSync(context.Background(), []gage.Message{gage.UserText(big)})
	if !errors.Is(err, gage.ErrBudgetExceeded) {
		t.Fatalf("err = %v, want ErrBudgetExceeded", err)
	}
	mp.mu.Lock()
	calls := mp.call
	mp.mu.Unlock()
	if calls != 0 {
		t.Fatalf("provider called %d times after budget blew on compaction", calls)
	}
}

func TestSummarizeReportsUsage(t *testing.T) {
	summarizer := &mockProvider{turns: [][]gage.Event{
		{gage.MessageStart(), gage.TextDelta("the summary"), gage.UsageEvent(gage.Usage{InputTokens: 5, OutputTokens: 7}), gage.MessageDone(gage.StopEndTurn)},
	}}
	msgs := []gage.Message{
		gage.UserText("task"),
		gage.AssistantText("a1"),
		gage.AssistantText("a2"),
		gage.AssistantText("a3"),
		gage.AssistantText("a4"),
	}
	out, used, err := Summarize(summarizer, "", 2).Compact(context.Background(), msgs, gage.Usage{})
	if err != nil {
		t.Fatal(err)
	}
	if used.InputTokens != 5 || used.OutputTokens != 7 {
		t.Fatalf("usage = %+v", used)
	}
	if len(out) != 4 || !strings.Contains(out[1].Text(), "the summary") {
		t.Fatalf("out = %+v", out)
	}
}
