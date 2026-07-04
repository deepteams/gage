package mcp

import (
	"context"
	"fmt"

	"github.com/deepteams/gage"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Prompt describes a prompt (or prompt template) exposed by an MCP server.
type Prompt struct {
	// Name identifies the prompt; pass it to GetPrompt.
	Name string
	// Title is an optional human-readable display name.
	Title string
	// Description says what the prompt provides.
	Description string
	// Arguments are the template's parameters, if any.
	Arguments []PromptArgument
}

// PromptArgument describes one templating argument of a Prompt.
type PromptArgument struct {
	Name        string
	Title       string
	Description string
	Required    bool
}

// Prompts lists the server's prompts. Pagination is followed to completion.
func (c *Client) Prompts(ctx context.Context) ([]Prompt, error) {
	var out []Prompt
	var cursor string
	for {
		res, err := c.session.ListPrompts(ctx, &mcpsdk.ListPromptsParams{Cursor: cursor})
		if err != nil {
			return nil, fmt.Errorf("mcp: list prompts %s: %w", c.name, err)
		}
		for _, p := range res.Prompts {
			prompt := Prompt{Name: p.Name, Title: p.Title, Description: p.Description}
			for _, a := range p.Arguments {
				prompt.Arguments = append(prompt.Arguments, PromptArgument{
					Name:        a.Name,
					Title:       a.Title,
					Description: a.Description,
					Required:    a.Required,
				})
			}
			out = append(out, prompt)
		}
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	return out, nil
}

// GetPrompt expands the named prompt with args and maps the resulting MCP
// prompt messages onto gage messages: user/assistant roles are preserved, text
// content becomes text parts and image content becomes image parts.
func (c *Client) GetPrompt(ctx context.Context, name string, args map[string]string) ([]gage.Message, error) {
	res, err := c.session.GetPrompt(ctx, &mcpsdk.GetPromptParams{Name: name, Arguments: args})
	if err != nil {
		return nil, fmt.Errorf("mcp: get prompt %s %s: %w", c.name, name, err)
	}
	out := make([]gage.Message, 0, len(res.Messages))
	for _, m := range res.Messages {
		role := gage.RoleUser
		if m.Role == "assistant" {
			role = gage.RoleAssistant
		}
		out = append(out, gage.Message{
			Role:    role,
			Content: []gage.ContentPart{contentPart(m.Content)},
		})
	}
	return out, nil
}
