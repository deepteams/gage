package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/deepteams/gage"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type addInput struct {
	A int `json:"a"`
	B int `json:"b"`
}

func TestMCPDiscoveryAndCall(t *testing.T) {
	ctx := context.Background()

	// Build an in-memory MCP server exposing an "add" tool.
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "calc", Version: "1.0"}, nil)
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "add", Description: "add two ints"},
		func(ctx context.Context, req *mcpsdk.CallToolRequest, in addInput) (*mcpsdk.CallToolResult, any, error) {
			return &mcpsdk.CallToolResult{
				Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: itoa(in.A + in.B)}},
			}, nil, nil
		})

	clientT, serverT := mcpsdk.NewInMemoryTransports()
	if _, err := server.Connect(ctx, serverT, nil); err != nil {
		t.Fatal(err)
	}

	c, err := connect(ctx, "calc", clientT)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	tools, err := c.Tools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	tool := tools[0]
	if tool.Name() != "calc__add" {
		t.Fatalf("tool name = %q", tool.Name())
	}

	res, err := tool.Execute(ctx, json.RawMessage(`{"a":2,"b":3}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || res.Text() != "5" {
		t.Fatalf("result = %+v", res)
	}

	// Registration into a gage registry works.
	reg := newFakeRegistry()
	if err := c.Register(ctx, reg); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Get("calc__add"); !ok {
		t.Fatal("tool not registered")
	}
}

func TestMCPToolNameNormalizationKeepsRawName(t *testing.T) {
	tool := adaptTool(nil, "my server", &mcpsdk.Tool{Name: "read/file", Description: "read"}).(*mcpTool)
	if tool.rawName != "read/file" {
		t.Fatalf("rawName = %q", tool.rawName)
	}
	if strings.ContainsAny(tool.Name(), " /") {
		t.Fatalf("tool name was not normalized: %q", tool.Name())
	}
	if len(tool.Name()) > 64 {
		t.Fatalf("tool name too long: %q (%d)", tool.Name(), len(tool.Name()))
	}
	if !strings.Contains(tool.Name(), "__") {
		t.Fatalf("tool name missing separator: %q", tool.Name())
	}
	if tool.Name() == "my server__read/file" {
		t.Fatalf("tool name still uses raw identifiers: %q", tool.Name())
	}
	unchanged := adaptTool(nil, "calc", &mcpsdk.Tool{Name: "add"}).Name()
	if unchanged != "calc__add" {
		t.Fatalf("safe names should stay readable, got %q", unchanged)
	}
	long := adaptTool(nil, strings.Repeat("s", 80), &mcpsdk.Tool{Name: strings.Repeat("t", 80)}).Name()
	if len(long) > 64 {
		t.Fatalf("long tool name was not capped: %q (%d)", long, len(long))
	}
}

func TestImageToolResult(t *testing.T) {
	ctx := context.Background()
	png := []byte{0x89, 'P', 'N', 'G', 1, 2, 3}

	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "pix", Version: "1.0"}, nil)
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "shot", Description: "screenshot"},
		func(ctx context.Context, req *mcpsdk.CallToolRequest, in struct{}) (*mcpsdk.CallToolResult, any, error) {
			return &mcpsdk.CallToolResult{
				Content: []mcpsdk.Content{
					&mcpsdk.TextContent{Text: "here you go"},
					&mcpsdk.ImageContent{Data: png, MIMEType: "image/png"},
				},
			}, nil, nil
		})

	clientT, serverT := mcpsdk.NewInMemoryTransports()
	if _, err := server.Connect(ctx, serverT, nil); err != nil {
		t.Fatal(err)
	}
	c, err := connect(ctx, "pix", clientT)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	tools, err := c.Tools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	res, err := tools[0].Execute(ctx, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %+v", res)
	}
	if len(res.Content) != 2 {
		t.Fatalf("expected 2 parts, got %d: %+v", len(res.Content), res.Content)
	}
	if res.Content[0].Kind != gage.PartText || res.Content[0].Text != "here you go" {
		t.Fatalf("part 0 = %+v", res.Content[0])
	}
	img := res.Content[1]
	if img.Kind != gage.PartImage || img.Image == nil {
		t.Fatalf("part 1 = %+v", img)
	}
	if img.Image.MediaType != "image/png" {
		t.Fatalf("media type = %q", img.Image.MediaType)
	}
	if img.Image.Data != base64.StdEncoding.EncodeToString(png) {
		t.Fatalf("image data = %q", img.Image.Data)
	}
}

func TestHeaderTransport(t *testing.T) {
	base := &recordingTransport{}
	client := httpClientWithHeaders(&http.Client{Transport: base}, BearerHeaders("tok"))
	req, _ := http.NewRequest("GET", "http://example.com", nil)
	client.Transport.RoundTrip(req)
	if base.lastAuth != "Bearer tok" {
		t.Fatalf("auth header = %q", base.lastAuth)
	}
}

type recordingTransport struct{ lastAuth string }

func (r *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.lastAuth = req.Header.Get("Authorization")
	return &http.Response{StatusCode: 200, Body: http.NoBody, Header: make(http.Header)}, nil
}

// fakeRegistry is a minimal, concurrency-safe gage.ToolRegistry for tests. It
// must be safe because tool-sync reconciliation runs on notification handler
// goroutines.
type fakeRegistry struct {
	mu    sync.Mutex
	tools map[string]gage.Tool
}

func newFakeRegistry() *fakeRegistry {
	return &fakeRegistry{tools: map[string]gage.Tool{}}
}

func (r *fakeRegistry) Register(t gage.Tool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.tools[t.Name()]; ok {
		return fmt.Errorf("duplicate tool %s", t.Name())
	}
	r.tools[t.Name()] = t
	return nil
}

func (r *fakeRegistry) Unregister(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.tools[name]
	delete(r.tools, name)
	return ok
}

func (r *fakeRegistry) Get(n string) (gage.Tool, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.tools[n]
	return t, ok
}

func (r *fakeRegistry) List() []gage.Tool {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]gage.Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	return out
}

func (r *fakeRegistry) Schemas() []gage.ToolSchema { return nil }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
