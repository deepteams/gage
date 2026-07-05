package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/deepteams/gage"
)

// terminalInteractor is the non-TUI fallback for approvals and question tool
// prompts. Answering "a" sets Approval.Remember; gage.RememberingPerInput
// caches that by tool + exact input.
//
// The agent runs tools with MaxParallelTools > 1, so Approve/AskQuestion can be
// called concurrently. mu serializes access to the shared stdin reader so two
// prompts never race on it (and one prompt's answer is never consumed by
// another).
type terminalInteractor struct {
	mu  sync.Mutex
	in  *bufio.Reader
	out io.Writer
}

func (a *terminalInteractor) AskApproval(_ context.Context, req gage.PermissionRequest) (gage.Approval, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	// Summary is filled by the agent from the tool's ToolCallDescriber; fall
	// back to the raw input for tools that do not describe their calls.
	summary := req.Summary
	if summary == "" {
		summary = req.Tool + " " + string(req.Input)
	}
	fmt.Fprintf(a.out, "\n\x1b[33m⚠ %s\x1b[0m\n  allow? [y]es / [a]lways for this exact call / [N]o: ", summary)

	line, err := a.in.ReadString('\n')
	if err != nil {
		return gage.Denied("approval prompt unavailable"), nil
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return gage.Allowed(), nil
	case "a", "always":
		return gage.Approval{Allow: true, Remember: true}, nil
	default:
		return gage.Denied("the user denied this call; explain what you wanted to do or try another approach"), nil
	}
}

func (a *terminalInteractor) AskQuestion(_ context.Context, question string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	fmt.Fprintf(a.out, "\n\x1b[36m? %s\x1b[0m\n  answer: ", question)
	line, err := a.in.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(line), nil
}
