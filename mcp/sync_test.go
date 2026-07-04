package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/deepteams/gage"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestToolSyncOnListChanged(t *testing.T) {
	ctx := context.Background()

	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "calc", Version: "1.0"}, nil)
	addServerTool(server, "add", "add two ints")

	clientT, serverT := mcpsdk.NewInMemoryTransports()
	if _, err := server.Connect(ctx, serverT, nil); err != nil {
		t.Fatal(err)
	}

	reg := newFakeRegistry()
	// A foreign tool must survive reconciliation untouched.
	foreign := gage.ToolFunc{ToolName: "other__tool", Params: json.RawMessage(`{"type":"object"}`)}
	if err := reg.Register(foreign); err != nil {
		t.Fatal(err)
	}

	c, err := connect(ctx, "calc", clientT, WithToolSync(reg))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// Initial registration.
	if err := c.Register(ctx, reg); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Get("calc__add"); !ok {
		t.Fatal("calc__add not registered")
	}

	// Server-side add triggers a notification; the new tool shows up.
	addServerTool(server, "mul", "multiply two ints")
	waitFor(t, "calc__mul registered", func() bool {
		_, ok := reg.Get("calc__mul")
		return ok
	})

	// Server-side removal unregisters the vanished tool.
	server.RemoveTools("add")
	waitFor(t, "calc__add unregistered", func() bool {
		_, ok := reg.Get("calc__add")
		return !ok
	})

	// Server-side replacement (same name, new description) swaps the tool.
	addServerTool(server, "mul", "multiply two ints, but better")
	waitFor(t, "calc__mul replaced", func() bool {
		tool, ok := reg.Get("calc__mul")
		return ok && strings.Contains(tool.Description(), "better")
	})

	if _, ok := reg.Get("other__tool"); !ok {
		t.Fatal("foreign tool was removed by reconciliation")
	}

	// Manual sync also works and is idempotent.
	if err := c.SyncTools(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Get("calc__mul"); !ok {
		t.Fatal("calc__mul lost after manual sync")
	}
}

func TestSyncToolsWithoutRegistry(t *testing.T) {
	ctx := context.Background()
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "calc", Version: "1.0"}, nil)
	clientT, serverT := mcpsdk.NewInMemoryTransports()
	if _, err := server.Connect(ctx, serverT, nil); err != nil {
		t.Fatal(err)
	}
	c, err := connect(ctx, "calc", clientT)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.SyncTools(ctx); err == nil {
		t.Fatal("expected an error without WithToolSync")
	}
}

func addServerTool(server *mcpsdk.Server, name, desc string) {
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: name, Description: desc},
		func(ctx context.Context, req *mcpsdk.CallToolRequest, in addInput) (*mcpsdk.CallToolResult, any, error) {
			return &mcpsdk.CallToolResult{
				Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "ok"}},
			}, nil, nil
		})
}

// waitFor polls cond until it holds or the deadline passes; notifications are
// delivered asynchronously, so registry state converges rather than switches.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
