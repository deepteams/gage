package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/deepteams/gage"
	"github.com/deepteams/gage/jsonschema"
)

// NewTools returns memory_remember, memory_recall, and memory_forget tools.
func NewTools(store gage.MemoryStore) []gage.Tool {
	return []gage.Tool{
		&rememberTool{store: store},
		&recallTool{store: store},
		&forgetTool{store: store},
	}
}

func metadataProp(desc string) jsonschema.Property {
	return jsonschema.Property{Type: "object", Description: desc}
}

type rememberTool struct {
	store gage.MemoryStore
}

func (t *rememberTool) Name() string { return "memory_remember" }
func (t *rememberTool) Description() string {
	return "Persist a durable fact, preference, decision, or note for future agent runs."
}
func (t *rememberTool) Schema() gage.JSONSchema {
	return jsonschema.Object(map[string]jsonschema.Property{
		"text":     jsonschema.Str("The memory text to store."),
		"metadata": metadataProp("Optional string metadata to store with the memory."),
	}, "text")
}
func (t *rememberTool) Metadata() gage.ToolMetadata {
	return gage.ToolMetadata{Destructive: true, RequiresApproval: true, Tags: []string{"memory", "write"}}
}
func (t *rememberTool) DescribeCall(input json.RawMessage) string {
	var args struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(input, &args) == nil && args.Text != "" {
		return fmt.Sprintf("remember %q", summarize(args.Text, 80))
	}
	return ""
}
func (t *rememberTool) Execute(ctx context.Context, input json.RawMessage) (gage.ToolResult, error) {
	if t.store == nil {
		return gage.ErrorResult("", "memory store is nil"), nil
	}
	var args struct {
		Text     string            `json:"text"`
		Metadata map[string]string `json:"metadata"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return gage.ToolResult{}, err
	}
	m, err := t.store.Remember(ctx, gage.Memory{Text: args.Text, Metadata: args.Metadata})
	if err != nil {
		return gage.ErrorResult("", err.Error()), nil
	}
	return gage.TextResult("", "remembered "+m.ID), nil
}

type recallTool struct {
	store gage.MemoryStore
}

func (t *recallTool) Name() string { return "memory_recall" }
func (t *recallTool) Description() string {
	return "Recall durable memories relevant to a query, optionally filtered by metadata."
}
func (t *recallTool) Schema() gage.JSONSchema {
	return jsonschema.Object(map[string]jsonschema.Property{
		"query":    jsonschema.Str("Search query. Leave empty to list recent memories."),
		"limit":    jsonschema.Int("Maximum number of memories to return (default 10)."),
		"metadata": metadataProp("Optional exact-match metadata filters."),
	})
}
func (t *recallTool) Metadata() gage.ToolMetadata {
	return gage.ToolMetadata{ReadOnly: true, Tags: []string{"memory", "read"}}
}
func (t *recallTool) DescribeCall(input json.RawMessage) string {
	var args struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if json.Unmarshal(input, &args) == nil {
		if args.Limit > 0 {
			return fmt.Sprintf("recall memories for %q (limit %d)", args.Query, args.Limit)
		}
		return fmt.Sprintf("recall memories for %q", args.Query)
	}
	return ""
}
func (t *recallTool) Execute(ctx context.Context, input json.RawMessage) (gage.ToolResult, error) {
	if t.store == nil {
		return gage.ErrorResult("", "memory store is nil"), nil
	}
	var args struct {
		Query    string            `json:"query"`
		Limit    int               `json:"limit"`
		Metadata map[string]string `json:"metadata"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return gage.ToolResult{}, err
	}
	memories, err := t.store.Recall(ctx, gage.MemoryQuery{Query: args.Query, Limit: args.Limit, Metadata: args.Metadata})
	if err != nil {
		return gage.ErrorResult("", err.Error()), nil
	}
	if len(memories) == 0 {
		return gage.TextResult("", "no memories"), nil
	}
	data, err := json.MarshalIndent(memories, "", "  ")
	if err != nil {
		return gage.ToolResult{}, err
	}
	return gage.TextResult("", string(data)), nil
}

type forgetTool struct {
	store gage.MemoryStore
}

func (t *forgetTool) Name() string { return "memory_forget" }
func (t *forgetTool) Description() string {
	return "Delete one durable memory by id."
}
func (t *forgetTool) Schema() gage.JSONSchema {
	return jsonschema.Object(map[string]jsonschema.Property{
		"id": jsonschema.Str("Memory id to delete."),
	}, "id")
}
func (t *forgetTool) Metadata() gage.ToolMetadata {
	return gage.ToolMetadata{Destructive: true, RequiresApproval: true, Tags: []string{"memory", "delete"}}
}
func (t *forgetTool) DescribeCall(input json.RawMessage) string {
	var args struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(input, &args) == nil && args.ID != "" {
		return "forget memory " + args.ID
	}
	return ""
}
func (t *forgetTool) Execute(ctx context.Context, input json.RawMessage) (gage.ToolResult, error) {
	if t.store == nil {
		return gage.ErrorResult("", "memory store is nil"), nil
	}
	var args struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return gage.ToolResult{}, err
	}
	if err := t.store.Forget(ctx, args.ID); err != nil {
		return gage.ErrorResult("", err.Error()), nil
	}
	return gage.TextResult("", "forgot "+args.ID), nil
}

func summarize(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "...(truncated)"
}
