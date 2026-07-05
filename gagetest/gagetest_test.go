package gagetest_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deepteams/gage"
	"github.com/deepteams/gage/agent"
	"github.com/deepteams/gage/gagetest"
	"github.com/deepteams/gage/tools"
)

// drain collects every event until the stream closes, failing the test if it
// does not close within a generous timeout.
func drain(t *testing.T, ch <-chan gage.Event) []gage.Event {
	t.Helper()
	var out []gage.Event
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-timeout:
			t.Fatal("timed out draining stream")
		}
	}
}

func types(evs []gage.Event) []gage.EventType {
	out := make([]gage.EventType, len(evs))
	for i, ev := range evs {
		out[i] = ev.Type
	}
	return out
}

func joinText(evs []gage.Event, typ gage.EventType) string {
	var b strings.Builder
	for _, ev := range evs {
		if ev.Type == typ {
			b.WriteString(ev.Text)
		}
	}
	return b.String()
}

func TestNameDefault(t *testing.T) {
	if got := gagetest.NewProvider("").Name(); got != "gagetest" {
		t.Fatalf("Name() = %q, want %q", got, "gagetest")
	}
	if got := gagetest.NewProvider("scripted").Name(); got != "scripted" {
		t.Fatalf("Name() = %q, want %q", got, "scripted")
	}
}

func TestTextTurnEventSequence(t *testing.T) {
	p := gagetest.NewProvider("")
	p.Enqueue(gagetest.Text("hello world"))

	ch, err := p.Stream(context.Background(), gage.Request{})
	if err != nil {
		t.Fatal(err)
	}
	evs := drain(t, ch)

	if evs[0].Type != gage.EventMessageStart {
		t.Fatalf("first event = %v, want message_start", evs[0].Type)
	}
	var deltas int
	for _, ev := range evs[1 : len(evs)-2] {
		if ev.Type != gage.EventTextDelta {
			t.Fatalf("unexpected mid-stream event %v in %v", ev.Type, types(evs))
		}
		deltas++
	}
	if deltas < 2 {
		t.Fatalf("got %d text deltas, want >= 2 (accumulation must be exercised)", deltas)
	}
	if got := joinText(evs, gage.EventTextDelta); got != "hello world" {
		t.Fatalf("accumulated text = %q, want %q", got, "hello world")
	}
	usage := evs[len(evs)-2]
	if usage.Type != gage.EventUsage || usage.Usage == nil || usage.Usage.Total() == 0 {
		t.Fatalf("penultimate event = %+v, want non-zero usage", usage)
	}
	done := evs[len(evs)-1]
	if done.Type != gage.EventMessageDone || done.StopReason != gage.StopEndTurn {
		t.Fatalf("last event = %+v, want message_done/end_turn", done)
	}
}

func TestToolCallTurnEventSequence(t *testing.T) {
	p := gagetest.NewProvider("")
	p.Enqueue(gagetest.Calls(
		gagetest.Call("c1", "weather", map[string]any{"city": "Paris"}),
	).WithText("checking..."))

	ch, err := p.Stream(context.Background(), gage.Request{})
	if err != nil {
		t.Fatal(err)
	}
	evs := drain(t, ch)

	got := types(evs)
	want := []gage.EventType{
		gage.EventMessageStart,
		gage.EventTextDelta, gage.EventTextDelta,
		gage.EventToolCallStart, gage.EventToolCallDelta, gage.EventToolCallDone,
		gage.EventUsage,
		gage.EventMessageDone,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("event sequence = %v, want %v", got, want)
	}
	if txt := joinText(evs, gage.EventTextDelta); txt != "checking..." {
		t.Fatalf("text = %q, want %q", txt, "checking...")
	}

	start := evs[3].ToolCall
	if start == nil || start.ID != "c1" || start.Name != "weather" {
		t.Fatalf("tool_call_start = %+v, want id=c1 name=weather", start)
	}
	end := evs[5].ToolCall
	if end == nil {
		t.Fatal("tool_call_end has no ToolCall")
	}
	var args map[string]any
	if err := json.Unmarshal(end.Input, &args); err != nil {
		t.Fatalf("tool_call_end input %q: %v", end.Input, err)
	}
	if args["city"] != "Paris" {
		t.Fatalf("args = %v, want city=Paris", args)
	}
	if evs[7].StopReason != gage.StopToolUse {
		t.Fatalf("stop reason = %v, want tool_use (default with calls)", evs[7].StopReason)
	}
}

func TestReasoningTurn(t *testing.T) {
	p := gagetest.NewProvider("")
	p.Enqueue(gagetest.Text("ok").WithSignedReasoning("thinking hard", "sig-1"))

	ch, err := p.Stream(context.Background(), gage.Request{})
	if err != nil {
		t.Fatal(err)
	}
	evs := drain(t, ch)

	got := types(evs)
	want := []gage.EventType{
		gage.EventMessageStart,
		gage.EventReasoningDelta, gage.EventReasoningDelta, gage.EventReasoningDone,
		gage.EventTextDelta, gage.EventTextDelta,
		gage.EventUsage,
		gage.EventMessageDone,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("event sequence = %v, want %v", got, want)
	}
	if r := joinText(evs, gage.EventReasoningDelta); r != "thinking hard" {
		t.Fatalf("reasoning = %q", r)
	}
	if txt := joinText(evs, gage.EventTextDelta); txt != "ok" {
		t.Fatalf("text = %q, want ok", txt)
	}
	if evs[3].Signature != "sig-1" {
		t.Fatalf("reasoning_done signature = %q, want sig-1", evs[3].Signature)
	}
}

func TestDefaultsAndOverrides(t *testing.T) {
	p := gagetest.NewProvider("")
	p.Enqueue(
		gagetest.Text("a").WithUsage(gage.Usage{InputTokens: 100, OutputTokens: 50}).WithStop(gage.StopMaxTokens),
	)
	ch, err := p.Stream(context.Background(), gage.Request{})
	if err != nil {
		t.Fatal(err)
	}
	evs := drain(t, ch)
	usage := evs[len(evs)-2]
	if usage.Usage == nil || usage.Usage.InputTokens != 100 || usage.Usage.OutputTokens != 50 {
		t.Fatalf("usage = %+v, want 100/50", usage.Usage)
	}
	if evs[len(evs)-1].StopReason != gage.StopMaxTokens {
		t.Fatalf("stop = %v, want max_tokens", evs[len(evs)-1].StopReason)
	}
}

func TestErrorTurn(t *testing.T) {
	boom := errors.New("backend melted")
	p := gagetest.NewProvider("")
	p.Enqueue(gagetest.Text("fine"))
	p.EnqueueError(boom)

	// First turn streams normally.
	ch, err := p.Stream(context.Background(), gage.Request{})
	if err != nil {
		t.Fatal(err)
	}
	drain(t, ch)

	// Second turn emits the error event then closes.
	ch, err = p.Stream(context.Background(), gage.Request{})
	if err != nil {
		t.Fatal(err)
	}
	evs := drain(t, ch)
	if len(evs) != 1 || evs[0].Type != gage.EventError {
		t.Fatalf("events = %v, want exactly one error event", types(evs))
	}
	if !errors.Is(evs[0].Err, boom) || evs[0].ErrorString != boom.Error() {
		t.Fatalf("error event = %+v, want Err=%v", evs[0], boom)
	}
}

func TestStreamError(t *testing.T) {
	dial := errors.New("dial refused")
	p := gagetest.NewProvider("")
	p.EnqueueStreamError(dial)

	ch, err := p.Stream(context.Background(), gage.Request{System: "sys"})
	if !errors.Is(err, dial) {
		t.Fatalf("Stream err = %v, want %v", err, dial)
	}
	if ch != nil {
		t.Fatal("Stream returned a channel alongside an error")
	}
	// The request is still recorded for assertions.
	reqs := p.Requests()
	if len(reqs) != 1 || reqs[0].System != "sys" {
		t.Fatalf("requests = %+v, want the failing request recorded", reqs)
	}
}

func TestScriptExhausted(t *testing.T) {
	p := gagetest.NewProvider("")
	_, err := p.Stream(context.Background(), gage.Request{})
	if !errors.Is(err, gagetest.ErrScriptExhausted) {
		t.Fatalf("err = %v, want ErrScriptExhausted", err)
	}
	if !strings.Contains(err.Error(), "nothing enqueued") {
		t.Fatalf("err = %q, want a hint that nothing was enqueued", err)
	}
}

func TestContextCancellationMidStream(t *testing.T) {
	p := gagetest.NewProvider("")
	p.Enqueue(gagetest.Text("a fairly long response so several sends remain"))

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := p.Stream(ctx, gage.Request{})
	if err != nil {
		t.Fatal(err)
	}
	// Receive the first event, then cancel while the goroutine still has
	// sends pending; the channel must close promptly.
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("no first event")
	}
	cancel()
	drain(t, ch) // fails the test via timeout if the channel never closes
}

func TestRequestsCapture(t *testing.T) {
	p := gagetest.NewProvider("")
	p.Enqueue(gagetest.Text("one"), gagetest.Text("two"))

	msgs := []gage.Message{gage.UserText("original")}
	temp := 0.5
	req := gage.Request{
		Model:    "m1",
		System:   "be terse",
		Messages: msgs,
		Tools:    []gage.ToolSchema{{Name: "weather", Description: "d", Parameters: gage.JSONSchema(`{"type":"object"}`)}},
		Options:  gage.GenerateOptions{Temperature: &temp, StopSequences: []string{"END"}},
	}
	ch, err := p.Stream(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	drain(t, ch)

	ch, err = p.Stream(context.Background(), gage.Request{Model: "m2"})
	if err != nil {
		t.Fatal(err)
	}
	drain(t, ch)

	// Mutate the originals after the fact; captures must be unaffected.
	msgs[0].Content[0].Text = "mutated"
	temp = 9.9
	req.Tools[0].Name = "mutated"
	req.Options.StopSequences[0] = "mutated"

	reqs := p.Requests()
	if len(reqs) != 2 {
		t.Fatalf("got %d requests, want 2", len(reqs))
	}
	r := reqs[0]
	if r.Model != "m1" || r.System != "be terse" {
		t.Fatalf("request 0 = %+v", r)
	}
	if got := r.Messages[0].Text(); got != "original" {
		t.Fatalf("captured message text = %q, want %q (deep copy)", got, "original")
	}
	if r.Tools[0].Name != "weather" {
		t.Fatalf("captured tool = %q, want weather (deep copy)", r.Tools[0].Name)
	}
	if *r.Options.Temperature != 0.5 || r.Options.StopSequences[0] != "END" {
		t.Fatalf("captured options = %+v (deep copy expected)", r.Options)
	}
	if reqs[1].Model != "m2" {
		t.Fatalf("request 1 model = %q, want m2 (order preserved)", reqs[1].Model)
	}
}

func TestConcurrentStreams(t *testing.T) {
	const n = 16
	p := gagetest.NewProvider("")
	for i := 0; i < n; i++ {
		p.Enqueue(gagetest.Text(fmt.Sprintf("turn-%d", i)))
	}

	texts := make(chan string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch, err := p.Stream(context.Background(), gage.Request{})
			if err != nil {
				t.Error(err)
				return
			}
			var b strings.Builder
			for ev := range ch {
				if ev.Type == gage.EventTextDelta {
					b.WriteString(ev.Text)
				}
			}
			texts <- b.String()
		}()
	}
	wg.Wait()
	close(texts)

	seen := map[string]bool{}
	for s := range texts {
		seen[s] = true
	}
	if len(seen) != n {
		t.Fatalf("saw %d distinct turns, want %d: %v", len(seen), n, seen)
	}
	if p.Remaining() != 0 {
		t.Fatalf("remaining = %d, want 0", p.Remaining())
	}
	if got := len(p.Requests()); got != n {
		t.Fatalf("requests = %d, want %d", got, n)
	}
}

// TestAgentToolCycle proves the scripted provider satisfies the real agent
// loop across a multi-turn tool cycle, and that the second request carries
// the assistant tool call and the tool result back to the "model".
func TestAgentToolCycle(t *testing.T) {
	p := gagetest.NewProvider("")
	p.Enqueue(
		gagetest.Calls(gagetest.Call("c1", "echo", `{"text":"hi"}`)),
		gagetest.Text("done"),
	)

	reg := tools.NewRegistry()
	reg.MustRegister(tools.ToolFuncMust("echo", "echo the text", func(ctx context.Context, input json.RawMessage) (gage.ToolResult, error) {
		var a struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(input, &a); err != nil {
			return gage.ErrorResult("", err.Error()), nil
		}
		return gage.TextResult("", "echoed:"+a.Text), nil
	}))

	ag, err := agent.New(agent.Config{Provider: p, Registry: reg, System: "sys"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := ag.RunSync(context.Background(), []gage.Message{gage.UserText("go")})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "done" || res.Turns != 2 || res.StopReason != gage.StopEndTurn {
		t.Fatalf("result = %+v", res)
	}
	if res.Usage.Total() == 0 {
		t.Fatal("usage not aggregated across turns")
	}

	reqs := p.Requests()
	if len(reqs) != 2 {
		t.Fatalf("provider saw %d requests, want 2", len(reqs))
	}
	if reqs[0].System != "sys" || len(reqs[0].Tools) != 1 || reqs[0].Tools[0].Name != "echo" {
		t.Fatalf("first request = %+v, want system prompt and echo tool advertised", reqs[0])
	}
	second := reqs[1].Messages
	if len(second) != 3 {
		t.Fatalf("second request has %d messages, want user+assistant+tool", len(second))
	}
	if second[1].Role != gage.RoleAssistant || len(second[1].ToolCalls()) != 1 {
		t.Fatalf("second request message 1 = %+v, want assistant tool call", second[1])
	}
	if second[2].Role != gage.RoleTool || !strings.Contains(second[2].Content[0].ToolResult.Text(), "echoed:hi") {
		t.Fatalf("second request message 2 = %+v, want tool result echoed:hi", second[2])
	}
}

// TestAgentMidRunError proves EnqueueError surfaces as a run failure from the
// real loop after a successful first turn.
func TestAgentMidRunError(t *testing.T) {
	boom := errors.New("rate limited hard")
	p := gagetest.NewProvider("")
	p.Enqueue(gagetest.Calls(gagetest.Call("c1", "echo", `{"text":"x"}`)))
	p.EnqueueError(boom)

	reg := tools.NewRegistry()
	reg.MustRegister(tools.ToolFuncMust("echo", "echo", func(ctx context.Context, input json.RawMessage) (gage.ToolResult, error) {
		return gage.TextResult("", "ok"), nil
	}))

	ag, err := agent.New(agent.Config{Provider: p, Registry: reg})
	if err != nil {
		t.Fatal(err)
	}
	_, err = ag.RunSync(context.Background(), []gage.Message{gage.UserText("go")})
	if !errors.Is(err, boom) {
		t.Fatalf("RunSync err = %v, want %v", err, boom)
	}
}
