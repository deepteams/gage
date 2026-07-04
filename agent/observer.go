package agent

import (
	"context"
	"time"

	"github.com/deepteams/gage"
)

// ObservationType identifies a lifecycle observation emitted by an Agent.
type ObservationType string

const (
	ObservationRunStart  ObservationType = "run_start"
	ObservationRunEnd    ObservationType = "run_end"
	ObservationTurnStart ObservationType = "turn_start"
	ObservationTurnEnd   ObservationType = "turn_end"
	ObservationToolStart ObservationType = "tool_start"
	ObservationToolEnd   ObservationType = "tool_end"
)

// Observation is a structured audit/telemetry event. It intentionally uses
// strings and durations so callers can map it to logs, metrics, traces, or
// custom audit stores without gage owning an observability stack.
type Observation struct {
	Type        ObservationType
	RunID       string
	Agent       string
	Provider    string
	Turn        int
	Tool        string
	CallID      string
	StartedAt   time.Time
	Duration    time.Duration
	IsError     bool
	ErrorString string
	// Usage is the provider-reported token usage of the turn (turn_end only).
	Usage gage.Usage
}

// Observer consumes structured Agent lifecycle observations.
type Observer interface {
	Observe(ctx context.Context, obs Observation)
}

// ObserverFunc adapts a function into an Observer.
type ObserverFunc func(ctx context.Context, obs Observation)

func (f ObserverFunc) Observe(ctx context.Context, obs Observation) {
	f(ctx, obs)
}
