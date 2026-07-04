package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/deepteams/gage"
)

// runLoop drives the agentic loop, sending every event on out. It does not
// close out (the caller does). ctx bounds model/tool execution; sendCtx bounds
// delivery to the consumer, so an agent-owned timeout can still be reported as
// a terminal error event before the stream closes.
func (a *Agent) runLoop(ctx, sendCtx context.Context, input []gage.Message, out chan<- gage.Event, runID string) {
	send := func(e gage.Event) bool {
		select {
		case out <- e:
			return true
		case <-sendCtx.Done():
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

	var runUsage gage.Usage
	var lastText string
	var lastStop gage.StopReason

	fail := func(turn int, err error) {
		runErr = err.Error()
		send(gage.ErrorEvent(err).WithTurn(turn))
	}

	for turn := 0; turn < a.cfg.maxTurns(); turn++ {
		turnStarted := time.Now()
		a.observe(ctx, Observation{
			Type:      ObservationTurnStart,
			RunID:     runID,
			Agent:     a.cfg.Name,
			Provider:  a.cfg.Provider.Name(),
			Turn:      turn,
			StartedAt: turnStarted,
		})
		req := gage.Request{
			Model:    a.cfg.Model,
			Messages: conversation,
			Tools:    schemas,
			System:   a.cfg.systemPrompt(),
			Options:  a.cfg.Options,
		}
		if a.cfg.Hooks.PrepareRequest != nil {
			if err := a.cfg.Hooks.PrepareRequest(ctx, &req); err != nil {
				fail(turn, fmt.Errorf("prepare request hook: %w", err))
				return
			}
		}

		stream, err := a.cfg.Provider.Stream(ctx, req)
		if err != nil {
			fail(turn, err)
			return
		}

		asst, streamErr := a.consume(ctx, turn, stream, send)
		if streamErr != nil {
			// consume already forwarded an error event or ctx was cancelled.
			if ctx.Err() != nil {
				fail(turn, ctx.Err())
				return
			}
			runErr = streamErr.Error()
			return
		}
		runUsage = runUsage.Add(asst.usage)
		lastStop = asst.stopReason
		if t := asst.text(); t != "" {
			lastText = t
		}
		a.observe(ctx, Observation{
			Type:      ObservationTurnEnd,
			RunID:     runID,
			Agent:     a.cfg.Name,
			Provider:  a.cfg.Provider.Name(),
			Turn:      turn,
			StartedAt: turnStarted,
			Duration:  time.Since(turnStarted),
			Usage:     asst.usage,
		})

		if msg, ok := asst.message(); ok {
			conversation = append(conversation, msg)
		}

		if len(asst.toolCalls) == 0 {
			res := &gage.Result{
				Messages:   conversation,
				Text:       lastText,
				StopReason: lastStop,
				Usage:      runUsage,
				Turns:      turn + 1,
			}
			send(gage.DoneEvent(res).WithTurn(turn))
			return
		}

		// Execute the requested tool calls and feed results back.
		results, ok := a.execTools(ctx, runID, turn, asst.toolCalls, send)
		if !ok {
			if ctx.Err() != nil {
				fail(turn, ctx.Err())
			}
			return
		}
		for _, result := range results {
			conversation = append(conversation, gage.ToolResultMessage(result))
		}

		// Compact the conversation when the input side of the context window
		// crosses the configured threshold.
		if a.cfg.Compactor != nil && a.cfg.CompactAfter > 0 && asst.usage.InputTokens >= a.cfg.CompactAfter {
			compacted, err := a.cfg.Compactor.Compact(ctx, conversation, asst.usage)
			if err != nil {
				fail(turn, fmt.Errorf("compaction: %w", err))
				return
			}
			conversation = compacted
		}
	}

	runErr = gage.ErrMaxTurns.Error()
	send(gage.ErrorEvent(gage.ErrMaxTurns).WithTurn(a.cfg.maxTurns()))
}

// assistantAccum reconstructs an assistant message from a provider stream,
// preserving the order of reasoning, text, and tool-use blocks.
type assistantAccum struct {
	parts      []gage.ContentPart
	openKind   gage.PartKind // PartText or PartReasoning while a run of deltas is open
	openText   string
	toolCalls  []gage.ToolCall
	usage      gage.Usage
	stopReason gage.StopReason
}

// flush closes the currently open delta run into a part.
func (acc *assistantAccum) flush(signature string) {
	if acc.openText == "" && signature == "" {
		acc.openKind = ""
		return
	}
	switch acc.openKind {
	case gage.PartReasoning:
		acc.parts = append(acc.parts, gage.SignedReasoningPart(acc.openText, signature))
	case gage.PartText:
		acc.parts = append(acc.parts, gage.TextPart(acc.openText))
	}
	acc.openKind = ""
	acc.openText = ""
}

// delta appends streamed text of the given kind, flushing when the kind changes.
func (acc *assistantAccum) delta(kind gage.PartKind, s string) {
	if acc.openKind != kind {
		acc.flush("")
		acc.openKind = kind
	}
	acc.openText += s
}

func (acc *assistantAccum) text() string {
	var s string
	for _, p := range acc.parts {
		if p.Kind == gage.PartText {
			s += p.Text
		}
	}
	if acc.openKind == gage.PartText {
		s += acc.openText
	}
	return s
}

// message returns the reconstructed assistant message. ok is false when the
// stream produced no content at all (some providers reject empty assistant
// messages, so the loop skips them).
func (acc *assistantAccum) message() (gage.Message, bool) {
	acc.flush("")
	content := append([]gage.ContentPart(nil), acc.parts...)
	if len(content) == 0 {
		return gage.Message{}, false
	}
	return gage.Message{Role: gage.RoleAssistant, Content: content}, true
}

// consume relays provider events (tagged with turn) and accumulates the
// assistant message. It returns a non-nil error if the stream errored or ctx
// was cancelled, in which case an error event has already been forwarded (for
// stream errors) — the caller should stop.
func (a *Agent) consume(ctx context.Context, turn int, stream <-chan gage.Event, send func(gage.Event) bool) (*assistantAccum, error) {
	acc := &assistantAccum{}
	for ev := range stream {
		switch ev.Type {
		case gage.EventTextDelta:
			acc.delta(gage.PartText, ev.Text)
		case gage.EventReasoningDelta:
			acc.delta(gage.PartReasoning, ev.Text)
		case gage.EventReasoningDone:
			// Close the reasoning block, attaching the provider's replay
			// signature so the block survives the round-trip.
			if acc.openKind != gage.PartReasoning {
				acc.flush("")
				acc.openKind = gage.PartReasoning
			}
			acc.flush(ev.Signature)
		case gage.EventToolCallDone:
			if ev.ToolCall != nil {
				acc.flush("")
				acc.toolCalls = append(acc.toolCalls, *ev.ToolCall)
				acc.parts = append(acc.parts, gage.ToolUsePart(*ev.ToolCall))
			}
		case gage.EventUsage:
			if ev.Usage != nil {
				acc.usage = *ev.Usage
			}
		case gage.EventMessageDone:
			acc.stopReason = ev.StopReason
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

// execTools runs a turn's tool calls — sequentially by default, concurrently
// when Config.MaxParallelTools > 1 — emitting each tool_result event as it
// completes. The returned slice is ordered like calls so the conversation
// stays deterministic. ok is false when ctx was cancelled mid-way.
func (a *Agent) execTools(ctx context.Context, runID string, turn int, calls []gage.ToolCall, send func(gage.Event) bool) ([]gage.ToolResult, bool) {
	parallel := a.cfg.MaxParallelTools
	if parallel <= 1 || len(calls) == 1 {
		results := make([]gage.ToolResult, 0, len(calls))
		for _, tc := range calls {
			if ctx.Err() != nil {
				return nil, false
			}
			result := a.execTool(ctx, runID, turn, tc)
			if !send(gage.ToolResultEvent(result).WithTurn(turn)) {
				return nil, false
			}
			results = append(results, result)
		}
		return results, true
	}

	results := make([]gage.ToolResult, len(calls))
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	var sendMu sync.Mutex
	cancelled := false
	for i, tc := range calls {
		wg.Add(1)
		go func(i int, tc gage.ToolCall) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			result := a.execTool(ctx, runID, turn, tc)
			results[i] = result
			sendMu.Lock()
			defer sendMu.Unlock()
			if !send(gage.ToolResultEvent(result).WithTurn(turn)) {
				cancelled = true
			}
		}(i, tc)
	}
	wg.Wait()
	if cancelled || ctx.Err() != nil {
		return nil, false
	}
	return results, true
}

// execTool runs a single tool call through the hooks, registry and optional
// approver, always returning a ToolResult (errors are reported to the model,
// not the caller).
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
		if a.cfg.Hooks.PostToolUse != nil {
			res = a.cfg.Hooks.PostToolUse(ctx, tc, res)
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
	if a.cfg.Hooks.PreToolUse != nil {
		updated, err := a.cfg.Hooks.PreToolUse(ctx, tc)
		if err != nil {
			return gage.ErrorResult(tc.ID, "blocked by pre-tool hook: "+err.Error())
		}
		updated.ID, updated.Name = tc.ID, tc.Name // hooks may only rewrite the input
		tc = updated
	}
	if a.cfg.Approver != nil {
		approval, err := a.cfg.Approver.Approve(ctx, gage.PermissionRequest{
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
		if !approval.Allow {
			msg := "permission denied for tool " + tc.Name
			if approval.Reason != "" {
				msg += ": " + approval.Reason
			}
			return gage.ErrorResult(tc.ID, msg)
		}
		if approval.UpdatedInput != nil {
			tc.Input = approval.UpdatedInput
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
