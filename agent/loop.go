package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/deepteams/gage"
)

// resumeState carries what runLoop needs to finish a paused turn before
// re-entering the regular loop: the checkpoint and the caller's decisions for
// the pending tool calls.
type resumeState struct {
	cp        *gage.Checkpoint
	decisions map[string]gage.Approval
}

// runLoop drives the agentic loop, sending every event on out. It does not
// close out (the caller does). ctx bounds model/tool execution; sendCtx bounds
// delivery to the consumer, so an agent-owned timeout can still be reported as
// a terminal error event before the stream closes. When resume is non-nil the
// loop first completes the checkpointed turn's tool batch (possibly pausing
// again) and continues from the following turn.
func (a *Agent) runLoop(ctx, sendCtx context.Context, input []gage.Message, resume *resumeState, out chan<- gage.Event, runID string) {
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
	startTurn := 0
	var runUsage gage.Usage
	var lastText string
	var lastStop gage.StopReason

	fail := func(turn int, err error) {
		runErr = err.Error()
		send(gage.ErrorEvent(err).WithTurn(turn))
	}
	record := func(err error) {
		runErr = err.Error()
	}

	pause := func(cp *gage.Checkpoint) {
		a.observe(ctx, Observation{
			Type:      ObservationRunPaused,
			RunID:     runID,
			Agent:     a.cfg.Name,
			Provider:  a.cfg.Provider.Name(),
			Turn:      cp.Turn,
			StartedAt: runStarted,
			Duration:  time.Since(runStarted),
		})
		send(gage.PausedEvent(cp).WithTurn(cp.Turn))
	}

	if resume != nil {
		cp := resume.cp
		results, stillPending, ok := a.resumeToolBatch(ctx, runID, cp, resume.decisions, send)
		if !ok {
			if ctx.Err() != nil {
				fail(cp.Turn, ctx.Err())
			}
			return
		}
		if stillPending {
			pause(&gage.Checkpoint{
				Messages:   cp.Messages,
				Turn:       cp.Turn,
				Usage:      cp.Usage,
				StopReason: cp.StopReason,
				Calls:      cp.Calls,
				Results:    results,
			})
			return
		}
		conversation = append([]gage.Message(nil), cp.Messages...)
		for _, r := range results {
			conversation = append(conversation, gage.ToolResultMessage(r))
		}
		startTurn = cp.Turn + 1
		runUsage = cp.Usage
		lastText = lastAssistantText(cp.Messages)
		lastStop = cp.StopReason
	}

	overBudget := func() bool {
		return a.cfg.TokenBudget > 0 && runUsage.Total() >= a.cfg.TokenBudget
	}
	budgetErr := func() error {
		return fmt.Errorf("%w: used %d of %d tokens", gage.ErrBudgetExceeded, runUsage.Total(), a.cfg.TokenBudget)
	}
	compact := func(reported gage.Usage) error {
		compacted, used, err := a.cfg.Compactor.Compact(ctx, conversation, reported)
		if err != nil {
			return fmt.Errorf("compaction: %w", err)
		}
		runUsage = runUsage.Add(used)
		conversation = compacted
		if overBudget() {
			return budgetErr()
		}
		return nil
	}

	var schemas []gage.ToolSchema
	if a.cfg.Registry != nil {
		schemas = a.cfg.Registry.Schemas()
	}

	// repeatSig/repeatCount track consecutive identical tool calls across
	// turns for the MaxToolRepeats guard.
	var repeatSig string
	var repeatCount int

	for turn := startTurn; turn < a.cfg.maxTurns(); turn++ {
		// Proactive compaction: shrink before the provider call when the
		// estimated conversation size already crosses the threshold, so an
		// oversized history never reaches the provider (the reported-usage
		// trigger below can only fire after a first successful call). With
		// Config.CountTokens the heuristic estimate is upgraded to the
		// provider's exact count when the capability is available, falling
		// back silently to the heuristic on error.
		if a.cfg.Compactor != nil && a.cfg.CompactAfter > 0 {
			est := gage.EstimateTokens(conversation) + gage.EstimateTextTokens(a.cfg.systemPrompt())
			if a.cfg.CountTokens {
				if counter, ok := a.cfg.Provider.(gage.TokenCounter); ok {
					if exact, err := counter.CountTokens(ctx, gage.Request{
						Model:    a.cfg.Model,
						Messages: conversation,
						Tools:    schemas,
						System:   a.cfg.systemPrompt(),
						Options:  a.cfg.Options,
					}); err == nil {
						est = exact
					}
				}
			}
			if est >= a.cfg.CompactAfter {
				if err := compact(gage.Usage{InputTokens: est}); err != nil {
					fail(turn, err)
					return
				}
			}
		}

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

		asst, ok := a.streamTurn(ctx, turn, req, send, fail, record, runID)
		if !ok {
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

		if overBudget() {
			fail(turn, budgetErr())
			return
		}

		// Doom-loop guard: past MaxToolRepeats consecutive identical calls the
		// call is answered with an error result instead of executing; one more
		// repetition aborts the run.
		var forced []*gage.ToolResult
		if a.cfg.MaxToolRepeats > 0 {
			forced = make([]*gage.ToolResult, len(asst.toolCalls))
			for i, tc := range asst.toolCalls {
				sig := tc.Name + "\x00" + string(tc.Input)
				if sig == repeatSig {
					repeatCount++
				} else {
					repeatSig, repeatCount = sig, 1
				}
				switch {
				case repeatCount == a.cfg.MaxToolRepeats+1:
					r := gage.ErrorResult(tc.ID, fmt.Sprintf(
						"loop detected: %s called %d times in a row with identical input; the call was not executed — change your approach or give your final answer",
						tc.Name, repeatCount))
					forced[i] = &r
				case repeatCount > a.cfg.MaxToolRepeats+1:
					fail(turn, fmt.Errorf("%w: %s repeated %d times with identical input", gage.ErrLoopDetected, tc.Name, repeatCount))
					return
				}
			}
		}

		// Execute the requested tool calls and feed results back.
		results, pendingCalls, ok := a.execTools(ctx, runID, turn, asst.toolCalls, forced, send)
		if !ok {
			if ctx.Err() != nil {
				fail(turn, ctx.Err())
			}
			return
		}
		if len(pendingCalls) > 0 {
			pause(&gage.Checkpoint{
				Messages:   conversation,
				Turn:       turn,
				Usage:      runUsage,
				StopReason: asst.stopReason,
				Calls:      asst.toolCalls,
				Results:    results,
			})
			return
		}
		for _, result := range results {
			conversation = append(conversation, gage.ToolResultMessage(result))
		}

		// Compact the conversation when the input side of the context window
		// crosses the configured threshold, per the provider's own accounting.
		if a.cfg.Compactor != nil && a.cfg.CompactAfter > 0 && asst.usage.InputTokens >= a.cfg.CompactAfter {
			if err := compact(asst.usage); err != nil {
				fail(turn, err)
				return
			}
		}
	}

	runErr = gage.ErrMaxTurns.Error()
	send(gage.ErrorEvent(gage.ErrMaxTurns).WithTurn(a.cfg.maxTurns()))
}

// streamTurn calls the provider for one turn, retrying retryable stream
// failures up to Config.MaxStreamRetries. ok is false when the turn (and the
// run) must stop; a terminal error event has then already been sent unless
// the consumer is gone.
func (a *Agent) streamTurn(ctx context.Context, turn int, req gage.Request, send func(gage.Event) bool, fail func(int, error), record func(error), runID string) (*assistantAccum, bool) {
	for attempt := 0; ; attempt++ {
		var errEv *gage.Event
		stream, err := a.cfg.Provider.Stream(ctx, req)
		if err == nil {
			var acc *assistantAccum
			var consumeErr error
			acc, errEv, consumeErr = a.consume(ctx, turn, stream, send)
			if consumeErr != nil {
				if ctx.Err() != nil {
					fail(turn, ctx.Err())
				} else {
					record(consumeErr)
				}
				return nil, false
			}
			if errEv == nil {
				return acc, true
			}
			err = errEv.Err
			if err == nil {
				err = errors.New(errEv.ErrorString)
			}
		}
		if attempt < a.cfg.MaxStreamRetries && retryable(err) {
			a.observe(ctx, Observation{
				Type:        ObservationTurnRetry,
				RunID:       runID,
				Agent:       a.cfg.Name,
				Provider:    a.cfg.Provider.Name(),
				Turn:        turn,
				StartedAt:   time.Now(),
				ErrorString: err.Error(),
			})
			if sleepBackoff(ctx, a.cfg.retryBaseDelay(), attempt) {
				continue
			}
			fail(turn, ctx.Err())
			return nil, false
		}
		if errEv != nil {
			// Forward the provider's own terminal error event verbatim.
			send(errEv.WithTurn(turn))
			record(err)
			return nil, false
		}
		fail(turn, err)
		return nil, false
	}
}

// retryable reports whether a stream failure is worth retrying: everything
// except cancellation, authentication failures, and unsupported options.
func retryable(err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, gage.ErrAuth) || errors.Is(err, gage.ErrUnsupported) {
		return false
	}
	return true
}

// sleepBackoff waits base<<attempt or until ctx is done, reporting whether the
// wait completed.
func sleepBackoff(ctx context.Context, base time.Duration, attempt int) bool {
	t := time.NewTimer(base << attempt)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// lastAssistantText returns the text of the most recent assistant message
// that has any, mirroring the loop's lastText tracking across a pause.
func lastAssistantText(msgs []gage.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == gage.RoleAssistant {
			if t := msgs[i].Text(); t != "" {
				return t
			}
		}
	}
	return ""
}

// resumeToolBatch finishes a checkpointed turn: it executes the pending calls
// that received a decision (skipping the pre-tool hook and the Approver — the
// decision replaces them) and merges them with the already-completed results,
// in original call order. stillPending is true when some calls remain without
// a decision. ok is false when ctx died mid-way.
func (a *Agent) resumeToolBatch(ctx context.Context, runID string, cp *gage.Checkpoint, decisions map[string]gage.Approval, send func(gage.Event) bool) (results []gage.ToolResult, stillPending bool, ok bool) {
	byID := make(map[string]gage.ToolResult, len(cp.Results))
	for _, r := range cp.Results {
		byID[r.CallID] = r
	}
	for _, tc := range cp.Calls {
		if _, done := byID[tc.ID]; done {
			continue
		}
		decision, decided := decisions[tc.ID]
		if !decided {
			stillPending = true
			continue
		}
		if ctx.Err() != nil {
			return nil, false, false
		}
		res, _ := a.execTool(ctx, runID, cp.Turn, tc, &decision)
		if !send(gage.ToolResultEvent(res).WithTurn(cp.Turn)) {
			return nil, false, false
		}
		byID[tc.ID] = res
	}
	for _, tc := range cp.Calls {
		if r, done := byID[tc.ID]; done {
			results = append(results, r)
		}
	}
	return results, stillPending, true
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
// assistant message. A terminal EventError is NOT forwarded: it is returned
// as errEv so the loop can decide to retry the turn or forward it. A non-nil
// err means delivery stopped (ctx cancelled or consumer gone) and the run
// must end.
func (a *Agent) consume(ctx context.Context, turn int, stream <-chan gage.Event, send func(gage.Event) bool) (acc *assistantAccum, errEv *gage.Event, err error) {
	acc = &assistantAccum{}
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
			e := ev
			return acc, &e, nil
		}
		if !send(ev.WithTurn(turn)) {
			if cerr := ctx.Err(); cerr != nil {
				return acc, nil, cerr
			}
			return acc, nil, errSendCancelled
		}
	}
	if ctx.Err() != nil {
		return acc, nil, ctx.Err()
	}
	return acc, nil, nil
}

// execTools runs a turn's tool calls — sequentially by default, concurrently
// when Config.MaxParallelTools > 1 — emitting each tool_result event as it
// completes. forced, when non-nil, is aligned with calls: a non-nil entry is
// used as the result without executing the tool. Calls whose approval is
// pending (gage.ErrApprovalPending) produce no result and are returned in
// pending. The returned results keep the order of calls so the conversation
// stays deterministic. ok is false when ctx was cancelled mid-way.
func (a *Agent) execTools(ctx context.Context, runID string, turn int, calls []gage.ToolCall, forced []*gage.ToolResult, send func(gage.Event) bool) (results []gage.ToolResult, pending []gage.ToolCall, ok bool) {
	parallel := a.cfg.MaxParallelTools
	if parallel <= 1 || len(calls) == 1 {
		results = make([]gage.ToolResult, 0, len(calls))
		for i, tc := range calls {
			if ctx.Err() != nil {
				return nil, nil, false
			}
			var result gage.ToolResult
			if forced != nil && forced[i] != nil {
				result = *forced[i]
			} else {
				var isPending bool
				result, isPending = a.execTool(ctx, runID, turn, tc, nil)
				if isPending {
					pending = append(pending, tc)
					continue
				}
			}
			if !send(gage.ToolResultEvent(result).WithTurn(turn)) {
				return nil, nil, false
			}
			results = append(results, result)
		}
		return results, pending, true
	}

	slots := make([]*gage.ToolResult, len(calls))
	pendingIdx := make([]bool, len(calls))
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
			var result gage.ToolResult
			if forced != nil && forced[i] != nil {
				result = *forced[i]
			} else {
				var isPending bool
				result, isPending = a.execTool(ctx, runID, turn, tc, nil)
				if isPending {
					pendingIdx[i] = true
					return
				}
			}
			slots[i] = &result
			sendMu.Lock()
			defer sendMu.Unlock()
			if !send(gage.ToolResultEvent(result).WithTurn(turn)) {
				cancelled = true
			}
		}(i, tc)
	}
	wg.Wait()
	if cancelled || ctx.Err() != nil {
		return nil, nil, false
	}
	for i, tc := range calls {
		if pendingIdx[i] {
			pending = append(pending, tc)
			continue
		}
		if slots[i] != nil {
			results = append(results, *slots[i])
		}
	}
	return results, pending, true
}

// execTool runs a single tool call through the hooks, registry and optional
// approver, always returning a ToolResult (errors are reported to the model,
// not the caller). A non-nil decision replaces the pre-tool hook and the
// Approver (resume path): it is applied as the approval outcome. pending is
// true when the Approver returned gage.ErrApprovalPending; the call then has
// no result and the run pauses.
func (a *Agent) execTool(ctx context.Context, runID string, turn int, tc gage.ToolCall, decision *gage.Approval) (res gage.ToolResult, pending bool) {
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
			pending = false
		}
		if !pending {
			if a.cfg.Hooks.PostToolUse != nil {
				res = a.cfg.Hooks.PostToolUse(ctx, tc, res)
			}
			if res.CallID == "" {
				res.CallID = tc.ID
			}
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
		if pending {
			obs.ErrorString = "approval pending"
		} else if res.IsError {
			obs.ErrorString = res.Text()
		}
		a.observe(ctx, obs)
	}()

	if a.cfg.Registry == nil {
		return gage.ErrorResult(tc.ID, "no tools available"), false
	}
	tool, ok := a.cfg.Registry.Get(tc.Name)
	if !ok {
		return gage.ErrorResult(tc.ID, "unknown tool: "+tc.Name), false
	}

	var approval gage.Approval
	switch {
	case decision != nil:
		approval = *decision
	default:
		if a.cfg.Hooks.PreToolUse != nil {
			updated, err := a.cfg.Hooks.PreToolUse(ctx, tc)
			if err != nil {
				return gage.ErrorResult(tc.ID, "blocked by pre-tool hook: "+err.Error()), false
			}
			updated.ID, updated.Name = tc.ID, tc.Name // hooks may only rewrite the input
			tc = updated
		}
		if a.cfg.Approver == nil {
			approval = gage.Allowed()
			break
		}
		var err error
		approval, err = a.cfg.Approver.Approve(ctx, gage.PermissionRequest{
			Tool:     tc.Name,
			Input:    tc.Input,
			Agent:    a.cfg.Name,
			RunID:    runID,
			Turn:     turn,
			Metadata: gage.MetadataOf(tool),
			Summary:  gage.CallSummaryOf(tool, tc.Input),
		})
		if err != nil {
			if errors.Is(err, gage.ErrApprovalPending) {
				return gage.ToolResult{}, true
			}
			return gage.ErrorResult(tc.ID, "permission check failed: "+err.Error()), false
		}
	}
	if !approval.Allow {
		msg := "permission denied for tool " + tc.Name
		if approval.Reason != "" {
			msg += ": " + approval.Reason
		}
		return gage.ErrorResult(tc.ID, msg), false
	}
	if approval.UpdatedInput != nil {
		tc.Input = approval.UpdatedInput
	}

	input := tc.Input
	if len(input) == 0 {
		input = json.RawMessage("{}")
	}
	out := a.callTool(ctx, tool, input, tc.Name)
	if out.panicValue != nil {
		return gage.ErrorResult(tc.ID, fmt.Sprintf("tool panic: %v", out.panicValue)), false
	}
	if out.err != nil {
		return gage.ErrorResult(tc.ID, out.err.Error()), false
	}
	// Ensure the result is correlated with the originating call id.
	out.result.CallID = tc.ID
	return out.result, false
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

// errSendCancelled marks a run whose consumer stopped receiving events.
var errSendCancelled = &loopError{"event delivery cancelled"}

type loopError struct{ msg string }

func (e *loopError) Error() string { return e.msg }
