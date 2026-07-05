package gagetest_test

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/deepteams/gage"
	"github.com/deepteams/gage/agent"
	"github.com/deepteams/gage/gagetest"
	"github.com/deepteams/gage/jsonschema"
	"github.com/deepteams/gage/tools"
)

// Example runs a real agent loop against the scripted provider: the first
// scripted turn requests a tool call, the agent executes the tool and feeds
// the result back, and the second scripted turn gives the final answer.
func Example() {
	p := gagetest.NewProvider("")
	p.Enqueue(
		gagetest.Calls(gagetest.Call("c1", "weather", map[string]any{"city": "Paris"})).
			WithText("checking..."),
		gagetest.Text("It is sunny in Paris."),
	)

	executed := false
	weather := tools.Func("weather", "Report the weather for a city",
		jsonschema.Object(map[string]jsonschema.Property{
			"city": jsonschema.Str("City name"),
		}, "city"),
		func(ctx context.Context, input json.RawMessage) (gage.ToolResult, error) {
			executed = true
			var args struct {
				City string `json:"city"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return gage.ErrorResult("", err.Error()), nil
			}
			return gage.TextResult("", "22C and sunny in "+args.City), nil
		})

	reg := tools.NewRegistry()
	reg.MustRegister(weather)

	ag, err := agent.New(agent.Config{Provider: p, Registry: reg})
	if err != nil {
		fmt.Println("new agent:", err)
		return
	}
	res, err := ag.RunSync(context.Background(), []gage.Message{gage.UserText("weather in Paris?")})
	if err != nil {
		fmt.Println("run:", err)
		return
	}

	fmt.Println(res.Text)
	fmt.Println("tool executed:", executed)
	fmt.Println("turns:", res.Turns)
	fmt.Println("script consumed:", p.Remaining() == 0)
	// Output:
	// It is sunny in Paris.
	// tool executed: true
	// turns: 2
	// script consumed: true
}
