package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/deepteams/gage"
	"github.com/deepteams/gage/tools"
)

// pendingFor returns an Approver that suspends the named tools and allows
// everything else.
func pendingFor(names ...string) gage.Approver {
	set := map[string]bool{}
	for _, n := range names {
		set[n] = true
	}
	return gage.ApproverFunc(func(ctx context.Context, r gage.PermissionRequest) (gage.Approval, error) {
		if set[r.Tool] {
			return gage.Approval{}, gage.ErrApprovalPending
		}
		return gage.Allowed(), nil
	})
}

func TestRunPausesOnPendingApproval(t *testing.T) {
	reg := tools.NewRegistry()
	var executed bool
	reg.MustRegister(tools.ToolFuncMust("danger", "d", func(ctx context.Context, input json.RawMessage) (gage.ToolResult, error) {
		executed = true
		return gage.TextResult("", "ran"), nil
	}))
	mp := &mockProvider{turns: [][]gage.Event{
		{gage.MessageStart(), toolCallDone("c1", "danger", `{"x":1}`), gage.UsageEvent(gage.Usage{InputTokens: 10, OutputTokens: 2}), gage.MessageDone(gage.StopToolUse)},
		{gage.MessageStart(), gage.TextDelta("done"), gage.UsageEvent(gage.Usage{InputTokens: 20, OutputTokens: 3}), gage.MessageDone(gage.StopEndTurn)},
	}}
	ag, _ := New(Config{Provider: mp, Registry: reg, Approver: pendingFor("danger")})

	_, err := ag.RunSync(context.Background(), []gage.Message{gage.UserText("go")})
	var paused *Paused
	if !errors.As(err, &paused) {
		t.Fatalf("err = %v, want *Paused", err)
	}
	if !errors.Is(err, gage.ErrApprovalPending) {
		t.Fatal("Paused must match ErrApprovalPending")
	}
	if executed {
		t.Fatal("tool executed before approval")
	}
	cp := paused.Checkpoint
	if cp.Turn != 0 || len(cp.Calls) != 1 || cp.Calls[0].ID != "c1" {
		t.Fatalf("checkpoint = %+v", cp)
	}
	if pending := cp.Pending(); len(pending) != 1 || pending[0].ID != "c1" {
		t.Fatalf("pending = %+v", pending)
	}
	if cp.Usage.InputTokens != 10 || cp.StopReason != gage.StopToolUse {
		t.Fatalf("checkpoint usage/stop = %+v %v", cp.Usage, cp.StopReason)
	}
	// The paused assistant message must already be part of the conversation.
	last := cp.Messages[len(cp.Messages)-1]
	if last.Role != gage.RoleAssistant || len(last.ToolCalls()) != 1 {
		t.Fatalf("last checkpoint message = %+v", last)
	}

	// Approve and resume: the tool runs and the loop continues to the answer.
	res, err := ag.ResumeSync(context.Background(), cp, map[string]gage.Approval{"c1": gage.Allowed()})
	if err != nil {
		t.Fatal(err)
	}
	if !executed {
		t.Fatal("tool not executed after approval")
	}
	if res.Text != "done" || res.Turns != 2 {
		t.Fatalf("result = %+v", res)
	}
	// Usage from before the pause must carry over.
	if res.Usage.InputTokens != 30 || res.Usage.OutputTokens != 5 {
		t.Fatalf("usage = %+v", res.Usage)
	}
}

func TestResumeDenyFeedsErrorResultToModel(t *testing.T) {
	reg := tools.NewRegistry()
	var executed bool
	reg.MustRegister(tools.ToolFuncMust("danger", "d", func(ctx context.Context, input json.RawMessage) (gage.ToolResult, error) {
		executed = true
		return gage.TextResult("", "ran"), nil
	}))
	mp := &mockProvider{turns: [][]gage.Event{
		{gage.MessageStart(), toolCallDone("c1", "danger", `{}`), gage.MessageDone(gage.StopToolUse)},
		{gage.MessageStart(), gage.TextDelta("understood"), gage.MessageDone(gage.StopEndTurn)},
	}}
	ag, _ := New(Config{Provider: mp, Registry: reg, Approver: pendingFor("danger")})
	_, err := ag.RunSync(context.Background(), []gage.Message{gage.UserText("go")})
	var paused *Paused
	if !errors.As(err, &paused) {
		t.Fatalf("err = %v", err)
	}

	res, err := ag.ResumeSync(context.Background(), paused.Checkpoint, map[string]gage.Approval{"c1": gage.Denied("too risky")})
	if err != nil {
		t.Fatal(err)
	}
	if executed {
		t.Fatal("denied tool executed")
	}
	var toolMsg *gage.ToolResult
	for _, m := range res.Messages {
		if m.Role == gage.RoleTool {
			toolMsg = m.Content[0].ToolResult
		}
	}
	if toolMsg == nil || !toolMsg.IsError || toolMsg.CallID != "c1" {
		t.Fatalf("tool result = %+v", toolMsg)
	}
	if res.Text != "understood" {
		t.Fatalf("text = %q", res.Text)
	}
}

func TestResumeWithoutDecisionPausesAgain(t *testing.T) {
	reg := tools.NewRegistry()
	reg.MustRegister(tools.ToolFuncMust("danger", "d", func(ctx context.Context, input json.RawMessage) (gage.ToolResult, error) {
		return gage.TextResult("", "ran"), nil
	}))
	mp := &mockProvider{turns: [][]gage.Event{
		{gage.MessageStart(), toolCallDone("c1", "danger", `{}`), gage.MessageDone(gage.StopToolUse)},
	}}
	ag, _ := New(Config{Provider: mp, Registry: reg, Approver: pendingFor("danger")})
	_, err := ag.RunSync(context.Background(), []gage.Message{gage.UserText("go")})
	var paused *Paused
	if !errors.As(err, &paused) {
		t.Fatalf("err = %v", err)
	}

	_, err = ag.ResumeSync(context.Background(), paused.Checkpoint, nil)
	var again *Paused
	if !errors.As(err, &again) {
		t.Fatalf("err = %v, want *Paused again", err)
	}
	if len(again.Checkpoint.Pending()) != 1 {
		t.Fatalf("pending = %+v", again.Checkpoint.Pending())
	}
}

func TestPauseKeepsCompletedResultsAndSurvivesJSON(t *testing.T) {
	reg := tools.NewRegistry()
	safeRuns := 0
	reg.MustRegister(tools.ToolFuncMust("safe", "s", func(ctx context.Context, input json.RawMessage) (gage.ToolResult, error) {
		safeRuns++
		return gage.TextResult("", "safe-ok"), nil
	}))
	var dangerRan bool
	reg.MustRegister(tools.ToolFuncMust("danger", "d", func(ctx context.Context, input json.RawMessage) (gage.ToolResult, error) {
		dangerRan = true
		return gage.TextResult("", "danger-ok"), nil
	}))
	mp := &mockProvider{turns: [][]gage.Event{
		{
			gage.MessageStart(),
			toolCallDone("c1", "safe", `{}`), toolCallDone("c2", "danger", `{}`),
			gage.MessageDone(gage.StopToolUse),
		},
		{gage.MessageStart(), gage.TextDelta("final"), gage.MessageDone(gage.StopEndTurn)},
	}}
	ag, _ := New(Config{Provider: mp, Registry: reg, Approver: pendingFor("danger")})
	_, err := ag.RunSync(context.Background(), []gage.Message{gage.UserText("go")})
	var paused *Paused
	if !errors.As(err, &paused) {
		t.Fatalf("err = %v", err)
	}
	cp := paused.Checkpoint
	if safeRuns != 1 {
		t.Fatalf("safe tool ran %d times before pause", safeRuns)
	}
	if len(cp.Results) != 1 || cp.Results[0].CallID != "c1" {
		t.Fatalf("checkpoint results = %+v", cp.Results)
	}
	if pending := cp.Pending(); len(pending) != 1 || pending[0].ID != "c2" {
		t.Fatalf("pending = %+v", pending)
	}

	// Persist and restore the checkpoint like an out-of-process approver would.
	data, err := json.Marshal(cp)
	if err != nil {
		t.Fatal(err)
	}
	var restored gage.Checkpoint
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}

	res, err := ag.ResumeSync(context.Background(), &restored, map[string]gage.Approval{"c2": gage.Allowed()})
	if err != nil {
		t.Fatal(err)
	}
	if !dangerRan {
		t.Fatal("approved tool did not run")
	}
	if safeRuns != 1 {
		t.Fatalf("safe tool re-executed on resume (%d runs)", safeRuns)
	}
	// Results must be fed back in original call order.
	var order []string
	for _, m := range res.Messages {
		if m.Role == gage.RoleTool {
			order = append(order, m.Content[0].ToolResult.CallID)
		}
	}
	if len(order) != 2 || order[0] != "c1" || order[1] != "c2" {
		t.Fatalf("result order = %v", order)
	}
	if res.Text != "final" {
		t.Fatalf("text = %q", res.Text)
	}
}

func TestResumeRejectsInvalidCheckpoint(t *testing.T) {
	mp := &mockProvider{}
	ag, _ := New(Config{Provider: mp})
	if _, err := ag.Resume(context.Background(), nil, nil); err == nil {
		t.Fatal("expected error for nil checkpoint")
	}
	if _, err := ag.Resume(context.Background(), &gage.Checkpoint{}, nil); err == nil {
		t.Fatal("expected error for empty checkpoint")
	}
}
