package gage_test

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/deepteams/gage"
	"github.com/deepteams/gage/agent"
	"github.com/deepteams/gage/tools"
)

// echoProvider is a minimal Provider that asks to call the "shout" tool once,
// then reports the tool's result. It stands in for a real model backend so the
// example runs offline.
type echoProvider struct{ calls int }

func (p *echoProvider) Name() string { return "example" }

func (p *echoProvider) Stream(ctx context.Context, req gage.Request) (<-chan gage.Event, error) {
	ch := make(chan gage.Event)
	turn := p.calls
	p.calls++
	go func() {
		defer close(ch)
		ch <- gage.MessageStart()
		if turn == 0 {
			ch <- gage.ToolCallDone(gage.ToolCall{ID: "1", Name: "shout", Input: json.RawMessage(`{"text":"hi"}`)})
			ch <- gage.MessageDone("tool_use")
			return
		}
		// Second turn: echo the last tool result back as the final answer.
		last := req.Messages[len(req.Messages)-1]
		var toolText string
		for _, p := range last.Content {
			if p.Kind == gage.PartToolResult && p.ToolResult != nil {
				toolText = p.ToolResult.Text()
			}
		}
		ch <- gage.TextDelta("final: " + toolText)
		ch <- gage.MessageDone("end_turn")
	}()
	return ch, nil
}

// Example wires a provider, a tool registry and an agent, then drains the event
// stream — the canonical way to embed gage in another program.
func Example() {
	reg := tools.NewRegistry()
	reg.MustRegister(tools.ToolFuncMust("shout", "uppercase the text",
		func(ctx context.Context, input json.RawMessage) (gage.ToolResult, error) {
			var a struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(input, &a)
			return gage.TextResult("", "HELLO "+a.Text), nil
		}))

	ag, err := agent.New(agent.Config{
		Provider: &echoProvider{},
		Registry: reg,
		System:   "You are a helpful assistant.",
	})
	if err != nil {
		panic(err)
	}

	stream, err := ag.Run(context.Background(), []gage.Message{gage.UserText("shout hi")})
	if err != nil {
		panic(err)
	}

	for ev := range stream {
		switch ev.Type {
		case gage.EventToolResult:
			fmt.Println("tool:", ev.ToolResult.Text())
		case gage.EventTextDelta:
			fmt.Print(ev.Text)
		case gage.EventDone:
			fmt.Println()
		}
	}
	// Output:
	// tool: HELLO hi
	// final: HELLO hi
}
