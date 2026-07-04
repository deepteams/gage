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

type waitingProvider struct{}

func (waitingProvider) Name() string { return "waiting" }

func (waitingProvider) Stream(ctx context.Context, req gage.Request) (<-chan gage.Event, error) {
	ch := make(chan gage.Event)
	go func() {
		defer close(ch)
		select {
		case ch <- gage.MessageStart():
		case <-ctx.Done():
			return
		}
		<-ctx.Done()
	}()
	return ch, nil
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
	deny := gage.ApproverFunc(func(ctx context.Context, r gage.PermissionRequest) (gage.Approval, error) {
		return gage.Denied("not allowed in tests"), nil
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
	deny := gage.ApproverFunc(func(ctx context.Context, r gage.PermissionRequest) (gage.Approval, error) {
		req = r
		return gage.Denied(""), nil
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

func TestRunSyncResult(t *testing.T) {
	mp := &mockProvider{turns: [][]gage.Event{
		{gage.MessageStart(), gage.TextDelta("Hello"), gage.UsageEvent(gage.Usage{InputTokens: 10, OutputTokens: 5}), gage.MessageDone(gage.StopEndTurn)},
	}}
	ag, _ := New(Config{Provider: mp})
	res, err := ag.RunSync(context.Background(), []gage.Message{gage.UserText("hi")})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "Hello" || res.Turns != 1 || res.StopReason != gage.StopEndTurn {
		t.Fatalf("result = %+v", res)
	}
	if res.Usage.InputTokens != 10 || res.Usage.OutputTokens != 5 {
		t.Fatalf("usage = %+v", res.Usage)
	}
	if len(res.Messages) != 2 || res.Messages[1].Role != gage.RoleAssistant {
		t.Fatalf("messages = %+v", res.Messages)
	}
}

func TestRunSyncConfigTimeoutReturnsDeadlineExceeded(t *testing.T) {
	ag, _ := New(Config{Provider: waitingProvider{}, Timeout: 10 * time.Millisecond})
	_, err := ag.RunSync(context.Background(), []gage.Message{gage.UserText("hi")})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
}

func TestRunConfigTimeoutEmitsErrorEvent(t *testing.T) {
	ag, _ := New(Config{Provider: waitingProvider{}, Timeout: 10 * time.Millisecond})
	ch, err := ag.Run(context.Background(), []gage.Message{gage.UserText("hi")})
	if err != nil {
		t.Fatal(err)
	}
	var sawStart bool
	var terminal error
	for ev := range ch {
		switch ev.Type {
		case gage.EventMessageStart:
			sawStart = true
		case gage.EventError:
			terminal = ev.Err
		}
	}
	if !sawStart {
		t.Fatal("expected initial message_start")
	}
	if !errors.Is(terminal, context.DeadlineExceeded) {
		t.Fatalf("terminal error = %v, want DeadlineExceeded", terminal)
	}
}

func TestUsageAggregatedAcrossTurns(t *testing.T) {
	reg := tools.NewRegistry()
	reg.MustRegister(tools.ToolFuncMust("echo", "echo", func(ctx context.Context, input json.RawMessage) (gage.ToolResult, error) {
		return gage.TextResult("", "ok"), nil
	}))
	mp := &mockProvider{turns: [][]gage.Event{
		{gage.MessageStart(), toolCallDone("c1", "echo", `{}`), gage.UsageEvent(gage.Usage{InputTokens: 10, OutputTokens: 2}), gage.MessageDone(gage.StopToolUse)},
		{gage.MessageStart(), gage.TextDelta("done"), gage.UsageEvent(gage.Usage{InputTokens: 20, OutputTokens: 3}), gage.MessageDone(gage.StopEndTurn)},
	}}
	ag, _ := New(Config{Provider: mp, Registry: reg})
	res, err := ag.RunSync(context.Background(), []gage.Message{gage.UserText("x")})
	if err != nil {
		t.Fatal(err)
	}
	if res.Usage.InputTokens != 30 || res.Usage.OutputTokens != 5 || res.Turns != 2 {
		t.Fatalf("usage = %+v turns = %d", res.Usage, res.Turns)
	}
}

func TestReasoningPreservedInHistory(t *testing.T) {
	reg := tools.NewRegistry()
	reg.MustRegister(tools.ToolFuncMust("echo", "echo", func(ctx context.Context, input json.RawMessage) (gage.ToolResult, error) {
		return gage.TextResult("", "ok"), nil
	}))
	mp := &mockProvider{turns: [][]gage.Event{
		{
			gage.MessageStart(),
			gage.ReasoningDelta("thinking "), gage.ReasoningDelta("hard"), gage.ReasoningDone("sig-1"),
			gage.TextDelta("calling tool"),
			toolCallDone("c1", "echo", `{}`),
			gage.MessageDone(gage.StopToolUse),
		},
		{gage.MessageStart(), gage.TextDelta("done"), gage.MessageDone(gage.StopEndTurn)},
	}}
	ag, _ := New(Config{Provider: mp, Registry: reg})
	if _, err := ag.RunSync(context.Background(), []gage.Message{gage.UserText("x")}); err != nil {
		t.Fatal(err)
	}
	// The second provider call must have replayed the signed reasoning block
	// before the text and tool-use parts.
	mp.mu.Lock()
	msgs := mp.lastReq.Messages
	mp.mu.Unlock()
	var asst *gage.Message
	for i := range msgs {
		if msgs[i].Role == gage.RoleAssistant {
			asst = &msgs[i]
			break
		}
	}
	if asst == nil {
		t.Fatal("no assistant message in history")
	}
	if len(asst.Content) < 3 {
		t.Fatalf("content = %+v", asst.Content)
	}
	if p := asst.Content[0]; p.Kind != gage.PartReasoning || p.Text != "thinking hard" || p.Signature != "sig-1" {
		t.Fatalf("reasoning part = %+v", p)
	}
	if asst.Content[1].Kind != gage.PartText || asst.Content[2].Kind != gage.PartToolUse {
		t.Fatalf("part order = %+v", asst.Content)
	}
}

func TestAssistantHistoryPreservesInterleavedToolOrder(t *testing.T) {
	reg := tools.NewRegistry()
	reg.MustRegister(tools.ToolFuncMust("echo", "echo", func(ctx context.Context, input json.RawMessage) (gage.ToolResult, error) {
		return gage.TextResult("", "ok"), nil
	}))
	mp := &mockProvider{turns: [][]gage.Event{
		{
			gage.MessageStart(),
			gage.TextDelta("before "),
			toolCallDone("c1", "echo", `{}`),
			gage.TextDelta("after"),
			gage.MessageDone(gage.StopToolUse),
		},
		{gage.MessageStart(), gage.TextDelta("done"), gage.MessageDone(gage.StopEndTurn)},
	}}
	ag, _ := New(Config{Provider: mp, Registry: reg})
	if _, err := ag.RunSync(context.Background(), []gage.Message{gage.UserText("x")}); err != nil {
		t.Fatal(err)
	}

	mp.mu.Lock()
	msgs := mp.lastReq.Messages
	mp.mu.Unlock()
	var asst *gage.Message
	for i := range msgs {
		if msgs[i].Role == gage.RoleAssistant {
			asst = &msgs[i]
			break
		}
	}
	if asst == nil {
		t.Fatal("no assistant message in history")
	}
	if len(asst.Content) != 3 {
		t.Fatalf("content = %+v", asst.Content)
	}
	if asst.Content[0].Kind != gage.PartText || asst.Content[0].Text != "before " ||
		asst.Content[1].Kind != gage.PartToolUse ||
		asst.Content[2].Kind != gage.PartText || asst.Content[2].Text != "after" {
		t.Fatalf("part order = %+v", asst.Content)
	}
}

func TestHooksRewriteInputAndResult(t *testing.T) {
	reg := tools.NewRegistry()
	var gotInput string
	reg.MustRegister(tools.ToolFuncMust("echo", "echo", func(ctx context.Context, input json.RawMessage) (gage.ToolResult, error) {
		gotInput = string(input)
		return gage.TextResult("", "secret=hunter2"), nil
	}))
	mp := &mockProvider{turns: [][]gage.Event{
		{gage.MessageStart(), toolCallDone("c1", "echo", `{"text":"raw"}`), gage.MessageDone(gage.StopToolUse)},
		{gage.MessageStart(), gage.TextDelta("done"), gage.MessageDone(gage.StopEndTurn)},
	}}
	ag, _ := New(Config{Provider: mp, Registry: reg, Hooks: Hooks{
		PreToolUse: func(ctx context.Context, tc gage.ToolCall) (gage.ToolCall, error) {
			tc.Input = json.RawMessage(`{"text":"rewritten"}`)
			return tc, nil
		},
		PostToolUse: func(ctx context.Context, tc gage.ToolCall, res gage.ToolResult) gage.ToolResult {
			return gage.TextResult(res.CallID, strings.ReplaceAll(res.Text(), "hunter2", "[redacted]"))
		},
	}})
	ch, _ := ag.Run(context.Background(), []gage.Message{gage.UserText("x")})
	var res *gage.ToolResult
	for e := range ch {
		if e.Type == gage.EventToolResult {
			res = e.ToolResult
		}
	}
	if gotInput != `{"text":"rewritten"}` {
		t.Fatalf("tool saw input %q", gotInput)
	}
	if res == nil || res.Text() != "secret=[redacted]" {
		t.Fatalf("post hook result = %+v", res)
	}
}

func TestPreToolUseHookBlocks(t *testing.T) {
	reg := tools.NewRegistry()
	var executed bool
	reg.MustRegister(tools.ToolFuncMust("danger", "d", func(ctx context.Context, input json.RawMessage) (gage.ToolResult, error) {
		executed = true
		return gage.TextResult("", "ran"), nil
	}))
	mp := &mockProvider{turns: [][]gage.Event{
		{gage.MessageStart(), toolCallDone("c1", "danger", `{}`), gage.MessageDone(gage.StopToolUse)},
		{gage.MessageStart(), gage.TextDelta("ok"), gage.MessageDone(gage.StopEndTurn)},
	}}
	ag, _ := New(Config{Provider: mp, Registry: reg, Hooks: Hooks{
		PreToolUse: func(ctx context.Context, tc gage.ToolCall) (gage.ToolCall, error) {
			return tc, context.Canceled
		},
	}})
	ch, _ := ag.Run(context.Background(), []gage.Message{gage.UserText("x")})
	var res *gage.ToolResult
	for e := range ch {
		if e.Type == gage.EventToolResult {
			res = e.ToolResult
		}
	}
	if executed {
		t.Fatal("tool ran despite blocking hook")
	}
	if res == nil || !res.IsError || !strings.Contains(res.Text(), "blocked by pre-tool hook") {
		t.Fatalf("result = %+v", res)
	}
}

func TestParallelToolExecution(t *testing.T) {
	reg := tools.NewRegistry()
	var mu sync.Mutex
	inFlight, peak := 0, 0
	block := make(chan struct{})
	reg.MustRegister(tools.ToolFuncMust("slow", "s", func(ctx context.Context, input json.RawMessage) (gage.ToolResult, error) {
		mu.Lock()
		inFlight++
		if inFlight > peak {
			peak = inFlight
		}
		ready := inFlight == 2
		mu.Unlock()
		if ready {
			close(block)
		}
		select {
		case <-block:
		case <-time.After(2 * time.Second):
		}
		mu.Lock()
		inFlight--
		mu.Unlock()
		return gage.TextResult("", "ok"), nil
	}))
	mp := &mockProvider{turns: [][]gage.Event{
		{
			gage.MessageStart(),
			toolCallDone("c1", "slow", `{}`), toolCallDone("c2", "slow", `{}`),
			gage.MessageDone(gage.StopToolUse),
		},
		{gage.MessageStart(), gage.TextDelta("done"), gage.MessageDone(gage.StopEndTurn)},
	}}
	ag, _ := New(Config{Provider: mp, Registry: reg, MaxParallelTools: 2})
	res, err := ag.RunSync(context.Background(), []gage.Message{gage.UserText("x")})
	if err != nil {
		t.Fatal(err)
	}
	if peak != 2 {
		t.Fatalf("peak concurrency = %d, want 2", peak)
	}
	// Results must be fed back in call order regardless of completion order.
	var callIDs []string
	for _, m := range res.Messages {
		if m.Role == gage.RoleTool {
			callIDs = append(callIDs, m.Content[0].ToolResult.CallID)
		}
	}
	if len(callIDs) != 2 || callIDs[0] != "c1" || callIDs[1] != "c2" {
		t.Fatalf("call order = %v", callIDs)
	}
}

func TestCompactionTriggered(t *testing.T) {
	reg := tools.NewRegistry()
	reg.MustRegister(tools.ToolFuncMust("echo", "echo", func(ctx context.Context, input json.RawMessage) (gage.ToolResult, error) {
		return gage.TextResult("", "ok"), nil
	}))
	mp := &mockProvider{turns: [][]gage.Event{
		{gage.MessageStart(), toolCallDone("c1", "echo", `{}`), gage.UsageEvent(gage.Usage{InputTokens: 5000}), gage.MessageDone(gage.StopToolUse)},
		{gage.MessageStart(), gage.TextDelta("done"), gage.UsageEvent(gage.Usage{InputTokens: 100}), gage.MessageDone(gage.StopEndTurn)},
	}}
	var compacted bool
	comp := gage.CompactorFunc(func(ctx context.Context, msgs []gage.Message, u gage.Usage) ([]gage.Message, error) {
		compacted = true
		if u.InputTokens != 5000 {
			t.Errorf("compactor usage = %+v", u)
		}
		return msgs, nil
	})
	ag, _ := New(Config{Provider: mp, Registry: reg, Compactor: comp, CompactAfter: 1000})
	if _, err := ag.RunSync(context.Background(), []gage.Message{gage.UserText("x")}); err != nil {
		t.Fatal(err)
	}
	if !compacted {
		t.Fatal("compactor not invoked past threshold")
	}
}

func TestTrimCompactorPreservesToolPairing(t *testing.T) {
	msgs := []gage.Message{
		gage.UserText("task"),
		gage.AssistantText("a1"),
		{Role: gage.RoleAssistant, Content: []gage.ContentPart{gage.ToolUsePart(gage.ToolCall{ID: "c1", Name: "t"})}},
		gage.ToolResultMessage(gage.TextResult("c1", "r1")),
		gage.AssistantText("a2"),
		gage.AssistantText("a3"),
	}
	// keep=3 would start the tail on the tool result; the cut must skip past it.
	out, err := Trim(3).Compact(context.Background(), msgs, gage.Usage{})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range out {
		if m.Role == gage.RoleTool {
			t.Fatalf("orphaned tool result in %+v", out)
		}
	}
	if out[0].Text() != "task" {
		t.Fatalf("head not preserved: %+v", out[0])
	}
}

func TestSubAgentFailureIsErrorResult(t *testing.T) {
	// A sub-agent whose provider errors must return an error result, not an
	// empty success.
	failing := &mockProvider{turns: [][]gage.Event{
		{gage.MessageStart(), gage.ErrorEvent(gage.ErrRateLimited)},
	}}
	subAgent, _ := New(Config{Provider: failing, Name: "researcher"})
	tool := subAgent.AsTool("researcher", "delegates research")
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"find X"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(res.Text(), "failed") {
		t.Fatalf("expected error result, got %+v", res)
	}
}

func TestApproverUpdatedInput(t *testing.T) {
	reg := tools.NewRegistry()
	var gotInput string
	reg.MustRegister(tools.ToolFuncMust("echo", "echo", func(ctx context.Context, input json.RawMessage) (gage.ToolResult, error) {
		gotInput = string(input)
		return gage.TextResult("", "ok"), nil
	}))
	mp := &mockProvider{turns: [][]gage.Event{
		{gage.MessageStart(), toolCallDone("c1", "echo", `{"path":"/etc"}`), gage.MessageDone(gage.StopToolUse)},
		{gage.MessageStart(), gage.TextDelta("done"), gage.MessageDone(gage.StopEndTurn)},
	}}
	approver := gage.ApproverFunc(func(ctx context.Context, r gage.PermissionRequest) (gage.Approval, error) {
		return gage.Approval{Allow: true, UpdatedInput: json.RawMessage(`{"path":"/tmp"}`)}, nil
	})
	ag, _ := New(Config{Provider: mp, Registry: reg, Approver: approver})
	if _, err := ag.RunSync(context.Background(), []gage.Message{gage.UserText("x")}); err != nil {
		t.Fatal(err)
	}
	if gotInput != `{"path":"/tmp"}` {
		t.Fatalf("tool saw input %q", gotInput)
	}
}
