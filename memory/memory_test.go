package memory

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/deepteams/gage"
)

func TestStoreRecallFilterAndForget(t *testing.T) {
	store := New()
	ctx := context.Background()

	first, err := store.Remember(ctx, gage.Memory{
		Text:      "User prefers concise Go examples.",
		Metadata:  map[string]string{"user": "u1", "topic": "go"},
		CreatedAt: time.Unix(100, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Remember(ctx, gage.Memory{
		Text:      "User prefers detailed Python notebooks.",
		Metadata:  map[string]string{"user": "u1", "topic": "python"},
		CreatedAt: time.Unix(200, 0),
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := store.Recall(ctx, gage.MemoryQuery{Query: "Go examples", Metadata: map[string]string{"user": "u1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != first.ID {
		t.Fatalf("recall = %+v, want %s", got, first.ID)
	}

	got, err = store.Recall(ctx, gage.MemoryQuery{Metadata: map[string]string{"user": "u1"}, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != second.ID {
		t.Fatalf("recent recall = %+v, want %s", got, second.ID)
	}

	if err := store.Forget(ctx, second.ID); err != nil {
		t.Fatal(err)
	}
	got, err = store.Recall(ctx, gage.MemoryQuery{Query: "Python"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("forgotten memory recalled: %+v", got)
	}
}

func TestMemoryTools(t *testing.T) {
	store := New()
	tools := NewTools(store)
	remember := toolByName(t, tools, "memory_remember")
	recall := toolByName(t, tools, "memory_recall")
	forget := toolByName(t, tools, "memory_forget")

	res := runTool(t, remember, `{"text":"Project Gage uses provider adapters.","metadata":{"project":"gage"}}`)
	if res.IsError || !strings.Contains(res.Text(), "remembered mem_1") {
		t.Fatalf("remember = %+v", res)
	}
	if meta := gage.MetadataOf(remember); !meta.Destructive || !meta.RequiresApproval {
		t.Fatalf("remember metadata = %+v", meta)
	}
	if meta := gage.MetadataOf(recall); !meta.ReadOnly {
		t.Fatalf("recall metadata = %+v", meta)
	}

	res = runTool(t, recall, `{"query":"provider adapters","metadata":{"project":"gage"}}`)
	if res.IsError || !strings.Contains(res.Text(), "Project Gage") {
		t.Fatalf("recall = %+v", res)
	}
	var memories []gage.Memory
	if err := json.Unmarshal([]byte(res.Text()), &memories); err != nil {
		t.Fatalf("recall should return JSON memories: %v\n%s", err, res.Text())
	}
	if len(memories) != 1 || memories[0].ID != "mem_1" {
		t.Fatalf("memories = %+v", memories)
	}

	res = runTool(t, forget, `{"id":"mem_1"}`)
	if res.IsError {
		t.Fatalf("forget = %+v", res)
	}
	res = runTool(t, recall, `{"query":"provider adapters"}`)
	if res.Text() != "no memories" {
		t.Fatalf("recall after forget = %q", res.Text())
	}
}

func toolByName(t *testing.T, tools []gage.Tool, name string) gage.Tool {
	t.Helper()
	for _, tool := range tools {
		if tool.Name() == name {
			return tool
		}
	}
	t.Fatalf("tool %s not found", name)
	return nil
}

func runTool(t *testing.T, tool gage.Tool, input string) gage.ToolResult {
	t.Helper()
	res, err := tool.Execute(context.Background(), json.RawMessage(input))
	if err != nil {
		t.Fatal(err)
	}
	return res
}
