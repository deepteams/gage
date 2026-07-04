package agent

import (
	"context"
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
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, a.cfg.Timeout)
		// cancel is called when the loop goroutine finishes (see runLoop).
		return a.start(ctx, input, cancel, runID), nil
	}
	return a.start(ctx, input, nil, runID), nil
}

func (a *Agent) start(ctx context.Context, input []gage.Message, cancel context.CancelFunc, runID string) <-chan gage.Event {
	out := make(chan gage.Event)
	go func() {
		defer close(out)
		if cancel != nil {
			defer cancel()
		}
		a.runLoop(ctx, input, out, runID)
	}()
	return out
}

func nextRunID() string {
	return fmt.Sprintf("%d-%d", time.Now().UnixNano(), runSeq.Add(1))
}

// RunSync executes the loop and blocks until it completes, returning the run
// summary. Streaming consumers should use Run; RunSync is the convenience for
// callers that only want the outcome.
func (a *Agent) RunSync(ctx context.Context, input []gage.Message) (*gage.Result, error) {
	stream, err := a.Run(ctx, input)
	if err != nil {
		return nil, err
	}
	return Collect(ctx, stream)
}

// Collect drains an agent event stream and returns its terminal Result. It
// returns the stream's error event as a Go error, and ctx.Err() when the
// stream closed because the run was cancelled or timed out before finishing.
func Collect(ctx context.Context, stream <-chan gage.Event) (*gage.Result, error) {
	var lastErr error
	for ev := range stream {
		switch ev.Type {
		case gage.EventDone:
			if ev.Result != nil {
				return ev.Result, nil
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
