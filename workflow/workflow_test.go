package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/deepteams/gage"
	"github.com/deepteams/gage/agent"
	"github.com/deepteams/gage/gagetest"
	"github.com/deepteams/gage/sessions"
	"github.com/deepteams/gage/tools"
)

func TestRunnerPersistsPauseAndResume(t *testing.T) {
	ctx := context.Background()
	provider := gagetest.NewProvider("")
	provider.Enqueue(
		gagetest.Calls(gagetest.Call("c1", "danger", map[string]any{})),
		gagetest.Text("done"),
	)
	reg := tools.NewRegistry()
	executed := false
	reg.MustRegister(tools.ToolFuncMust("danger", "danger", func(ctx context.Context, input json.RawMessage) (gage.ToolResult, error) {
		executed = true
		return gage.TextResult("", "ran"), nil
	}))
	ag, err := agent.New(agent.Config{
		Provider: provider,
		Registry: reg,
		Approver: gage.ApproverFunc(func(ctx context.Context, req gage.PermissionRequest) (gage.Approval, error) {
			return gage.Approval{}, gage.ErrApprovalPending
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	store := sessions.Memory()
	r := New(ag, store)

	out, err := r.Run(ctx, "s1", []gage.Message{gage.UserText("go")})
	if !errors.Is(err, gage.ErrApprovalPending) {
		t.Fatalf("run err = %v, want approval pending", err)
	}
	if out.Checkpoint == nil || executed {
		t.Fatalf("paused outcome = %+v executed=%v", out, executed)
	}
	saved, err := store.LoadSession(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if saved.Checkpoint == nil || len(saved.Checkpoint.Pending()) != 1 {
		t.Fatalf("saved checkpoint = %+v", saved.Checkpoint)
	}

	out, err = r.Resume(ctx, "s1", map[string]gage.Approval{"c1": gage.Allowed()})
	if err != nil {
		t.Fatal(err)
	}
	if !executed || out.Result == nil || out.Result.Text != "done" {
		t.Fatalf("resume outcome = %+v executed=%v", out, executed)
	}
	saved, err = store.LoadSession(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if saved.Checkpoint != nil || len(saved.Messages) == 0 {
		t.Fatalf("final saved session = %+v", saved)
	}
}
