package openai

import (
	"encoding/json"

	"github.com/deepteams/gage"
)

// toolAccumulator reassembles streamed tool calls from Chat Completions deltas.
// Each delta references a call by Index and contributes an id, name and/or a
// fragment of the arguments JSON. The accumulator emits a ToolCallStart when a
// call is first seen, ToolCallDelta as arguments grow, and ToolCallDone (via
// finish) once the stream ends.
type toolAccumulator struct {
	order []int
	calls map[int]*accCall
}

type accCall struct {
	id      string
	name    string
	args    []byte
	started bool
}

func newToolAccumulator() *toolAccumulator {
	return &toolAccumulator{calls: map[int]*accCall{}}
}

func (a *toolAccumulator) apply(tc chatToolCall) []gage.Event {
	c, ok := a.calls[tc.Index]
	if !ok {
		c = &accCall{}
		a.calls[tc.Index] = c
		a.order = append(a.order, tc.Index)
	}
	if tc.ID != "" {
		c.id = tc.ID
	}
	if tc.Function.Name != "" {
		c.name = tc.Function.Name
	}

	var events []gage.Event
	// Emit the start once we know the call's name.
	if !c.started && c.name != "" {
		c.started = true
		events = append(events, gage.ToolCallStart(gage.ToolCall{ID: c.id, Name: c.name}))
	}
	if tc.Function.Arguments != "" {
		c.args = append(c.args, tc.Function.Arguments...)
		if c.started {
			events = append(events, gage.ToolCallDelta(gage.ToolCall{
				ID:    c.id,
				Name:  c.name,
				Input: json.RawMessage(append([]byte(nil), c.args...)),
			}))
		}
	}
	return events
}

// finish emits a ToolCallDone for every accumulated call, in arrival order.
func (a *toolAccumulator) finish() []gage.Event {
	var events []gage.Event
	for _, idx := range a.order {
		c := a.calls[idx]
		if !c.started && c.name == "" {
			continue
		}
		input := c.args
		if len(input) == 0 {
			input = []byte("{}")
		}
		events = append(events, gage.ToolCallDone(gage.ToolCall{
			ID:    c.id,
			Name:  c.name,
			Input: json.RawMessage(append([]byte(nil), input...)),
		}))
	}
	return events
}
