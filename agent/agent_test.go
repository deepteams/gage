package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deepteams/gage"
	"github.com/deepteams/gage/tools"
)

// mockProvider returns a scripted sequence of event slices, one per turn.
type mockProvider struct {
	mu      sync.Mutex
	turns   [][]gage.Event
	call    int
	lastReq gage.Request
}

func (m *mockProvider) Name() string { return "mock" }

func (m *mockProvider) Stream(ctx context.Context, req gage.Request) (<-chan gage.Event, error) {
	m.mu.Lock()
	m.lastReq = req
	turn := m.call
	m.call++
	m.mu.Unlock()

	ch := make(chan gage.Event)
	go func() {
		defer close(ch)
		if turn >= len(m.turns) {
			ch <- gage.MessageDone("end_turn")
			return
		}
		for _, e := range m.turns[turn] {
			select {
			case ch <- e:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

func toolCallDone(id, name, args string) gage.Event {
	return gage.ToolCallDone(gage.ToolCall{ID: id, Name: name, Input: json.RawMessage(args)})
}

func TestAgentSingleTurnNoTools(t *testing.T) {
	mp := &mockProvider{turns: [][]gage.Event{
		{gage.MessageStart(), gage.TextDelta("Hello"), gage.MessageDone("end_turn")},
	}}
	ag, err := New(Config{Provider: mp})
	if err != nil {
		t.Fatal(err)
	}
	ch, _ := ag.Run(context.Background(), []gage.Message{gage.UserText("hi")})

	var text strings.Builder
	var sawDone bool
	for e := range ch {
		switch e.Type {
		case gage.EventTextDelta:
			text.WriteString(e.Text)
		case gage.EventDone:
			sawDone = true
		}
	}
	if text.String() != "Hello" || !sawDone {
		t.Fatalf("text=%q done=%v", text.String(), sawDone)
	}
}

func TestAgentToolLoop(t *testing.T) {
	reg := tools.NewRegistry()
	reg.MustRegister(tools.ToolFuncMust("echo", "echo the text", func(ctx context.Context, input json.RawMessage) (gage.ToolResult, error) {
		var a struct {
			Text string `json:"text"`
		}
		json.Unmarshal(input, &a)
		return gage.TextResult("", "echoed:"+a.Text), nil
	}))

	mp := &mockProvider{turns: [][]gage.Event{
		// Turn 0: request a tool call.
		{gage.MessageStart(), toolCallDone("c1", "echo", `{"text":"hi"}`), gage.MessageDone("tool_use")},
		// Turn 1: final answer.
		{gage.MessageStart(), gage.TextDelta("done"), gage.MessageDone("end_turn")},
	}}

	ag, _ := New(Config{Provider: mp, Registry: reg})
	ch, _ := ag.Run(context.Background(), []gage.Message{gage.UserText("please echo")})

	var toolResults []gage.ToolResult
	var finalText strings.Builder
	for e := range ch {
		switch e.Type {
		case gage.EventToolResult:
			toolResults = append(toolResults, *e.ToolResult)
		case gage.EventTextDelta:
			finalText.WriteString(e.Text)
		}
	}
	if len(toolResults) != 1 || toolResults[0].Text() != "echoed:hi" {
		t.Fatalf("tool results = %+v", toolResults)
	}
	if toolResults[0].CallID != "c1" {
		t.Fatalf("call id not propagated: %q", toolResults[0].CallID)
	}
	if finalText.String() != "done" {
		t.Fatalf("final = %q", finalText.String())
	}
	// The second provider call must have seen the tool result in history.
	mp.mu.Lock()
	msgs := mp.lastReq.Messages
	mp.mu.Unlock()
	foundResult := false
	for _, m := range msgs {
		if m.Role == gage.RoleTool {
			foundResult = true
		}
	}
	if !foundResult {
		t.Fatal("tool result not fed back into conversation")
	}
}

func TestAgentUnknownTool(t *testing.T) {
	reg := tools.NewRegistry()
	mp := &mockProvider{turns: [][]gage.Event{
		{gage.MessageStart(), toolCallDone("c1", "nope", `{}`), gage.MessageDone("tool_use")},
		{gage.MessageStart(), gage.TextDelta("ok"), gage.MessageDone("end_turn")},
	}}
	ag, _ := New(Config{Provider: mp, Registry: reg})
	ch, _ := ag.Run(context.Background(), []gage.Message{gage.UserText("x")})

	var res *gage.ToolResult
	for e := range ch {
		if e.Type == gage.EventToolResult {
			res = e.ToolResult
		}
	}
	if res == nil || !res.IsError || !strings.Contains(res.Text(), "unknown tool") {
		t.Fatalf("expected unknown tool error, got %+v", res)
	}
}

func TestAgentMaxTurns(t *testing.T) {
	reg := tools.NewRegistry()
	reg.MustRegister(tools.ToolFuncMust("loop", "loops", func(ctx context.Context, input json.RawMessage) (gage.ToolResult, error) {
		return gage.TextResult("", "again"), nil
	}))
	// Provider always asks for the tool -> never terminates on its own.
	mp := &mockProvider{turns: [][]gage.Event{}}
	always := []gage.Event{gage.MessageStart(), toolCallDone("c", "loop", `{}`), gage.MessageDone("tool_use")}
	mp.turns = [][]gage.Event{always, always, always}

	ag, _ := New(Config{Provider: mp, Registry: reg, MaxTurns: 2})
	ch, _ := ag.Run(context.Background(), []gage.Message{gage.UserText("go")})

	var sawMaxTurns bool
	for e := range ch {
		if e.Type == gage.EventError && e.Err == gage.ErrMaxTurns {
			sawMaxTurns = true
		}
	}
	if !sawMaxTurns {
		t.Fatal("expected ErrMaxTurns")
	}
}

func TestAgentApproverDeny(t *testing.T) {
	reg := tools.NewRegistry()
	var executed bool
	reg.MustRegister(tools.ToolFuncMust("danger", "does stuff", func(ctx context.Context, input json.RawMessage) (gage.ToolResult, error) {
		executed = true
		return gage.TextResult("", "ran"), nil
	}))
	mp := &mockProvider{turns: [][]gage.Event{
		{gage.MessageStart(), toolCallDone("c1", "danger", `{}`), gage.MessageDone("tool_use")},
		{gage.MessageStart(), gage.TextDelta("stopped"), gage.MessageDone("end_turn")},
	}}
	deny := gage.ApproverFunc(func(ctx context.Context, r gage.PermissionRequest) (gage.Decision, error) {
		return gage.Deny, nil
	})
	ag, _ := New(Config{Provider: mp, Registry: reg, Approver: deny})
	ch, _ := ag.Run(context.Background(), []gage.Message{gage.UserText("x")})

	var res *gage.ToolResult
	for e := range ch {
		if e.Type == gage.EventToolResult {
			res = e.ToolResult
		}
	}
	if executed {
		t.Fatal("tool executed despite denial")
	}
	if res == nil || !res.IsError || !strings.Contains(res.Text(), "denied") {
		t.Fatalf("expected denial result, got %+v", res)
	}
}

func TestAgentApproverReceivesMetadataAndContext(t *testing.T) {
	reg := tools.NewRegistry()
	reg.MustRegister(tools.NewBashTool(tools.BashConfig{}))
	mp := &mockProvider{turns: [][]gage.Event{
		{gage.MessageStart(), toolCallDone("c1", "bash", `{"command":"rm -rf tmp"}`), gage.MessageDone("tool_use")},
		{gage.MessageStart(), gage.TextDelta("stopped"), gage.MessageDone("end_turn")},
	}}
	var req gage.PermissionRequest
	deny := gage.ApproverFunc(func(ctx context.Context, r gage.PermissionRequest) (gage.Decision, error) {
		req = r
		return gage.Deny, nil
	})
	ag, _ := New(Config{Provider: mp, Registry: reg, Approver: deny, Name: "main"})
	ch, _ := ag.Run(context.Background(), []gage.Message{gage.UserText("x")})
	for range ch {
	}
	if req.Tool != "bash" || req.Agent != "main" || req.RunID == "" || req.Turn != 0 {
		t.Fatalf("permission context = %+v", req)
	}
	if !req.Metadata.Shell || !req.Metadata.RequiresApproval || !req.Metadata.Destructive {
		t.Fatalf("metadata = %+v", req.Metadata)
	}
	if !strings.Contains(req.Summary, "rm -rf tmp") {
		t.Fatalf("summary = %q", req.Summary)
	}
}

func TestAgentToolPanicRecovered(t *testing.T) {
	reg := tools.NewRegistry()
	reg.MustRegister(tools.ToolFuncMust("boom", "panics", func(ctx context.Context, input json.RawMessage) (gage.ToolResult, error) {
		panic("bad tool")
	}))
	mp := &mockProvider{turns: [][]gage.Event{
		{gage.MessageStart(), toolCallDone("c1", "boom", `{}`), gage.MessageDone("tool_use")},
		{gage.MessageStart(), gage.TextDelta("recovered"), gage.MessageDone("end_turn")},
	}}
	ag, _ := New(Config{Provider: mp, Registry: reg})
	ch, _ := ag.Run(context.Background(), []gage.Message{gage.UserText("x")})

	var res *gage.ToolResult
	for e := range ch {
		if e.Type == gage.EventToolResult {
			res = e.ToolResult
		}
	}
	if res == nil || !res.IsError || !strings.Contains(res.Text(), "tool panic") {
		t.Fatalf("expected panic tool result, got %+v", res)
	}
}

func TestAgentToolTimeout(t *testing.T) {
	reg := tools.NewRegistry()
	release := make(chan struct{})
	defer close(release)
	reg.MustRegister(tools.ToolFuncMust("slow", "blocks", func(ctx context.Context, input json.RawMessage) (gage.ToolResult, error) {
		<-release
		return gage.TextResult("", "late"), nil
	}))
	mp := &mockProvider{turns: [][]gage.Event{
		{gage.MessageStart(), toolCallDone("c1", "slow", `{}`), gage.MessageDone("tool_use")},
		{gage.MessageStart(), gage.TextDelta("done"), gage.MessageDone("end_turn")},
	}}
	ag, _ := New(Config{Provider: mp, Registry: reg, ToolTimeout: 10 * time.Millisecond})
	ch, _ := ag.Run(context.Background(), []gage.Message{gage.UserText("x")})

	var res *gage.ToolResult
	for e := range ch {
		if e.Type == gage.EventToolResult {
			res = e.ToolResult
		}
	}
	if res == nil || !res.IsError || !strings.Contains(res.Text(), "timed out") {
		t.Fatalf("expected timeout tool result, got %+v", res)
	}
}

func TestAgentObserver(t *testing.T) {
	reg := tools.NewRegistry()
	reg.MustRegister(tools.ToolFuncMust("echo", "echo", func(ctx context.Context, input json.RawMessage) (gage.ToolResult, error) {
		return gage.TextResult("", "ok"), nil
	}))
	mp := &mockProvider{turns: [][]gage.Event{
		{gage.MessageStart(), toolCallDone("c1", "echo", `{}`), gage.MessageDone("tool_use")},
		{gage.MessageStart(), gage.TextDelta("done"), gage.MessageDone("end_turn")},
	}}
	var mu sync.Mutex
	var observations []Observation
	obs := ObserverFunc(func(ctx context.Context, o Observation) {
		mu.Lock()
		defer mu.Unlock()
		observations = append(observations, o)
	})
	ag, _ := New(Config{Provider: mp, Registry: reg, Name: "main", Observer: obs})
	ch, _ := ag.Run(context.Background(), []gage.Message{gage.UserText("x")})
	for range ch {
	}

	mu.Lock()
	defer mu.Unlock()
	seen := map[ObservationType]bool{}
	var runID string
	for _, o := range observations {
		seen[o.Type] = true
		if o.RunID != "" {
			if runID == "" {
				runID = o.RunID
			} else if runID != o.RunID {
				t.Fatalf("observations use multiple run IDs: %q vs %q", runID, o.RunID)
			}
		}
	}
	for _, typ := range []ObservationType{ObservationRunStart, ObservationTurnStart, ObservationToolStart, ObservationToolEnd, ObservationRunEnd} {
		if !seen[typ] {
			t.Fatalf("missing observation %s in %+v", typ, observations)
		}
	}
}

func TestAgentContextCancel(t *testing.T) {
	mp := &mockProvider{turns: [][]gage.Event{
		{gage.MessageStart(), gage.TextDelta("partial"), gage.MessageDone("end_turn")},
	}}
	ag, _ := New(Config{Provider: mp})
	ctx, cancel := context.WithCancel(context.Background())
	ch, _ := ag.Run(ctx, []gage.Message{gage.UserText("hi")})
	cancel()
	// Draining must terminate (channel closes) even after cancel.
	for range ch {
	}
}

func TestSubAgentAsTool(t *testing.T) {
	sub := &mockProvider{turns: [][]gage.Event{
		{gage.MessageStart(), gage.TextDelta("subresult"), gage.MessageDone("end_turn")},
	}}
	subAgent, _ := New(Config{Provider: sub, Name: "researcher"})
	tool := subAgent.AsTool("researcher", "delegates research")

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"find X"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.Text() != "subresult" {
		t.Fatalf("subagent result = %q", res.Text())
	}
}

func TestSubAgentAsToolReturnsOnlyFinalAnswer(t *testing.T) {
	sub := &mockProvider{turns: [][]gage.Event{
		{gage.MessageStart(), gage.TextDelta("intermediate"), toolCallDone("c1", "missing", `{}`), gage.MessageDone("tool_use")},
		{gage.MessageStart(), gage.TextDelta("final"), gage.MessageDone("end_turn")},
	}}
	subAgent, _ := New(Config{Provider: sub, Name: "researcher"})
	tool := subAgent.AsTool("researcher", "delegates research")

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"find X"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.Text() != "final" {
		t.Fatalf("subagent result = %q", res.Text())
	}
}

func TestNewRequiresProvider(t *testing.T) {
	if _, err := New(Config{}); err != gage.ErrNoProvider {
		t.Fatalf("err = %v", err)
	}
}
