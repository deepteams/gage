package mcp

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/deepteams/gage"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestPrompts(t *testing.T) {
	ctx := context.Background()
	png := []byte{0x89, 'P', 'N', 'G', 7, 8}

	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "prompts", Version: "1.0"}, nil)
	server.AddPrompt(
		&mcpsdk.Prompt{
			Name:        "greet",
			Description: "greets someone",
			Arguments:   []*mcpsdk.PromptArgument{{Name: "who", Description: "target", Required: true}},
		},
		func(ctx context.Context, req *mcpsdk.GetPromptRequest) (*mcpsdk.GetPromptResult, error) {
			return &mcpsdk.GetPromptResult{Messages: []*mcpsdk.PromptMessage{
				{Role: "user", Content: &mcpsdk.TextContent{Text: "Say hi to " + req.Params.Arguments["who"]}},
				{Role: "assistant", Content: &mcpsdk.ImageContent{Data: png, MIMEType: "image/png"}},
			}}, nil
		})

	clientT, serverT := mcpsdk.NewInMemoryTransports()
	if _, err := server.Connect(ctx, serverT, nil); err != nil {
		t.Fatal(err)
	}
	c, err := connect(ctx, "prompts", clientT)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	prompts, err := c.Prompts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(prompts) != 1 {
		t.Fatalf("expected 1 prompt, got %d", len(prompts))
	}
	p := prompts[0]
	if p.Name != "greet" || p.Description != "greets someone" {
		t.Fatalf("prompt = %+v", p)
	}
	if len(p.Arguments) != 1 || p.Arguments[0].Name != "who" || !p.Arguments[0].Required {
		t.Fatalf("arguments = %+v", p.Arguments)
	}

	msgs, err := c.GetPrompt(ctx, "greet", map[string]string{"who": "Ada"})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d: %+v", len(msgs), msgs)
	}
	if msgs[0].Role != gage.RoleUser || msgs[0].Text() != "Say hi to Ada" {
		t.Fatalf("message 0 = %+v", msgs[0])
	}
	if msgs[1].Role != gage.RoleAssistant {
		t.Fatalf("message 1 role = %q", msgs[1].Role)
	}
	if len(msgs[1].Content) != 1 || msgs[1].Content[0].Kind != gage.PartImage {
		t.Fatalf("message 1 content = %+v", msgs[1].Content)
	}
	img := msgs[1].Content[0].Image
	if img == nil || img.MediaType != "image/png" || img.Data != base64.StdEncoding.EncodeToString(png) {
		t.Fatalf("image = %+v", img)
	}
}
