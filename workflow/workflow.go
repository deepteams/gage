// Package workflow adds durable session/checkpoint handling around an agent.
package workflow

import (
	"context"
	"errors"
	"fmt"

	"github.com/deepteams/gage"
	"github.com/deepteams/gage/agent"
)

// Agent is the subset of *agent.Agent used by Runner.
type Agent interface {
	RunSync(ctx context.Context, input []gage.Message) (*gage.Result, error)
	ResumeSync(ctx context.Context, cp *gage.Checkpoint, decisions map[string]gage.Approval) (*gage.Result, error)
}

// Runner persists completed conversations and paused checkpoints in a
// SessionStore, so an application can survive process restarts between an
// approval request and its later decision.
type Runner struct {
	Agent Agent
	Store gage.SessionStore
}

// Outcome is the result of a durable run. Exactly one of Result or Checkpoint
// is normally set.
type Outcome struct {
	Result     *gage.Result
	Checkpoint *gage.Checkpoint
}

// New returns a durable workflow runner.
func New(a Agent, store gage.SessionStore) *Runner {
	return &Runner{Agent: a, Store: store}
}

// Run appends input to the stored session history and runs the agent. On
// completion it stores the full conversation; on approval pause it stores the
// checkpoint and returns it in the Outcome.
func (r *Runner) Run(ctx context.Context, sessionID string, input []gage.Message) (Outcome, error) {
	if err := r.ready(sessionID); err != nil {
		return Outcome{}, err
	}
	s, err := r.loadOrEmpty(ctx, sessionID)
	if err != nil {
		return Outcome{}, err
	}
	if s.Checkpoint != nil {
		return Outcome{Checkpoint: s.Checkpoint}, fmt.Errorf("%w: session %q is already paused", gage.ErrApprovalPending, sessionID)
	}
	msgs := append(append([]gage.Message(nil), s.Messages...), input...)
	res, err := r.Agent.RunSync(ctx, msgs)
	return r.persistOutcome(ctx, sessionID, res, err)
}

// Resume continues a paused session with the supplied approval decisions.
func (r *Runner) Resume(ctx context.Context, sessionID string, decisions map[string]gage.Approval) (Outcome, error) {
	if err := r.ready(sessionID); err != nil {
		return Outcome{}, err
	}
	s, err := r.Store.LoadSession(ctx, sessionID)
	if err != nil {
		return Outcome{}, err
	}
	if s.Checkpoint == nil {
		return Outcome{}, fmt.Errorf("workflow: session %q has no checkpoint", sessionID)
	}
	res, err := r.Agent.ResumeSync(ctx, s.Checkpoint, decisions)
	return r.persistOutcome(ctx, sessionID, res, err)
}

func (r *Runner) ready(sessionID string) error {
	if r.Agent == nil {
		return fmt.Errorf("workflow: nil agent")
	}
	if r.Store == nil {
		return fmt.Errorf("workflow: nil session store")
	}
	if sessionID == "" {
		return fmt.Errorf("workflow: empty session id")
	}
	return nil
}

func (r *Runner) loadOrEmpty(ctx context.Context, sessionID string) (gage.Session, error) {
	s, err := r.Store.LoadSession(ctx, sessionID)
	if err == nil {
		return s, nil
	}
	if errors.Is(err, gage.ErrSessionNotFound) {
		return gage.Session{}, nil
	}
	return gage.Session{}, err
}

func (r *Runner) persistOutcome(ctx context.Context, sessionID string, res *gage.Result, err error) (Outcome, error) {
	if err == nil {
		if res == nil {
			return Outcome{}, fmt.Errorf("workflow: agent returned nil result")
		}
		if saveErr := r.Store.SaveSession(ctx, sessionID, gage.Session{Messages: res.Messages}); saveErr != nil {
			return Outcome{}, saveErr
		}
		return Outcome{Result: res}, nil
	}
	var paused *agent.Paused
	if errors.As(err, &paused) && paused.Checkpoint != nil {
		s := gage.Session{Messages: paused.Checkpoint.Messages, Checkpoint: paused.Checkpoint}
		if saveErr := r.Store.SaveSession(ctx, sessionID, s); saveErr != nil {
			return Outcome{}, saveErr
		}
		return Outcome{Checkpoint: paused.Checkpoint}, err
	}
	return Outcome{}, err
}
