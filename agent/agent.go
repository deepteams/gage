package agent

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/deepteams/gage"
)

var runSeq atomic.Uint64

// Agent runs the agentic loop over a provider and tool registry.
type Agent struct {
	cfg Config
}

// New builds an Agent from cfg. It returns gage.ErrNoProvider if no provider is set.
func New(cfg Config) (*Agent, error) {
	if cfg.Provider == nil {
		return nil, gage.ErrNoProvider
	}
	return &Agent{cfg: cfg}, nil
}

// Run executes the agentic loop starting from input and streams all events
// (provider deltas, tool results, and a terminal EventDone) on the returned
// channel, which is closed when the run completes. Cancelling ctx stops the run.
func (a *Agent) Run(ctx context.Context, input []gage.Message) (<-chan gage.Event, error) {
	runID := nextRunID()
	if a.cfg.Timeout > 0 {
		runCtx, cancel := context.WithTimeout(ctx, a.cfg.Timeout)
		// cancel is called when the loop goroutine finishes (see runLoop).
		return a.start(runCtx, ctx, input, nil, cancel, runID), nil
	}
	return a.start(ctx, ctx, input, nil, nil, runID), nil
}

// Resume continues a run that paused awaiting tool approval (EventPaused).
// decisions maps pending ToolCall IDs to their outcome; approved calls are
// executed (the recorded decision replaces the Approver), denied calls are
// reported to the model as error results. Pending calls without a decision
// leave the run paused: a fresh EventPaused with an updated Checkpoint is
// emitted. Tool results already present in the checkpoint are not re-emitted;
// only newly executed calls stream tool_result events.
func (a *Agent) Resume(ctx context.Context, cp *gage.Checkpoint, decisions map[string]gage.Approval) (<-chan gage.Event, error) {
	if cp == nil || len(cp.Messages) == 0 || len(cp.Calls) == 0 {
		return nil, errors.New("agent: invalid checkpoint")
	}
	resume := &resumeState{cp: cp, decisions: decisions}
	runID := nextRunID()
	if a.cfg.Timeout > 0 {
		runCtx, cancel := context.WithTimeout(ctx, a.cfg.Timeout)
		return a.start(runCtx, ctx, nil, resume, cancel, runID), nil
	}
	return a.start(ctx, ctx, nil, resume, nil, runID), nil
}

// ResumeSync is the blocking form of Resume; see RunSync.
func (a *Agent) ResumeSync(ctx context.Context, cp *gage.Checkpoint, decisions map[string]gage.Approval) (*gage.Result, error) {
	stream, err := a.Resume(ctx, cp, decisions)
	if err != nil {
		return nil, err
	}
	return Collect(ctx, stream)
}

func (a *Agent) start(runCtx, sendCtx context.Context, input []gage.Message, resume *resumeState, cancel context.CancelFunc, runID string) <-chan gage.Event {
	out := make(chan gage.Event)
	go func() {
		defer close(out)
		if cancel != nil {
			defer cancel()
		}
		a.runLoop(runCtx, sendCtx, input, resume, out, runID)
	}()
	return out
}

func nextRunID() string {
	return fmt.Sprintf("%d-%d", time.Now().UnixNano(), runSeq.Add(1))
}

// RunSync executes the loop and blocks until it completes, returning the run
// summary. Streaming consumers should use Run; RunSync is the convenience for
// callers that only want the outcome. When the run pauses awaiting approval,
// the returned error is a *Paused carrying the checkpoint.
func (a *Agent) RunSync(ctx context.Context, input []gage.Message) (*gage.Result, error) {
	if a.cfg.Timeout > 0 {
		runCtx, cancel := context.WithTimeout(ctx, a.cfg.Timeout)
		defer cancel()
		return Collect(runCtx, a.start(runCtx, ctx, input, nil, nil, nextRunID()))
	}
	stream, err := a.Run(ctx, input)
	if err != nil {
		return nil, err
	}
	return Collect(ctx, stream)
}

// Paused is the error returned by the blocking helpers when a run suspended
// awaiting tool approval. It carries the checkpoint to persist and later pass
// to Resume, and matches gage.ErrApprovalPending under errors.Is.
type Paused struct {
	Checkpoint *gage.Checkpoint
}

func (p *Paused) Error() string { return "agent: run paused awaiting tool approval" }

// Is lets errors.Is(err, gage.ErrApprovalPending) match a paused run.
func (p *Paused) Is(target error) bool { return target == gage.ErrApprovalPending }

// Collect drains an agent event stream and returns its terminal Result. It
// returns the stream's error event as a Go error, a *Paused error when the
// run suspended awaiting approval, and ctx.Err() when the stream closed
// because the run was cancelled or timed out before finishing.
func Collect(ctx context.Context, stream <-chan gage.Event) (*gage.Result, error) {
	var lastErr error
	for ev := range stream {
		switch ev.Type {
		case gage.EventDone:
			if ev.Result != nil {
				return ev.Result, nil
			}
		case gage.EventPaused:
			if ev.Checkpoint != nil {
				lastErr = &Paused{Checkpoint: ev.Checkpoint}
			}
		case gage.EventError:
			if ev.Err != nil {
				lastErr = ev.Err
			} else {
				lastErr = &loopError{ev.ErrorString}
			}
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return nil, &loopError{"run ended without a result"}
}
