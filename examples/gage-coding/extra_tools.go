package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/deepteams/gage"
	"github.com/deepteams/gage/jsonschema"
	"github.com/deepteams/gage/tools"
)

type questionAsker interface {
	AskQuestion(ctx context.Context, question string) (string, error)
}

func newQuestionTool(asker questionAsker) gage.Tool {
	return tools.FuncWithMetadata("question",
		"Ask the user a concise clarification question during the run and return their answer.",
		jsonschema.Object(map[string]jsonschema.Property{
			"question": jsonschema.Str("The question to ask the user."),
		}, "question"),
		gage.ToolMetadata{ReadOnly: true, LongRunning: true, Tags: []string{"question"}},
		func(ctx context.Context, input json.RawMessage) (gage.ToolResult, error) {
			var args struct {
				Question string `json:"question"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return gage.ToolResult{}, err
			}
			if strings.TrimSpace(args.Question) == "" {
				return gage.ErrorResult("", "question is required"), nil
			}
			if asker == nil {
				return gage.ErrorResult("", "no interactive question handler is available"), nil
			}
			answer, err := asker.AskQuestion(ctx, args.Question)
			if err != nil {
				return gage.ErrorResult("", err.Error()), nil
			}
			return gage.TextResult("", answer), nil
		})
}

type todoStore struct {
	mu    sync.Mutex
	items []todoItem
}

type todoItem struct {
	Content string `json:"content"`
	Status  string `json:"status"`
}

func newTodoStore() *todoStore {
	return &todoStore{}
}

func (s *todoStore) tools() []gage.Tool {
	write := tools.FuncWithMetadata("todowrite",
		"Replace the current task list. Use short items with status pending, in_progress, or completed.",
		jsonschema.Object(map[string]jsonschema.Property{
			"items": jsonschema.Array("The current task list.", jsonschema.ObjectProp(map[string]jsonschema.Property{
				"content": jsonschema.Str("Task description."),
				"status":  jsonschema.Enum("Task status.", "pending", "in_progress", "completed"),
			}, "content", "status")),
		}, "items"),
		gage.ToolMetadata{Tags: []string{"planning", "todo"}},
		func(ctx context.Context, input json.RawMessage) (gage.ToolResult, error) {
			var args struct {
				Items []todoItem `json:"items"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return gage.ToolResult{}, err
			}
			for _, item := range args.Items {
				switch item.Status {
				case "pending", "in_progress", "completed":
				default:
					return gage.ErrorResult("", fmt.Sprintf("invalid todo status %q", item.Status)), nil
				}
			}
			s.mu.Lock()
			s.items = append([]todoItem(nil), args.Items...)
			s.mu.Unlock()
			return gage.TextResult("", fmt.Sprintf("stored %d todo item(s)", len(args.Items))), nil
		})

	read := tools.FuncWithMetadata("todoread",
		"Read the current task list.",
		jsonschema.Object(nil),
		gage.ToolMetadata{ReadOnly: true, Tags: []string{"planning", "todo"}},
		func(ctx context.Context, input json.RawMessage) (gage.ToolResult, error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			if len(s.items) == 0 {
				return gage.TextResult("", "(no todo items)"), nil
			}
			var b strings.Builder
			for i, item := range s.items {
				fmt.Fprintf(&b, "%d. [%s] %s\n", i+1, item.Status, item.Content)
			}
			return gage.TextResult("", strings.TrimSpace(b.String())), nil
		})
	return []gage.Tool{write, read}
}
