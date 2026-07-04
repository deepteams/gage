package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/deepteams/gage"
)

// runLoop drives the agentic loop, sending every event on out. It does not
// close out (the caller does).
func (a *Agent) runLoop(ctx context.Context, input []gage.Message, out chan<- gage.Event, runID string) {
	send := func(e gage.Event) bool {
		select {
		case out <- e:
			return true
		case <-ctx.Done():
			return false
		}
	}

	runStarted := time.Now()
	runErr := ""
	a.observe(ctx, Observation{
		Type:      ObservationRunStart,
		RunID:     runID,
		Agent:     a.cfg.Name,
		Provider:  a.cfg.Provider.Name(),
		StartedAt: runStarted,
	})
	defer func() {
		a.observe(ctx, Observation{
			Type:        ObservationRunEnd,
			RunID:       runID,
			Agent:       a.cfg.Name,
			Provider:    a.cfg.Provider.Name(),
			StartedAt:   runStarted,
			Duration:    time.Since(runStarted),
			IsError:     runErr != "",
			ErrorString: runErr,
		})
	}()

	conversation := append([]gage.Message(nil), input...)
	var schemas []gage.ToolSchema
	if a.cfg.Registry != nil {
		schemas = a.cfg.Registry.Schemas()
	}

	for turn := 0; turn < a.cfg.maxTurns(); turn++ {
		a.observe(ctx, Observation{
			Type:      ObservationTurnStart,
			RunID:     runID,
			Agent:     a.cfg.Name,
			Provider:  a.cfg.Provider.Name(),
			Turn:      turn,
			StartedAt: time.Now(),
		})
		req := gage.Request{
			Model:    a.cfg.Model,
			Messages: conversation,
			Tools:    schemas,
			System:   a.cfg.systemPrompt(),
			Options:  a.cfg.Options,
		}

		stream, err := a.cfg.Provider.Stream(ctx, req)
		if err != nil {
			runErr = err.Error()
			send(gage.ErrorEvent(err).WithTurn(turn))
			return
		}

		asst, streamErr := a.consume(ctx, turn, stream, send)
		if streamErr != nil {
			// consume already forwarded an error event or ctx was cancelled.
			runErr = streamErr.Error()
			return
		}
		conversation = append(conversation, asst.message())

		if len(asst.toolCalls) == 0 {
			send(gage.DoneEvent().WithTurn(turn))
			return
		}

		// Execute the requested tool calls and feed results back.
		for _, tc := range asst.toolCalls {
			if ctx.Err() != nil {
				runErr = ctx.Err().Error()
				return
			}
			result := a.execTool(ctx, runID, turn, tc)
			if !send(gage.ToolResultEvent(result).WithTurn(turn)) {
				if ctx.Err() != nil {
					runErr = ctx.Err().Error()
				}
				return
			}
			conversation = append(conversation, gage.ToolResultMessage(result))
		}
	}

	runErr = gage.ErrMaxTurns.Error()
	send(gage.ErrorEvent(gage.ErrMaxTurns).WithTurn(a.cfg.maxTurns()))
}

// assistantAccum reconstructs an assistant message from a provider stream.
type assistantAccum struct {
	text      string
	toolCalls []gage.ToolCall
}

func (acc assistantAccum) message() gage.Message {
	content := make([]gage.ContentPart, 0, 1+len(acc.toolCalls))
	if acc.text != "" {
		content = append(content, gage.TextPart(acc.text))
	}
	for _, tc := range acc.toolCalls {
		content = append(content, gage.ToolUsePart(tc))
	}
	return gage.Message{Role: gage.RoleAssistant, Content: content}
}

// consume relays provider events (tagged with turn) and accumulates the
// assistant message. It returns a non-nil error if the stream errored or ctx
// was cancelled, in which case an error event has already been forwarded (for
// stream errors) — the caller should stop.
func (a *Agent) consume(ctx context.Context, turn int, stream <-chan gage.Event, send func(gage.Event) bool) (assistantAccum, error) {
	var acc assistantAccum
	for ev := range stream {
		switch ev.Type {
		case gage.EventTextDelta:
			acc.text += ev.Text
		case gage.EventToolCallDone:
			if ev.ToolCall != nil {
				acc.toolCalls = append(acc.toolCalls, *ev.ToolCall)
			}
		case gage.EventError:
			send(ev.WithTurn(turn))
			return acc, errStreamFailed
		}
		if !send(ev.WithTurn(turn)) {
			return acc, ctx.Err()
		}
	}
	if ctx.Err() != nil {
		return acc, ctx.Err()
	}
	return acc, nil
}

// execTool runs a single tool call through the registry and optional approver,
// always returning a ToolResult (errors are reported to the model, not the
// caller).
func (a *Agent) execTool(ctx context.Context, runID string, turn int, tc gage.ToolCall) (res gage.ToolResult) {
	started := time.Now()
	a.observe(ctx, Observation{
		Type:      ObservationToolStart,
		RunID:     runID,
		Agent:     a.cfg.Name,
		Provider:  a.cfg.Provider.Name(),
		Turn:      turn,
		Tool:      tc.Name,
		CallID:    tc.ID,
		StartedAt: started,
	})
	defer func() {
		if r := recover(); r != nil {
			res = gage.ErrorResult(tc.ID, fmt.Sprintf("tool panic: %v", r))
		}
		if res.CallID == "" {
			res.CallID = tc.ID
		}
		obs := Observation{
			Type:      ObservationToolEnd,
			RunID:     runID,
			Agent:     a.cfg.Name,
			Provider:  a.cfg.Provider.Name(),
			Turn:      turn,
			Tool:      tc.Name,
			CallID:    tc.ID,
			StartedAt: started,
			Duration:  time.Since(started),
			IsError:   res.IsError,
		}
		if res.IsError {
			obs.ErrorString = res.Text()
		}
		a.observe(ctx, obs)
	}()

	if a.cfg.Registry == nil {
		return gage.ErrorResult(tc.ID, "no tools available")
	}
	tool, ok := a.cfg.Registry.Get(tc.Name)
	if !ok {
		return gage.ErrorResult(tc.ID, "unknown tool: "+tc.Name)
	}
	if a.cfg.Approver != nil {
		decision, err := a.cfg.Approver.Approve(ctx, gage.PermissionRequest{
			Tool:     tc.Name,
			Input:    tc.Input,
			Agent:    a.cfg.Name,
			RunID:    runID,
			Turn:     turn,
			Metadata: gage.MetadataOf(tool),
			Summary:  gage.CallSummaryOf(tool, tc.Input),
		})
		if err != nil {
			return gage.ErrorResult(tc.ID, "permission check failed: "+err.Error())
		}
		if decision == gage.Deny {
			return gage.ErrorResult(tc.ID, "permission denied for tool "+tc.Name)
		}
	}

	input := tc.Input
	if len(input) == 0 {
		input = json.RawMessage("{}")
	}
	out := a.callTool(ctx, tool, input, tc.Name)
	if out.panicValue != nil {
		return gage.ErrorResult(tc.ID, fmt.Sprintf("tool panic: %v", out.panicValue))
	}
	if out.err != nil {
		return gage.ErrorResult(tc.ID, out.err.Error())
	}
	// Ensure the result is correlated with the originating call id.
	out.result.CallID = tc.ID
	return out.result
}

type toolOutcome struct {
	result     gage.ToolResult
	err        error
	panicValue any
}

func (a *Agent) callTool(ctx context.Context, tool gage.Tool, input json.RawMessage, toolName string) toolOutcome {
	timeout := a.cfg.ToolTimeout
	if timeout <= 0 {
		return callToolDirect(ctx, tool, input)
	}
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	done := make(chan toolOutcome, 1)
	go func() {
		done <- callToolDirect(execCtx, tool, input)
	}()
	select {
	case out := <-done:
		return out
	case <-execCtx.Done():
		if execCtx.Err() == context.DeadlineExceeded {
			return toolOutcome{result: gage.ErrorResult("", fmt.Sprintf("tool %s timed out after %s", toolName, timeout))}
		}
		return toolOutcome{result: gage.ErrorResult("", execCtx.Err().Error())}
	}
}

func callToolDirect(ctx context.Context, tool gage.Tool, input json.RawMessage) (out toolOutcome) {
	defer func() {
		if r := recover(); r != nil {
			out.panicValue = r
		}
	}()
	out.result, out.err = tool.Execute(ctx, input)
	return out
}

func (a *Agent) observe(ctx context.Context, obs Observation) {
	if a.cfg.Observer == nil {
		return
	}
	defer func() {
		_ = recover()
	}()
	a.cfg.Observer.Observe(ctx, obs)
}

// errStreamFailed marks a provider stream that ended with an error event.
var errStreamFailed = &loopError{"stream failed"}

type loopError struct{ msg string }

func (e *loopError) Error() string { return e.msg }
