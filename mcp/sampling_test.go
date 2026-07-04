package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/deepteams/gage"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeProvider is a gage.Provider that records the request and streams a
// canned two-chunk answer.
type fakeProvider struct {
	lastReq gage.Request
}

func (p *fakeProvider) Name() string { return "fake-model" }

func (p *fakeProvider) Stream(ctx context.Context, req gage.Request) (<-chan gage.Event, error) {
	p.lastReq = req
	ch := make(chan gage.Event, 8)
	ch <- gage.MessageStart()
	ch <- gage.TextDelta("Hello ")
	ch <- gage.TextDelta("world")
	ch <- gage.MessageDone(gage.StopEndTurn)
	close(ch)
	return ch, nil
}

func TestSamplingRoundTrip(t *testing.T) {
	ctx := context.Background()

	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "srv", Version: "1.0"}, nil)
	clientT, serverT := mcpsdk.NewInMemoryTransports()
	ss, err := server.Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatal(err)
	}

	provider := &fakeProvider{}
	c, err := connect(ctx, "srv", clientT, WithSamplingProvider(provider))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	res, err := ss.CreateMessage(ctx, &mcpsdk.CreateMessageParams{
		SystemPrompt: "be brief",
		MaxTokens:    128,
		Temperature:  0.7,
		Messages: []*mcpsdk.SamplingMessage{
			{Role: "user", Content: &mcpsdk.TextContent{Text: "greet the world"}},
			{Role: "assistant", Content: &mcpsdk.TextContent{Text: "sure —"}},
			{Role: "user", Content: &mcpsdk.TextContent{Text: "go on"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if res.Role != "assistant" {
		t.Fatalf("role = %q", res.Role)
	}
	tc, ok := res.Content.(*mcpsdk.TextContent)
	if !ok || tc.Text != "Hello world" {
		t.Fatalf("content = %#v", res.Content)
	}
	if res.Model != "fake-model" {
		t.Fatalf("model = %q", res.Model)
	}
	if res.StopReason != "endTurn" {
		t.Fatalf("stop reason = %q", res.StopReason)
	}

	// The MCP request was mapped onto the gage.Request faithfully.
	req := provider.lastReq
	if req.System != "be brief" {
		t.Fatalf("system = %q", req.System)
	}
	if req.Options.MaxTokens != 128 {
		t.Fatalf("max tokens = %d", req.Options.MaxTokens)
	}
	if req.Options.Temperature == nil || *req.Options.Temperature != 0.7 {
		t.Fatalf("temperature = %v", req.Options.Temperature)
	}
	if len(req.Messages) != 3 {
		t.Fatalf("messages = %+v", req.Messages)
	}
	if req.Messages[0].Role != gage.RoleUser || req.Messages[0].Text() != "greet the world" {
		t.Fatalf("message 0 = %+v", req.Messages[0])
	}
	if req.Messages[1].Role != gage.RoleAssistant || req.Messages[1].Text() != "sure —" {
		t.Fatalf("message 1 = %+v", req.Messages[1])
	}
}

func TestSamplingRejectsNonTextContent(t *testing.T) {
	ctx := context.Background()

	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "srv", Version: "1.0"}, nil)
	clientT, serverT := mcpsdk.NewInMemoryTransports()
	ss, err := server.Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatal(err)
	}

	c, err := connect(ctx, "srv", clientT, WithSamplingProvider(&fakeProvider{}))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	_, err = ss.CreateMessage(ctx, &mcpsdk.CreateMessageParams{
		MaxTokens: 16,
		Messages: []*mcpsdk.SamplingMessage{
			{Role: "user", Content: &mcpsdk.ImageContent{Data: []byte{1, 2, 3}, MIMEType: "image/png"}},
		},
	})
	if err == nil {
		t.Fatal("expected an error for image content")
	}
	if !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("error = %v", err)
	}
}
