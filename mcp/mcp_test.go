package mcp

import (
	"context"
	"encoding/json"
	"net/http"
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
	reg := &fakeRegistry{tools: map[string]gage.Tool{}}
	if err := c.Register(ctx, reg); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.tools["calc__add"]; !ok {
		t.Fatal("tool not registered")
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

type fakeRegistry struct{ tools map[string]gage.Tool }

func (r *fakeRegistry) Register(t gage.Tool) error { r.tools[t.Name()] = t; return nil }
func (r *fakeRegistry) Get(n string) (gage.Tool, bool) {
	t, ok := r.tools[n]
	return t, ok
}
func (r *fakeRegistry) List() []gage.Tool          { return nil }
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
