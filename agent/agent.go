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
